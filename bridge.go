// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/emiago/diago/audio"
	"github.com/emiago/diago/media"
)

var bridgeReadPool = sync.Pool{
	New: func() any {
		b := make([]byte, media.RTPBufSize)
		return &b
	},
}

func proxyMediaBackground(log *slog.Logger, reader io.Reader, writer io.Writer, ch chan error) {
	buf := rtpBufPool.Get()
	defer rtpBufPool.Put(buf)

	written, err := copyWithBuf(reader, writer, buf.([]byte))
	log.Debug("Proxy media routine finished", "bytes", written)
	if err, ok := err.(net.Error); ok && err.Timeout() {
		log.Debug("Proxy media stopped with timeout. RTP Deadline", "error", err)
		err = nil
	}
	ch <- err
}

// BridgeMix is mixing audio when having 2 or more parties.
//
// Experimental: not fully tested yet
type BridgeMix struct {
	mu     sync.Mutex
	medias []*DialogMedia

	mixWG    sync.WaitGroup
	mixState int

	// WaitDialogsNum is just helper flag when to start proxy
	WaitDialogsNum int
	// RealtimeReader is almost always nesessary if you are delaying audio streaming(mixing) in bridge
	RealtimeReader bool
	Poll           bool
	log            *slog.Logger
}

var (
	// BridgeDebug enables some traces
	BridgeDebug bool

	bridgeTrace = func(args ...any) {
		if BridgeDebug {
			fmt.Fprintln(os.Stderr, args...)
		}
	}
)

func NewBridgeMix() *BridgeMix {
	b := BridgeMix{
		RealtimeReader: true,
		Poll:           true,
	}
	b.Init()
	return &b
}

// Init initializes bridge struct. Use only if construct bridge with struct
// or use NewBridgeMix
func (b *BridgeMix) Init() {
	b.log = media.DefaultLogger().With("caller", "bridge_mix")
}

func (b *BridgeMix) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	str := fmt.Sprintf("state: %d", b.mixState)
	str += " medias:["
	for _, m := range b.medias {
		str += fmt.Sprintf(" %d", m.RTPPacketWriter.SSRC)
	}
	str += "]"
	return str
}

// DialogMediaList returns list of medias in bridge.
// It is not safe to use media until it is removed from bridge.
func (b *BridgeMix) DialogMediaList() []*DialogMedia {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.medias)
}

func (b *BridgeMix) AddDialogMedia(m *DialogMedia) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if m == nil {
		return fmt.Errorf("dialog media is nil")
	}

	// Stop any current mixing
	b.log.Debug("Stoping mix")
	if err := b.mixStopWait(); err != nil {
		return fmt.Errorf("failed to stop current mixing: %w", err)
	}

	b.medias = append(b.medias, m)
	b.log.Debug("Added media", "total", len(b.medias))
	b.mixStart()
	return nil
}

func (b *BridgeMix) RemoveDialogMedia(m *DialogMedia) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	var media *DialogMedia
	for _, bm := range b.medias {
		if bm == m {
			media = bm
			break
		}
	}
	if media == nil {
		return nil
	}

	b.log.Debug("Stoping mix")

	if err := b.mixStopWait(); err != nil {
		return fmt.Errorf("failed to stop current mixing: %w", err)
	}

	// NOTE: mixStopWait unlocks so we can not do any update before
	for i, bm := range b.medias {
		if bm == m {
			b.medias = append(b.medias[:i], b.medias[i+1:]...)
			break
		}
	}

	b.log.Debug("Removed media", "total", len(b.medias))
	return b.mixStart()
}

func (b *BridgeMix) stateWrite(s int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stateWriteUnsafe(s)
}

func (b *BridgeMix) stateWriteUnsafe(s int) {
	b.mixState = s
}

func (b *BridgeMix) stateRead() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.mixState
}

func (b *BridgeMix) mixStopWait() error {
	// DO NOT CALL THIS INSIDE LOOP of b.dialogs. This Unlocks
	stopInProgress, err := b.mixStop()
	if err != nil {
		return fmt.Errorf("failed to stop current mixing: %w", err)
	}

	if stopInProgress {
		b.mu.Unlock()
		b.mixWG.Wait()
		b.mu.Lock()
	}
	// Enable RTP again
	var allErros error
	for _, m := range b.medias {
		err := m.StartRTP(1, 0) // Start reading
		errors.Join(allErros, err)
	}
	return allErros
}

