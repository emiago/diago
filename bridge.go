// SPDX-License-Identifier: MPL-2.0
// Copyright (C) 2024 Emir Aganovic

package diago

import (
	"errors"
	"fmt"
	"io"
	"time"

	"github.com/emiago/diago/media"
	"github.com/pion/rtp"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

type Bridger interface {
	AddDialogSession(d DialogSession) error
}

type Bridge struct {
	// Originator is dialog session that created bridge
	Originator DialogSession
	dialogs    []DialogSession

	log zerolog.Logger
	// minDialogs is just helper flag when to start proxy
	waitDialogsNum int
}

func NewBridge() Bridge {
	return Bridge{
		log:            log.Logger,
		waitDialogsNum: 2, // For now only p2p bridge
	}
}

func (b *Bridge) GetDialogs() []DialogSession {
	return b.dialogs
}

func (b *Bridge) AddDialogSession(d DialogSession) error {
	// Check can this dialog be added to bridge. NO TRANSCODING
	if b.Originator != nil {
		// This may look ugly but it is safe way of reading
		m := d.Media()
		mprops := MediaProps{}
		_ = m.AudioWriterWithProps(&mprops)

		origM := b.Originator.Media()
		origProps := MediaProps{}
		_ = origM.AudioWriterWithProps(&mprops)
		err := func() error {
			if origProps.Codec != mprops.Codec {
				return fmt.Errorf("codec mismatch in bridge")
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
		if d.Media().AudioReader() == nil || d.Media().AudioWriter() == nil {
			return fmt.Errorf("dialog session not answered %q", d.Id())
		}
	}

	go func() {
		if err := b.proxyMedia(); err != nil {
			if errors.Is(err, io.EOF) {
				return
			}
			b.log.Error().Err(err).Msg("Proxy media stopped")
		}
	}()
	return nil
}

func (b *Bridge) proxyMedia() error {
	var err error
	log := b.log

	log.Info().Msg("Starting proxy media")
	defer func(start time.Time) {
		log.Info().Dur("dur", time.Since(start)).Msg("Proxy media setup")
	}(time.Now())

	m1 := b.dialogs[0].Media()
	m2 := b.dialogs[1].Media()

	// Lets for now simplify proxy and later optimize
	errCh := make(chan error, 2)

	func() {
		r := m1.AudioReader()
		w := m2.AudioWriter()
		buf := rtpBufPool.Get()
		defer rtpBufPool.Put(buf)

		go proxyMediaBackground(log, r, w, buf.([]byte), errCh)
	}()

	// Second
	func() {
		r := m2.AudioReader()
		w := m1.AudioWriter()
		buf := rtpBufPool.Get()
		defer rtpBufPool.Put(buf)

		go proxyMediaBackground(log, r, w, buf.([]byte), errCh)
	}()

	// Wait for all to finish
	for i := 0; i < len(b.dialogs); i++ {
		err = errors.Join(err, <-errCh)
	}
	return err
	// For webrtc we have no session for our packet readers
	// TODO find better distiction
	// if dlg1.Media().RTPPacketReader.Sess == nil || dlg2.Media().RTPPacketReader.Sess == nil {
	// 	b.log.Info().Msg("Starting proxy media no session")
	// 	r1 := dlg1.Media().RTPPacketReader.Reader().(media.RTPReaderRaw)
	// 	r2 := dlg2.Media().RTPPacketReader.Reader().(media.RTPReaderRaw)
	// 	w1 := dlg1.Media().RTPPacketWriter.Writer().(media.RTPWriterRaw)
	// 	w2 := dlg2.Media().RTPPacketWriter.Writer().(media.RTPWriterRaw)

	// 	go b.proxyMediaRTPRaw(r1, w2)
	// 	go b.proxyMediaRTPRaw(r2, w1)

	// 	return nil
	// }
}

func proxyMediaBackground(log zerolog.Logger, reader io.Reader, writer io.Writer, buf []byte, ch chan error) {
	written, err := copyWithBuf(reader, writer, buf)
	log.Debug().Int64("bytes", written).Msg("Bridge proxy stream finished")
	ch <- err
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

	// for {
	// 	// In case of recording we need to unmarshal RTP packet
	// 	pkt, err := m1.ReadRTP()
	// 	if err != nil {
	// 		return err
	// 	}

	// 	if err := m2.WriteRTP(&pkt); err != nil {
	// 		return err
	// 	}
	// }

}

func (b *Bridge) proxyMediaRTP(m1 media.RTPReader, m2 media.RTPWriter) (written int64, e error) {
	buf := make([]byte, 1500) // MTU

	var total int64
	for {
		p := rtp.Packet{}
		// In case of recording we need to unmarshal RTP packet
		err := m1.ReadRTP(buf, &p)
		if err != nil {
			return total, err
		}
		err = m2.WriteRTP(&p)
		if err != nil {
			return total, err
		}
		total += int64(len(p.Payload))
		total += 12
	}

	// for {
	// 	// In case of recording we need to unmarshal RTP packet
	// 	pkt, err := m1.ReadRTP()
	// 	if err != nil {
	// 		return err
	// 	}

	// 	if err := m2.WriteRTP(&pkt); err != nil {
	// 		return err
	// 	}
	// }

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

	// for {
	// 	// In case of recording we need to unmarshal RTP packet
	// 	pkt, err := m1.ReadRTP()
	// 	if err != nil {
	// 		return err
	// 	}

	// 	if err := m2.WriteRTP(&pkt); err != nil {
	// 		return err
	// 	}
	// }

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
