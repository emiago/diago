// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/emiago/diago/media"
)

type BridgeAudioMedia struct {
	Reader      io.Reader
	ReaderProps MediaProps
	Writer      io.Writer
	WriterProps MediaProps
	StopRTP     func() error
	StartRTP    func() error
}

type Bridge struct {
	// originator is dialog session that created bridge
	originator *BridgeAudioMedia
	// DTMFpass is also dtmf pipeline and proxy. By default only audio media is proxied
	// NOTE: this may not work if you are already processing DTMF with AudioReaderDTMF
	DTMFpass bool

	log *slog.Logger
	// TODO: RTPpass. RTP pass means that RTP will be proxied.
	// This gives high performance but you can not attach any pipeline in media processing
	// RTPpass bool

	dialogs []*BridgeAudioMedia
	// minDialogs is just helper flag when to start proxy
	WaitDialogsNum int

	wg sync.WaitGroup
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

func (b *Bridge) GetDialogs() []*BridgeAudioMedia {
	return b.dialogs
}

func (b *Bridge) AddDialogMedia(m *DialogMedia) error {
	med := BridgeAudioMedia{}
	var err error
	med.Reader, err = m.AudioReader(WithAudioReaderMediaProps(&med.ReaderProps))
	if err != nil {
		return err
	}

	med.Writer, err = m.AudioWriter(WithAudioWriterMediaProps(&med.WriterProps))
	if err != nil {
		return err
	}

	if b.DTMFpass {
		dtmfReader := DTMFReader{}
		med.Reader, err = m.AudioReader(WithAudioReaderMediaProps(&med.ReaderProps), WithAudioReaderDTMF(&dtmfReader))
		if err != nil {
			return err
		}
		dtmfWriter := DTMFWriter{}
		med.Writer, err = m.AudioWriter(WithAudioWriterMediaProps(&med.WriterProps), WithAudioWriterDTMF(&dtmfWriter))
		if err != nil {
			return err
		}

		dtmfReader.OnDTMF(func(dtmf rune) error {
			return dtmfWriter.WriteDTMF(dtmf)
		})
	}
	med.StopRTP = func() error {
		return m.StopRTP(0, 0)
	}

	med.StartRTP = func() error {
		return m.StartRTP(0, 0)
	}

	return b.AddAudioMedia(&med)
}

func (b *Bridge) AddMedia(m any) error {
	switch t := m.(type) {
	case *DialogMedia:
		return b.AddDialogMedia(t)
	case *DialogWebrtc:
		return b.AddDialogWebrtc(t)
	default:
		return fmt.Errorf("unsupporte media stack for bridge. Use AddAudioMedia")
	}
}

func (b *Bridge) AddDialogWebrtc(m *DialogWebrtc) error {
	med := BridgeAudioMedia{}
	var err error
	med.Reader, err = m.AudioReader(WithAudioReaderWebrtcProps(&med.ReaderProps))
	if err != nil {
		return err
	}
	med.Writer, err = m.AudioWriter(WithAudioWriterWebrtcProps(&med.WriterProps))
	if err != nil {
		return err
	}

	if b.DTMFpass {
		dtmfReader := DTMFReader{}
		med.Reader, err = m.AudioReader(WithAudioReaderWebrtcProps(&med.ReaderProps), WithAudioReaderWebrtcDTMF(&dtmfReader))
		if err != nil {
			return err
		}
		dtmfWriter := DTMFWriter{}
		med.Writer, err = m.AudioWriter(WithAudioWriterWebrtcProps(&med.WriterProps), WithAudioWriterWebrtcDTMF(&dtmfWriter))
		if err != nil {
			return err
		}

		dtmfReader.OnDTMF(func(dtmf rune) error {
			return dtmfWriter.WriteDTMF(dtmf)
		})
	}

	med.StopRTP = func() error {
		return m.mediaSession.StopRTP(0, 0)
	}

	med.StartRTP = func() error {
		return m.mediaSession.StartRTP(0)
	}

	return b.AddAudioMedia(&med)
}

func (b *Bridge) AddAudioMedia(m *BridgeAudioMedia) error {
	// Check can this dialog be added to bridge. NO TRANSCODING
	if b.originator != nil {
		// This may look ugly but it is safe way of reading
		origProps := b.originator.ReaderProps
		mprops := m.ReaderProps

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
		b.originator = m
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
		if m.Reader == nil || m.Writer == nil {
			return fmt.Errorf("dialog session not answered?")
		}
	}

	go func() {
		defer func(start time.Time) {
			b.log.Debug("Proxy media setup", "dur", time.Since(start).String())
		}(time.Now())
		m1 := b.dialogs[0]
		m2 := b.dialogs[1]
		if err := b.proxyMediaChannels(context.Background(), m1, m2); err != nil {
			if errors.Is(err, io.EOF) {
				return
			}

			b.log.Error("Proxy media stopped", "error", err)
		}
	}()
	return nil
}

// ProxyMedia is explicit call to start proxing media.
func (b *Bridge) ProxyMedia(ctx context.Context) error {
	if len(b.dialogs) > 2 {
		return fmt.Errorf("proxy media requieres minimum 2 parties")
	}
	// Check are both answered
	for _, m := range b.dialogs {
		// TODO remove this double locking. Read once
		if m.Reader == nil || m.Writer == nil {
			return fmt.Errorf("dialog session not answered?")
		}
	}

	defer func(start time.Time) {
		b.log.Debug("Proxy media setup", "dur", time.Since(start).String())
	}(time.Now())
	m1 := b.dialogs[0]
	m2 := b.dialogs[1]
	if err := b.proxyMediaChannels(ctx, m1, m2); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}

		return err
	}
	return nil
}

func (b *Bridge) proxyMediaChannels(ctx context.Context, m1, m2 *BridgeAudioMedia) error {
	var err error
	log := b.log

	// Lets for now simplify proxy and later optimize
	errCh := make(chan error, 2)
	func() {
		p1, p2 := m1.ReaderProps, m2.WriterProps
		r := m1.Reader
		w := m2.Writer

		log := log.With("from", p1.Raddr+" > "+p1.Laddr, "to", p2.Laddr+" > "+p2.Raddr)
		log.Debug("Starting proxy media routine")
		go proxyMediaBackground(log, r, w, errCh)
	}()

	// Second
	func() {
		p1, p2 := m2.ReaderProps, m1.WriterProps
		r := m2.Reader
		w := m1.Writer

		log := log.With("from", p1.Raddr+" > "+p1.Laddr, "to", p2.Laddr+" > "+p2.Raddr)
		log.Debug("Starting proxy media routine")
		go proxyMediaBackground(log, r, w, errCh)
	}()

	// Wait for all to finish or stop on cancelation
	stopped := false
	for i := 0; i < 2; i++ {
		select {
		case <-ctx.Done():
			// If we failed to stop, we will have useless block here
			if !stopped {
				if err := m1.StopRTP(); err != nil {
					return err
				}
				stopped = true
			}

			err = errors.Join(err, <-errCh)

		case e := <-errCh:
			err = errors.Join(err, e)
		}
	}

	if stopped {
		m1.StartRTP() // Make sure we have RTP continued
	}

	return err
}