func (b *BridgeMix) mixStop() (bool, error) {
	if state := b.mixState; state != 1 {
		// Only if state is running this goroutine can stop it
		return false, nil
	}
	b.mixState = 2 // Set it stoping in progress
	var allErros error
	for _, m := range b.medias {
		err := m.StopRTP(1, 0) // Stop reading
		errors.Join(allErros, err)
	}
	return true, allErros
}

func (b *BridgeMix) mixStart() error {
	if b.mixState == 2 {
		// A stop is in progress (another goroutine is in mixStopWait).
		// Don't start a new mix to avoid WaitGroup Add/Wait race.
		return nil
	}
	if len(b.medias) < 1 {
		return nil
	}
	if len(b.medias) < b.WaitDialogsNum {
		return nil
	}

	ctx, cancelPoll := context.WithCancel(context.Background())
	// We could decide and optimize here, poll vs deadlines
	poll := b.Poll
	rwStreams, err := func() ([]*bridgePCMStream, error) {
		rwStreams := make([]*bridgePCMStream, len(b.medias))
		firstDialogCodec := media.Codec{}

		for i, m := range b.medias {
			rwStreams[i] = &bridgePCMStream{}
			if err := b.addDialogStream(ctx, m, rwStreams[i], &firstDialogCodec, poll); err != nil {
				return nil, err
			}
		}
		return rwStreams, nil
	}()
	if err != nil {
		cancelPoll()
		return err
	}

	// Start new mix
	b.mixWG.Add(1)
	b.stateWriteUnsafe(1)
	go func(rwStreams []*bridgePCMStream) {
		defer cancelPoll()
		defer b.mixWG.Done()
		defer b.stateWrite(0)
		b.log.Debug("Starting mix loop", "streams.len", len(rwStreams))
		if err := b.mixLoop(rwStreams, poll); err != nil {
			b.log.Info("Mix stopped with error", "error", err)
		}
	}(rwStreams)
	return nil
}

func (b *BridgeMix) mixLoop(rwStreams []*bridgePCMStream, poll bool) error {
	mixBuf := make([]byte, media.RTPBufSize)

	if len(rwStreams) == 1 {
		b.log.Debug("Only single stream in bridge, reading bufffers...")
		// Just keep streaming
		r := rwStreams[0]
		if !poll {
			_, err := media.ReadAll(r.r, media.RTPBufSize)
			return err
		}

		for {
			bw, more := <-r.pipeWrite
			if !more {
				break
			}
			n := copy(r.buf, bw)
			r.pipeRead <- n
		}
		return nil
	}

	// Currently we consider that sample clock is done by Audio Writers
	// The slowest will cause jitter.
	// TODO fix this with single ticker
	for {
		n, err := b.mixAllStreams(rwStreams, mixBuf, poll)
		if err != nil {
			return err
		}
		if n == 0 {
			bridgeTrace("Nothing read, delaying read")
			time.Sleep(50 * time.Millisecond)
			continue
		}

		// broadcast to all
		for i, w := range rwStreams {
			streamBuf := mixBuf[:n]
			if w.n > 0 {
				readBuf := w.buf
				streamBuf = unmixStream(readBuf[:w.n], mixBuf[:n])
			}

			n, err := w.w.Write(streamBuf)
			bridgeTrace("Writing stream", "i", i, "stream", w.id, "n", n, "err", err)
			if err != nil {
				// Detect is this Deadline or EOF error caused by stream exiting
				if errors.Is(err, os.ErrDeadlineExceeded) {
					state := b.stateRead()
					if state != 1 {
						// We are stopped
						return err
					}

					// Mixing has been stopped or network problem
					w.markGone = true
					continue

				}
				return err
			}
		}
	}
}

type bridgePCMStream struct {
	id           uint32
	r            io.Reader
	w            io.Writer
	mediaSession *media.MediaSession
	// read buf
	buf []byte
	n   int

	pipeRead  chan int
	pipeWrite chan []byte
	markGone  bool
}

