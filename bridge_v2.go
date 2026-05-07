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

type BridgeV2 struct {
	// Originator is dialog session that created bridge
	Originator *DialogMedia
	// DTMFpass is also dtmf pipeline and proxy. By default only audio media is proxied
	// NOTE: this may not work if you are already processing DTMF with AudioReaderDTMF
	DTMFpass bool

	log *slog.Logger
	// TODO: RTPpass. RTP pass means that RTP will be proxied.
	// This gives high performance but you can not attach any pipeline in media processing
	// RTPpass bool

	dialogs []*DialogMedia
	// minDialogs is just helper flag when to start proxy
	WaitDialogsNum int
}

// NewBridge creates bridge with default settings.
func NewBridgeV2() BridgeV2 {
	b := BridgeV2{}
	b.Init(media.DefaultLogger())
	return b
}

func (b *BridgeV2) Init(log *slog.Logger) {
	b.log = log
	if b.log == nil {
		b.log = media.DefaultLogger()
	}

	if b.WaitDialogsNum == 0 {
		b.WaitDialogsNum = 2
	}
}

func (b *BridgeV2) GetDialogs() []*DialogMedia {
	return b.dialogs
}

func (b *BridgeV2) AddDialogMedia(m *DialogMedia) error {
	// Check can this dialog be added to bridge. NO TRANSCODING
	if b.Originator != nil {
		// This may look ugly but it is safe way of reading
		origM := b.Originator.Media()
		origProps := MediaProps{}
		_ = origM.audioWriterProps(&origProps)

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

	b.dialogs = append(b.dialogs, m)
	if len(b.dialogs) == 1 {
		b.Originator = m
	}

	if len(b.dialogs) < b.WaitDialogsNum {
		return nil
	}

	if len(b.dialogs) > 2 {
		return fmt.Errorf("currently bridge only support 2 party")
	}
	// Check are both answered
	for _, m := range b.dialogs {
		// TODO remove this double locking. Read once
		if m.RTPPacketReader == nil || m.RTPPacketWriter == nil {
			return fmt.Errorf("dialog session not answered?")
		}
	}

	go func() {
		defer func(start time.Time) {
			b.log.Debug("Proxy media setup", "dur", time.Since(start).String())
		}(time.Now())
		m1 := b.dialogs[0]
		m2 := b.dialogs[1]
		if err := b.proxyMediaChannels(m1, m2); err != nil {
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
func (b *BridgeV2) ProxyMedia() error {
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
func (b *BridgeV2) ProxyMediaControl() (func() error, error) {
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
func (b *BridgeV2) proxyMedia() error {
	m1 := b.dialogs[0].Media()
	m2 := b.dialogs[1].Media()
	return b.proxyMediaChannels(m1, m2)
}

func (b *BridgeV2) proxyMediaChannels(m1, m2 *DialogMedia) error {
	var err error
	log := b.log

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

func (b *BridgeV2) proxyMediaWithDTMF(m1 *DialogMedia, m2 *DialogMedia) error {
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
