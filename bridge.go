// SPDX-License-Identifier: MPL-2.0
// Copyright (C) 2024 Emir Aganovic

package diago

import (
	"errors"
	"fmt"
	"io"
	"net"
	"time"

	"github.com/emiago/diago/media"
	"github.com/pion/rtp"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

type Bridger interface {
	AddDialogSession(d DialogSession) error
}

const (
	bridgeKindProxy     = 1
	bridgeKindRecording = 2
)

type Bridge struct {
	// Originator is dialog session that created bridge
	Originator DialogSession
	dialogs    []DialogSession

	log zerolog.Logger
	// minDialogs is just helper flag when to start proxy
	minDialogsNumber int
}

func NewBridge() Bridge {
	return Bridge{
		log:              log.Logger,
		minDialogsNumber: 2, // For now only p2p bridge
	}
}

func (b *Bridge) GetDialogs() []DialogSession {
	return b.dialogs
}

func (b *Bridge) AddDialogSession(d DialogSession) error {
	b.dialogs = append(b.dialogs, d)
	if len(b.dialogs) == 1 {
		b.Originator = d
	}

	if len(b.dialogs) < b.minDialogsNumber {
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
	b.log.Info().Msg("Starting proxy media")
	defer func(start time.Time) {
		b.log.Info().Dur("dur", time.Since(start)).Msg("Proxy media setup")
	}(time.Now())

	dlg1 := b.dialogs[0]
	dlg2 := b.dialogs[1]

	// Lets for now simplify proxy and later optimize
	errCh := make(chan error, 2)
	// TODO:
	// For now bridge must not have transcoding
	{
		r := dlg1.Media().AudioReader()
		w := dlg2.Media().AudioWriter()
		buf := playBufPool.Get()
		defer playBufPool.Put(buf)

		go proxyMediaBackground(b.log, r, w, buf.([]byte), errCh)
	}

	// Second
	{
		r := dlg2.Media().AudioReader()
		w := dlg1.Media().AudioWriter()
		buf := playBufPool.Get()
		defer playBufPool.Put(buf)

		go proxyMediaBackground(b.log, r, w, buf.([]byte), errCh)
	}

	var err error
	// Wait for all to finish
	for i := 0; i < len(b.dialogs); i++ {
		err = errors.Join(err, <-errCh)
	}
	return err
	// For webrtc we have no session for our packet readers
	// TODO find better distiction
	if dlg1.Media().RTPPacketReader.Sess == nil || dlg2.Media().RTPPacketReader.Sess == nil {
		b.log.Info().Msg("Starting proxy media no session")
		r1 := dlg1.Media().RTPPacketReader.Reader().(media.RTPReaderRaw)
		r2 := dlg2.Media().RTPPacketReader.Reader().(media.RTPReaderRaw)
		w1 := dlg1.Media().RTPPacketWriter.Writer().(media.RTPWriterRaw)
		w2 := dlg2.Media().RTPPacketWriter.Writer().(media.RTPWriterRaw)

		go b.proxyMediaRTPRaw(r1, w2)
		go b.proxyMediaRTPRaw(r2, w1)

		// r1 := dlg1.Media().RTPPacketReader
		// r2 := dlg2.Media().RTPPacketReader
		// w1 := dlg1.Media().RTPPacketWriter
		// w2 := dlg2.Media().RTPPacketWriter

		// go b.proxyMediaIO(r1, w2)
		// go b.proxyMediaIO(r2, w1)
		// return nil

		// This is questionable.
		// Normally we should not stream RTCP. Instead this should be us monitoring both sides
		// go proxyMediaRTCPRaw()

		return nil
	}

	m1 := dlg1.Media().mediaSession
	m2 := dlg2.Media().mediaSession

	if m1 == nil || m2 == nil {
		return fmt.Errorf("no media setup")
	}

	// For now just as proxy media
	go b.proxyMediaSessions(m1, m2)
	go b.proxyMediaSessions(m2, m1)
	return nil
}

func (b *Bridge) proxyMediaSessions(m1 *media.MediaSession, m2 *media.MediaSession) {
	go func() {
		total, err := b.proxyMediaRTCP(m1, m2)
		if err != nil && !errors.Is(err, net.ErrClosed) {
			b.log.Error().Err(err).Msg("Proxy media RTCP stopped")
		}
		b.log.Debug().Int64("bytes", total).Str("peer1", m1.Raddr.String()).Str("peer2", m2.Raddr.String()).Msg("RTCP finished")
	}()

	total, err := b.proxyMediaRTP(m1, m2)
	if err != nil && !errors.Is(err, net.ErrClosed) {
		b.log.Error().Err(err).Msg("Proxy media stopped")
	}
	b.log.Debug().Int64("bytes", total).Str("peer1", m1.Raddr.String()).Str("peer2", m2.Raddr.String()).Msg("RTP finished")
}

func proxyMediaBackground(log zerolog.Logger, reader io.Reader, writer io.Writer, buf []byte, ch chan error) {
	written, err := copyWithBuf(reader, writer, buf)
	log.Debug().Int64("bytes", written).Msg("Bridge proxy stream finished")
	ch <- err
}

// func (b *Bridge) proxyMediaRTP(m1 *media.MediaSession, m2 *media.MediaSession) (written int64, e error) {
// 	buf := make([]byte, 1500) // MTU

// 	var total int64
// 	for {
// 		// In case of recording we need to unmarshal RTP packet
// 		n, err := m1.ReadRTPRaw(buf)
// 		if err != nil {
// 			return total, err
// 		}
// 		written, err := m2.WriteRTPRaw(buf[:n])
// 		if err != nil {
// 			return total, err
// 		}
// 		if written != n {
// 			return total, io.ErrShortWrite
// 		}
// 		total += int64(written)
// 	}

// 	// for {
// 	// 	// In case of recording we need to unmarshal RTP packet
// 	// 	pkt, err := m1.ReadRTP()
// 	// 	if err != nil {
// 	// 		return err
// 	// 	}

// 	// 	if err := m2.WriteRTP(&pkt); err != nil {
// 	// 		return err
// 	// 	}
// 	// }

// }

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

func (b *Bridge) proxyMediaIO(m1 io.Reader, m2 io.Writer) (written int64, e error) {
	buf := make([]byte, 1500) // MTU

	var total int64
	for {
		// In case of recording we need to unmarshal RTP packet
		n, err := m1.Read(buf)
		if err != nil {
			return total, err
		}
		written, err := m2.Write(buf[:n])
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
