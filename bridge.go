// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"time"

	"github.com/emiago/diago/media"
)

type Bridger interface {
	AddDialogSession(d DialogSession) error
}

type Bridge struct {
	// Originator is dialog session that created bridge
	Originator DialogSession
	// DTMFpass is also dtmf pipeline and proxy. By default only audio media is proxied
	DTMFpass bool

	log *slog.Logger
	// TODO: RTPpass. RTP pass means that RTP will be proxied.
	// This gives high performance but you can not attach any pipeline in media processing
	// RTPpass bool

	dialogs []DialogSession

	// minDialogs is just helper flag when to start proxy
	waitDialogsNum int
}

func NewBridge() Bridge {
	b := Bridge{}
	b.Init(slog.Default())
	return b
}

func (b *Bridge) Init(log *slog.Logger) {
	b.log = log
	b.waitDialogsNum = 2
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

	if len(b.dialogs) < b.waitDialogsNum {
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
			b.log.Info("Proxy media setup", "dur", time.Since(start).String())
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

		log.Debug("Starting proxy media routine", "from", p1.Raddr+" > "+p1.Laddr, "to", p2.Laddr+" > "+p2.Raddr)
		go proxyMediaBackground(log, r, w, errCh)
	}()

	// Second
	func() {
		p1, p2 := MediaProps{}, MediaProps{}
		r := m2.audioReaderProps(&p1)
		w := m1.audioWriterProps(&p2)
		log.Debug("Starting proxy media routine", "from", p1.Raddr+" > "+p1.Laddr, "to", p2.Laddr+" > "+p2.Raddr)
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
	log.Debug("Bridge proxy stream finished", "bytes", written)
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

	log := b.log
	log.Debug("Starting proxy media routine", "from", p1.Raddr+" > "+p1.Laddr, "to", p2.Laddr+" > "+p2.Raddr)
	written, err := copyWithBuf(r, w, buf.([]byte))
	log.Debug("Bridge proxy stream finished", "bytes", written)
	return err
}

func (b *Bridge) proxyMediaRTPRaw(m1 media.RTPReaderRaw, m2 media.RTPWriterRaw) (written int64, e error) {
	buf := make([]byte, 1500) // MTU

	var total int64
	for {
		// In case of recording we need to unmarshal RTP packet
		n, err := m1.ReadRTPRaw(buf)
		if err != nil {
			return total, err
		}
		written, err := m2.WriteRTPRaw(buf[:n])
		if err != nil {
			return total, err
		}
		if written != n {
			return total, io.ErrShortWrite
		}
		total += int64(written)
	}
}

func (b *Bridge) proxyMediaRTCP(m1 *media.MediaSession, m2 *media.MediaSession) (written int64, e error) {
	buf := make([]byte, 1500) // MTU

	var total int64
	for {
		// In case of recording we need to unmarshal RTP packet
		n, err := m1.ReadRTCPRaw(buf)
		if err != nil {
			return total, err
		}
		written, err := m2.WriteRTCPRaw(buf[:n])
		if err != nil {
			return total, err
		}
		if written != n {
			return total, io.ErrShortWrite
		}
		total += int64(written)
	}
}

func (b *Bridge) proxyMediaRTCPRaw(m1 media.RTPCReaderRaw, m2 media.RTCPWriterRaw) (written int64, e error) {
	buf := make([]byte, 1500) // MTU

	var total int64
	for {
		// In case of recording we need to unmarshal RTP packet
		n, err := m1.ReadRTCPRaw(buf)
		if err != nil {
			return total, err
		}
		written, err := m2.WriteRTCPRaw(buf[:n])
		if err != nil {
			return total, err
		}
		if written != n {
			return total, io.ErrShortWrite
		}
		total += int64(written)
	}
}
