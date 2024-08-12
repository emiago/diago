// SPDX-License-Identifier: BSD-2-Clause
// Copyright (C) 2024 Emir Aganovic

package diago

import (
	"errors"
	"fmt"
	"io"
	"net"

	"github.com/emiago/media"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// BridgeDial is special bridge optimized for Dial Case
type BridgeDial struct {
	// Originator is dialog session that created bridge
	originator DialogSession
	// callee     DialogSession

	log zerolog.Logger
}

func NewBridgeConference() BridgeDial {
	return BridgeDial{
		log: log.Logger,
	}
}

func (b *BridgeDial) AddDialogSession(d DialogSession) error {
	if b.originator == nil {
		b.originator = d
		return nil
	}

	b.log.Info().Msg("Starting bridge_dial proxy media")

	dlg1 := b.originator
	dlg2 := d

	if dlg1.Media().MediaSession == nil {
		// This could be webrtc
		r1 := dlg1.Media().RTPPacketReader.Reader.(media.RTPReaderRaw)
		r2 := dlg2.Media().RTPPacketReader.Reader.(media.RTPReaderRaw)
		w1 := dlg1.Media().RTPPacketWriter.Writer.(media.RTPWriterRaw)
		w2 := dlg2.Media().RTPPacketWriter.Writer.(media.RTPWriterRaw)

		go proxyMediaRTPRaw(r1, w2)
		go proxyMediaRTPRaw(r2, w1)

		// This is questionable.
		// Normally we should not stream RTCP. Instead this should be us monitoring both sides
		// go proxyMediaRTCPRaw()

		return nil
	}

	m1 := dlg1.Media().MediaSession
	m2 := dlg2.Media().MediaSession

	if m1 == nil || m2 == nil {
		return fmt.Errorf("no media setup")
	}

	// For now just as proxy media
	go proxyMedia(b.log, m1, m2)
	go proxyMedia(b.log, m2, m1)
	return nil
}

// func (b *Bridge) RemoveDialogSession(d DialogSession) error {

// }

// func (b *Bridge) proxyMedia(m1 *media.MediaSession, m2 *media.MediaSession) {
// 	go func() {
// 		total, err := b.proxyMediaRTCP(m1, m2)
// 		if err != nil && !errors.Is(err, net.ErrClosed) {
// 			b.log.Error().Err(err).Msg("Proxy media RTCP stopped")
// 		}
// 		b.log.Debug().Int64("bytes", total).Str("peer1", m1.Raddr.String()).Str("peer2", m2.Raddr.String()).Msg("RTCP finished")
// 	}()

// 	total, err := b.proxyMediaRTP(m1, m2)
// 	if err != nil && !errors.Is(err, net.ErrClosed) {
// 		b.log.Error().Err(err).Msg("Proxy media stopped")
// 	}
// 	b.log.Debug().Int64("bytes", total).Str("peer1", m1.Raddr.String()).Str("peer2", m2.Raddr.String()).Msg("RTP finished")
// }

func proxyMediaRTPRaw(m1 media.RTPReaderRaw, m2 media.RTPWriterRaw) (written int64, e error) {
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

func proxyMedia(log zerolog.Logger, m1 *media.MediaSession, m2 *media.MediaSession) {
	go func() {
		total, err := proxyMediaRTCP(m1, m2)
		if err != nil && !errors.Is(err, net.ErrClosed) {
			log.Error().Err(err).Msg("Proxy media RTCP stopped")
		}
		log.Debug().Int64("bytes", total).Str("peer1", m1.Raddr.String()).Str("peer2", m2.Raddr.String()).Msg("RTCP finished")
	}()

	total, err := proxyMediaRTPRaw(m1, m2)
	if err != nil && !errors.Is(err, net.ErrClosed) {
		log.Error().Err(err).Msg("Proxy media stopped")
	}
	log.Debug().Int64("bytes", total).Str("peer1", m1.Raddr.String()).Str("peer2", m2.Raddr.String()).Msg("RTP finished")
}

func proxyMediaRTCP(m1 *media.MediaSession, m2 *media.MediaSession) (written int64, e error) {
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
