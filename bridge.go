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

	kind int
	log  zerolog.Logger
}

func NewBridge() Bridge {
	return Bridge{
		log: log.Logger,
	}
}

func (b *Bridge) AddDialogSession(d DialogSession) error {
	b.dialogs = append(b.dialogs, d)
	if len(b.dialogs) == 1 {
		b.Originator = d
	}

	// How now conferencing should be done

	// For every new RTP packet, it must be broadcasted to other

	// For now bridge must not have transcoding

	// For now only p2p bridge
	if len(b.dialogs) != 2 {
		return nil
	}

	b.log.Info().Msg("Starting proxy media")

	dlg1 := b.dialogs[0]
	dlg2 := b.dialogs[1]

	// Check are both answered
	for _, d := range b.dialogs {
		if d.Media().RTPPacketReader == nil || d.Media().RTPPacketWriter == nil {
			return fmt.Errorf("dialog session not answered %q", d.Id())
		}
	}
	// For webrtc we have no session for our packet readers
	// TODO find better distiction
	if dlg1.Media().RTPPacketReader.Sess == nil {

		r1 := dlg1.Media().RTPPacketReader.Reader.(media.RTPReaderRaw)
		r2 := dlg2.Media().RTPPacketReader.Reader.(media.RTPReaderRaw)
		w1 := dlg1.Media().RTPPacketWriter.Writer.(media.RTPWriterRaw)
		w2 := dlg2.Media().RTPPacketWriter.Writer.(media.RTPWriterRaw)

		go b.proxyMediaRTPRaw(r1, w2)
		go b.proxyMediaRTPRaw(r2, w1)

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
	go b.proxyMedia(m1, m2)
	go b.proxyMedia(m2, m1)
	return nil
}

// func (b *Bridge) RemoveDialogSession(d DialogSession) error {

// }

func (b *Bridge) proxyMedia(m1 *media.MediaSession, m2 *media.MediaSession) {
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

func (b *Bridge) proxyMediaRTP(m1 *media.MediaSession, m2 *media.MediaSession) (written int64, e error) {
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