func (b *BridgeMix) addDialogStream(ctx context.Context, m *DialogMedia, stream *bridgePCMStream, firstDialogCodec *media.Codec, poll bool) error {
	p := MediaProps{}
	r, err := m.AudioReader(WithAudioReaderMediaProps(&p))
	if err != nil {
		return err
	}

	if firstDialogCodec.SampleRate == 0 {
		firstDialogCodec = &p.Codec
	}

	if firstDialogCodec.SampleRate != p.Codec.SampleRate && firstDialogCodec.SampleDur != p.Codec.SampleDur {
		return fmt.Errorf("Codec missmatch. Resampling or transcoding is not supported")
	}

	rtr := func() io.Reader {
		if !b.RealtimeReader {
			return r
		}

		if rtr, ok := r.(*media.RTPRealTimeReader); ok {
			return rtr
		}

		rtr := media.NewRTPRealTimeReader(r, m.RTPPacketReader, p.Codec)
		m.SetAudioReader(rtr)
		return rtr
	}()

	// Attach PCM decoder
	pcmReader := audio.PCMDecoderReader{}
	if err := pcmReader.Init(p.Codec, rtr); err != nil {
		return err
	}

	// Now do write stream
	p = MediaProps{}
	w, err := m.AudioWriter(WithAudioWriterMediaProps(&p))
	if err != nil {
		return err
	}

	pcmWriter := audio.PCMEncoderWriter{}
	if err := pcmWriter.Init(p.Codec, w); err != nil {
		return err
	}

	*stream = bridgePCMStream{
		r:            &pcmReader,
		w:            &pcmWriter,
		mediaSession: m.mediaSession,
		id:           m.RTPPacketWriter.SSRC,
		buf:          make([]byte, media.RTPBufSize),
		pipeRead:     make(chan int),
		pipeWrite:    make(chan []byte),
	}

	if poll {
		// We do buffering because initial packet can be read oner than actual mixing has started
		b.mixWG.Add(1)
		bridgeTrace("poll: starting stream", "stream.id", stream.id)
		go func(s *bridgePCMStream) {
			defer b.mixWG.Done()

			bufPtr := bridgeReadPool.Get().(*[]byte)
			defer bridgeReadPool.Put(bufPtr)

			defer close(s.pipeWrite)

			buf := *bufPtr
			for {
				n, err := s.r.Read(buf)
				if err != nil {
					bridgeTrace("poll: stopped with error", "error", err, "stream.id", stream.id)
					return
				}

				select {
				case s.pipeWrite <- buf[:n]:
					nw := <-s.pipeRead
					if nw != n {
						// there is no reason this to happen, so lets panic
						panic("reading from pipe was not full")
					}
				case <-ctx.Done():
					bridgeTrace("poll: stream context canceled", "stream.id", stream.id)
					return
				}
			}
		}(stream)
		return nil
	}
	return nil
}

func (b *BridgeMix) mixAllStreams(rwStreams []*bridgePCMStream, mixedBuf []byte, poll bool) (int, error) {
	maxN := 0
	// zero mixed buf
	for i := 0; i < len(mixedBuf); i++ {
		mixedBuf[i] = 0
		// binary.LittleEndian.PutUint16(mixedBuf[i:], uint16(0))
	}

	if !poll {
		// If are not polling data then we need todo direct read
		err := func() error {
			for i, r := range rwStreams {
				r.mediaSession.StopRTP(1, 1*time.Millisecond)

				// Mostly PCM sample size should be same or less our sampling
				// but we should keep same sampling or deal this per writer?
				n, err := r.r.Read(r.buf)
				rwStreams[i].n = n
				if err != nil {
					if errors.Is(err, os.ErrDeadlineExceeded) {
						state := b.stateRead()
						if state != 1 {
							// We are stopped
							return err
						}
						continue
					}
					return err
				}
				maxN = max(maxN, n)
			}
			return nil
		}()
		return maxN, err
	}

	err := func() error {
		handledStreams := len(rwStreams)
		for _, r := range rwStreams {
			if r.markGone {
				handledStreams--
				continue
			}
			r.n = 0 // Make sure it is zero

			select {
			case bw, more := <-r.pipeWrite:
				if !more {
					r.markGone = true
					continue
				}
				n := copy(r.buf, bw)
				r.n = n
				r.pipeRead <- n

				readBuf := r.buf[:n]
				mixN := audio.PCMMix(mixedBuf, mixedBuf, readBuf)
				maxN = max(maxN, mixN)

			default:
				// Do not block
				b.log.Debug("poll: no packet on stream", "stream.id", r.id)
			}
		}

		if handledStreams == 0 {
			return fmt.Errorf("all streams are gones")
		}

		if handledStreams < len(rwStreams) || maxN == 0 {
			state := b.stateRead()
			if state != 1 {
				// We are stopped
				return fmt.Errorf("reading is stopped")
			}
		}

		return nil
	}()

	b.log.Debug("Mixing done", "streams.len", len(rwStreams), "maxN", maxN)
	return maxN, err
}

func unmixStream(buf []byte, mixedBuf []byte) []byte {
	n := len(mixedBuf)
	if len(buf) < len(mixedBuf) {
		// panic("stream buf is shorter than mixed buf")
	}

	readBuf := buf[:n]
	audio.PCMUnmix(readBuf, mixedBuf, readBuf)
	// NOTE: This can be higher than actual read bytes
	return readBuf
}
