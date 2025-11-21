// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/emiago/diago/audio"
	"github.com/emiago/diago/media"
)

type Bridger interface {
	AddDialogSession(d DialogSession) error
}

type Bridge struct {
	// Originator is dialog session that created bridge
	Originator DialogSession
	// DTMFpass is also dtmf pipeline and proxy. By default only audio media is proxied
	// NOTE: this may not work if you are already processing DTMF with AudioReaderDTMF
	DTMFpass bool

	log *slog.Logger
	// TODO: RTPpass. RTP pass means that RTP will be proxied.
	// This gives high performance but you can not attach any pipeline in media processing
	// RTPpass bool

	dialogs []DialogSession

	// minDialogs is just helper flag when to start proxy
	WaitDialogsNum int
}

// NewBridge creates bridge with default settings.
func NewBridge() Bridge {
	b := Bridge{}
	b.Init(media.DefaultLogger())
	return b
}

func (b *Bridge) Init(log *slog.Logger) {
	b.log = log
	if b.log == nil {
		b.log = media.DefaultLogger()
	}

	if b.WaitDialogsNum == 0 {
		b.WaitDialogsNum = 2
	}
}

func (b *Bridge) GetDialogs() []DialogSession {
	return b.dialogs
}

func (b *Bridge) AddDialogSession(d DialogSession) error {
	// Check can this dialog be added to bridge. NO TRANSCODING
	if b.Originator != nil {
		// This may look ugly but it is safe way of reading
		origM := b.Originator.Media()
		origProps := MediaProps{}
		_ = origM.audioWriterProps(&origProps)

		m := d.Media()
		mprops := MediaProps{}
		_ = m.audioWriterProps(&mprops)

		err := func() error {
			if origProps.Codec != mprops.Codec {
				return fmt.Errorf("no transcoding supported in bridge codec1=%+v codec2=%+v", origProps.Codec, mprops.Codec)
			}
			return nil
		}()
		if err != nil {
			return err
		}
	}

	b.dialogs = append(b.dialogs, d)
	if len(b.dialogs) == 1 {
		b.Originator = d
	}

	if len(b.dialogs) < b.WaitDialogsNum {
		return nil
	}

	if len(b.dialogs) > 2 {
		return fmt.Errorf("currently bridge only support 2 party")
	}
	// Check are both answered
	for _, d := range b.dialogs {
		// TODO remove this double locking. Read once
		if d.Media().RTPPacketReader == nil || d.Media().RTPPacketWriter == nil {
			return fmt.Errorf("dialog session not answered %q", d.Id())
		}
	}

	go func() {
		defer func(start time.Time) {
			b.log.Debug("Proxy media setup", "dur", time.Since(start).String())
		}(time.Now())
		if err := b.proxyMedia(); err != nil {
			if errors.Is(err, io.EOF) {
				return
			}

			b.log.Error("Proxy media stopped", "error", err)
		}
	}()
	return nil
}

// ProxyMedia is explicit starting proxy media.
// In some cases you want to control and be signaled when bridge terminates
//
// NOTE: Should be only called if you want to start manually proxying.
// It is required to set WaitDialogsNum higher than 2
//
// Experimental
func (b *Bridge) ProxyMedia() error {
	if len(b.dialogs) < 2 {
		return fmt.Errorf("number of dialogs must equal to 2")
	}

	if b.WaitDialogsNum < 3 {
		return fmt.Errorf("you are already running proxy media. Increase WaitDialogsNum")
	}

	for _, d := range b.dialogs {
		d.Media().mediaSession.StopRTP(2, 0)
	}
	return b.proxyMedia()
}

// ProxyMediaControl starts proxy in background and allows to stop proxy at any time.
// Stop should be called once and it is not needed to be called if call is terminating
//
// Experimental
func (b *Bridge) ProxyMediaControl() (func() error, error) {
	proxyErr := make(chan error, 1)
	go func() {
		proxyErr <- b.proxyMedia()
	}()

	stopF := func() error {
		for _, d := range b.dialogs {
			d.Media().mediaSession.StopRTP(2, 0)
		}

		// Wait goroutine termination
		err := <-proxyErr
		for _, d := range b.dialogs {
			d.Media().mediaSession.StartRTP(2)
		}
		return err
	}

	return stopF, b.proxyMedia()
}

