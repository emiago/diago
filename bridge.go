package diago

import (
	"fmt"
	"io"

	"github.com/emiago/sipgox"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

type Bridge struct {
	// Originator is dialog session that created bridge
	Originator DialogSession
	dialogs    []DialogSession

	log zerolog.Logger
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

	m1 := dlg1.MediaSession()
	m2 := dlg2.MediaSession()

	if m1 == nil || m2 == nil {
		return fmt.Errorf("no media setup")
	}

	// For now just as proxy media
	go b.proxyMedia(m1, m2)
	go b.proxyMedia(m2, m1)

	return nil
}

func (b *Bridge) proxyMedia(m1 *sipgox.MediaSession, m2 *sipgox.MediaSession) {
	if err := b.proxyMediaRTP(m1, m2); err != nil {
		b.log.Error().Err(err).Msg("Proxy media stopped")
	}
}

func (b *Bridge) proxyMediaRTP(m1 *sipgox.MediaSession, m2 *sipgox.MediaSession) error {
	buf := make([]byte, 1500) // MTU

	for {
		// In case of recording we need to unmarshal RTP packet
		n, err := m1.ReadRTPRaw(buf)
		if err != nil {
			return err
		}
		written, err := m2.WriteRTPRaw(buf[:n])
		if err != nil {
			return err
		}
		if written != n {
			return io.ErrShortWrite
		}
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