// proxyMedia starts routine to proxy media between
// Should be called after having 2 or more participants
func (b *Bridge) proxyMedia() error {
	var err error
	log := b.log

	m1 := b.dialogs[0].Media()
	m2 := b.dialogs[1].Media()

	// Lets for now simplify proxy and later optimize

	if b.DTMFpass {
		errCh := make(chan error, 4)
		go func() {
			errCh <- b.proxyMediaWithDTMF(m1, m2)
		}()

		go func() {
			errCh <- b.proxyMediaWithDTMF(m2, m1)
		}()

		// Wait for all to finish
		for i := 0; i < 2; i++ {
			err = errors.Join(err, <-errCh)
		}
		return err
	}
	errCh := make(chan error, 2)
	func() {
		p1, p2 := MediaProps{}, MediaProps{}
		r := m1.audioReaderProps(&p1)
		w := m2.audioWriterProps(&p2)

		log := log.With("from", p1.Raddr+" > "+p1.Laddr, "to", p2.Laddr+" > "+p2.Raddr)
		log.Debug("Starting proxy media routine")
		go proxyMediaBackground(log, r, w, errCh)
	}()

	// Second
	func() {
		p1, p2 := MediaProps{}, MediaProps{}
		r := m2.audioReaderProps(&p1)
		w := m1.audioWriterProps(&p2)
		log := log.With("from", p1.Raddr+" > "+p1.Laddr, "to", p2.Laddr+" > "+p2.Raddr)
		log.Debug("Starting proxy media routine")
		go proxyMediaBackground(log, r, w, errCh)
	}()

	// Wait for all to finish
	for i := 0; i < 2; i++ {
		err = errors.Join(err, <-errCh)
	}
	return err
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

func (b *Bridge) proxyMediaWithDTMF(m1 *DialogMedia, m2 *DialogMedia) error {
	dtmfReader := DTMFReader{}
	p1, p2 := MediaProps{}, MediaProps{}
	r, err := m1.AudioReader(WithAudioReaderDTMF(&dtmfReader), WithAudioReaderMediaProps(&p1))
	if err != nil {
		return err
	}
	dtmfWriter := DTMFWriter{}
	w, err := m2.AudioWriter(WithAudioWriterDTMF(&dtmfWriter), WithAudioWriterMediaProps(&p2))
	if err != nil {
		return err
	}
	dtmfReader.OnDTMF(func(dtmf rune) error {
		return dtmfWriter.WriteDTMF(dtmf)
	})

	buf := rtpBufPool.Get()
	defer rtpBufPool.Put(buf)

	log := b.log.With("from", p1.Raddr+" > "+p1.Laddr, "to", p2.Laddr+" > "+p2.Raddr)
	log.Debug("Starting proxy media routine")
	written, err := copyWithBuf(r, w, buf.([]byte))
	log.Debug("Bridge proxy stream finished", "bytes", written)
	return err
}

type BridgeMix struct {
	// Originator is dialog session that created bridge
	Originator DialogSession

	// TODO: RTPpass. RTP pass means that RTP will be proxied.
	// This gives high performance but you can not attach any pipeline in media processing
	// RTPpass bool

	mu              sync.Mutex
	dialogs         []DialogSession
	originatorCodec media.Codec

	mixWG    sync.WaitGroup
	mixErr   error
	mixState atomic.Int32

	// minDialogs is just helper flag when to start proxy
	WaitDialogsNum int
}

func NewBridgeMix() BridgeMix {
	b := BridgeMix{}
	b.Init()
	return b
}

func (b *BridgeMix) Init() {
}

func (b *BridgeMix) AddDialogSession(d DialogSession) error {
	if b.Originator == nil {
		b.Originator = d

		m := d.Media()
		p := MediaProps{}
		_, err := m.AudioReader(WithAudioReaderMediaProps(&MediaProps{}))
		if err != nil {
			return err
		}
		b.originatorCodec = p.Codec
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	b.dialogs = append(b.dialogs, d)

	// Stop any current mixing
	fmt.Println("Mixing stopped by add", d.Id())
	if err := b.mixStop(); err != nil {
		return fmt.Errorf("failed to stop current mixing: %w", err)
	}

	// TODO add here stream build up
	b.mixStart()
	return nil
}

func (b *BridgeMix) RemoveDialogSession(dialogID string) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	for i, d := range b.dialogs {
		if d.Id() == dialogID {
			b.dialogs = append(b.dialogs[:i], b.dialogs[i+1:]...)

			// Update stream list
			fmt.Println("Mixing stopped by remove", dialogID)
			if err := b.mixStop(); err != nil {
				return fmt.Errorf("failed to stop current mixing: %w", err)
			}
			b.mixStart()
			break
		}
	}

	return nil
}

// Mix explicitely starts mixing. Make sure you have increased WaitDialogsNum
func (b *BridgeMix) Mix() (err error) {
	return b.mix()
}

func (b *BridgeMix) mixStart() {
	if len(b.dialogs) < 2 {
		return
	}
	if len(b.dialogs) < b.WaitDialogsNum {
		return
	}

	// Start new mix
	b.mixWG.Add(1)

	go func() {
		defer b.mixWG.Done()
		if err := b.mix(); err != nil {
			// b.mu.Lock()
			b.mixErr = err
			//
		}
	}()
	b.mixState.Store(1) //running
}

func (b *BridgeMix) mixStop() error {
	if b.mixState.Load() == 0 {
		return nil
	}
	var allErros error
	b.mixState.Store(2) // stoping
	for _, d := range b.dialogs {
		err := d.Media().StopRTP(2, 0) // Stop writing
		errors.Join(allErros, err)
	}

	// Waits mix termination
	b.mixWG.Wait()
	b.mixState.Store(0)

	// Now enable RTP writing
	for _, d := range b.dialogs {
		err := d.Media().StartRTP(2, 0) // Stop writing
		errors.Join(allErros, err)
	}
	return allErros
}

func (b *BridgeMix) mix() error {
	if len(b.dialogs) < 2 {
		return fmt.Errorf("number of dialogs must be at least 2")
	}

	type PCMStream struct {
		id           uint32
		r            io.Reader
		w            io.Writer
		mediaSession *media.MediaSession
		// read buf
		buf []byte
		n   int
	}
	fmt.Println("Mixing start")

	addDialogStream := func(d DialogSession, stream *PCMStream, firstMediaProps *MediaProps) error {
		m := d.Media()

		p := MediaProps{}
		r, err := m.AudioReader(WithAudioReaderMediaProps(&MediaProps{}))
		if err != nil {
			return err
		}

		if b.originatorCodec.SampleRate != p.Codec.SampleRate && b.originatorCodec.SampleDur != p.Codec.SampleDur {
			return fmt.Errorf("Codec missmatch. Resampling or transcoding is not supported")
		}

		// Attach PCM decoder
		pcmReader := audio.PCMDecoderReader{}
		if err := pcmReader.Init(p.Codec, r); err != nil {
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

		*stream = PCMStream{
			r:            &pcmReader,
			w:            &pcmWriter,
			mediaSession: m.mediaSession,
			id:           m.RTPPacketWriter.SSRC,
			buf:          make([]byte, media.RTPBufSize),
		}

		return nil
	}

	rwStreams := make([]PCMStream, len(b.dialogs))
	firstMediaProps := MediaProps{}

	err := func() error {
		// We need to lock here as b.dialogs can change
		b.mu.Lock()
		defer b.mu.Unlock()

		for i, d := range b.dialogs {
			if err := addDialogStream(d, &rwStreams[i], &firstMediaProps); err != nil {
				return err
			}
		}
		return nil
	}()
	if err != nil {
		return err
	}

	// We need to skip any packets arrived before this time

	readAllStreams := func(total *int) error {
		for i, r := range rwStreams {
			// We expect to have data or we will deadline very quickly
			// Blocking/Sampling will happen on Write Side so in this time received samples should be present
			// Buffering with timestamps checking needs to be added
			// Alternative is to have this in seperate goroutines with locked buffer reads
			r.mediaSession.StopRTP(1, 1*time.Millisecond)

			// Mostly PCM sample size should be same or less our sampling
			// but we should keep same sampling or deal this per writer?
			n, err := r.r.Read(r.buf)
			rwStreams[i].n = n
			if err != nil {
				if errors.Is(err, os.ErrDeadlineExceeded) {
					fmt.Println("Skipping stream read", r.id)
					continue
				}
				return err
			}
			*total = max(*total, n)
			fmt.Println("Reading stream", "ssrc", r.id, "n", n)
		}
		return nil
	}

	mixStreams := func(mixedBuf []byte) (int, error) {
		// How to now not block on READ if stream does not have any data
		maxN := 0
		// zero mixed buf
		for i := 0; i < len(mixedBuf); i += 2 {
			// mixedBuf[i] = 0
			binary.LittleEndian.PutUint16(mixedBuf[i:], uint16(0))
		}

		for _, r := range rwStreams {
			n := r.n
			readBuf := r.buf[:n]

			if n == 0 {
				// Skip any mixing if nothing has read
				continue
			}

			audio.PCMMix(mixedBuf, mixedBuf, readBuf)
			maxN = max(maxN, n)
		}
		return maxN, nil

	}

	unmixStream := func(r *PCMStream, mixedBuf []byte) []byte {
		n := len(mixedBuf)
		if len(r.buf) < len(mixedBuf) {
			panic("stream buf is shorter than mixed buf")
		}

		readBuf := r.buf[:n]
		audio.PCMUnmix(readBuf, mixedBuf, readBuf)
		// NOTE: This can be higher than actual read bytes
		return readBuf
	}

	mixBuf := make([]byte, media.RTPBufSize)
	// Currently we consider that sample clock is done by Audio Writers
	/*

				Fastest starts writing but rest will delay Reading
				----x----y---z-----|x----y----z|---x------
				    ^      20ms     ^              ^


				Slowest determines ticking and adds initial jitter for X and Y  but after it will have no impact except IO delay
				---x---y----z-------tx---ty--|z-x-y|-----tx---ty----|z
		           		    ^      20ms       ^              ^

	*/
	for {
		// First read all streams at this point of time and fill buffer
		total := 0
		if err := readAllStreams(&total); err != nil {
			return err
		}

		if total == 0 {
			fmt.Println("Nothing read")
			continue
		}

		// Mix all streams
		n, err := mixStreams(mixBuf)
		if err != nil {
			return err
		}

		// broadcast to all
		for _, w := range rwStreams {
			streamBuf := mixBuf[:n]
			if w.n > 0 {
				streamBuf = unmixStream(&w, mixBuf[:n])
			}
			writeStart := time.Now()

			// Mark as stream read
			w.n = 0
			if _, err := w.w.Write(streamBuf); err != nil {
				// Detect is this Deadline or EOF error caused by stream exiting
				fmt.Println("Writing stopped", err, "id", w.id, errors.Is(err, os.ErrDeadlineExceeded))
				if errors.Is(err, os.ErrDeadlineExceeded) {
					state := b.mixState.Load()
					if state != 1 {
						// We are stopped
						return err
					}

					// Mixing has been stopped or some stream
					continue

				}
				return err
			}
			fmt.Println("Writing finihed", w.id, time.Since(writeStart))
		}
	}
	// We need buffering up to 80ms ?
	// We need to read and decode frames from all streams
	// If frame is missing or it is not aligned with timestamp it will not effect mix

}

// Wait waits that mixing is stopped
func (b *BridgeMix) Wait() error {
	b.mixWG.Wait()
	return b.mixErr
}
