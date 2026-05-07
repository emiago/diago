package diago

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"sync"
	"time"

	"github.com/emiago/diago/media"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v3"
)

type webrtcSession struct {
	Laddr string
	Raddr string
	Codec media.Codec

	writer *WebrtcTrackRTPWriter
	reader *WebrtcTrackRTPReader
}

func (s *webrtcSession) StopRTP(rw int8, dur time.Duration) error {
	t := time.Now().Add(dur)
	if rw&1 > 0 {
		return s.reader.receiver.SetReadDeadline(t)
	}
	if rw&2 > 0 {
		// if dur == 0 {
		// 	return s.writer.sender.Stop()
		// }
		return fmt.Errorf("no support for duration based RTP write stop")
	}

	e1 := s.reader.receiver.SetReadDeadline(t)
	// e2 := s.writer.sender.Stop()
	return e1
}
func (s *webrtcSession) StartRTP(rw int8) error {
	if rw&1 > 0 {
		return s.reader.receiver.SetReadDeadline(time.Time{})
	}
	if rw&2 > 0 {
		return fmt.Errorf("no support to restart writer")
	}
	return s.reader.receiver.SetReadDeadline(time.Time{})
}

type DialogWebrtc struct {
	mu      sync.Mutex
	onClose func() error
	log     *slog.Logger
	// peerConnection *webrtc.PeerConnection
	mediaSession *webrtcSession

	RTPPacketWriter *media.RTPPacketWriter
	RTPPacketReader *media.RTPPacketReader

	// webrtc stuff to access
	peerConnection *webrtc.PeerConnection
}

func (d *DialogWebrtc) OnClose(f func() error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.onCloseUnsafe(f)
}

func (d *DialogWebrtc) onCloseUnsafe(f func() error) {
	if d.onClose != nil {
		prev := d.onClose
		d.onClose = func() error {
			return errors.Join(prev(), f())
		}
		return
	}
	d.onClose = f
}

func (d *DialogWebrtc) Close() error {
	d.mu.Lock()
	onClose := d.onClose
	d.mu.Unlock()
	return onClose()
}

type AudioReaderWebrtcOption func(d *DialogWebrtc) error

func WithAudioReaderWebrtcProps(p *MediaProps) AudioReaderWebrtcOption {
	return func(d *DialogWebrtc) error {
		p.Codec = d.mediaSession.Codec
		p.Laddr = d.mediaSession.Laddr
		p.Raddr = d.mediaSession.Raddr
		return nil
	}
}

func (d *DialogWebrtc) AudioReader(...AudioReaderWebrtcOption) (io.Reader, error) {
	return d.RTPPacketReader, nil
}

type AudioWriterWebrtcOption func(d *DialogWebrtc) error

func WithAudioWriterWebrtcProps(p *MediaProps) AudioWriterWebrtcOption {
	return func(d *DialogWebrtc) error {
		p.Codec = d.mediaSession.Codec
		p.Laddr = d.mediaSession.Laddr
		p.Raddr = d.mediaSession.Raddr
		return nil
	}
}

func (d *DialogWebrtc) AudioWriter(...AudioWriterWebrtcOption) (io.Writer, error) {
	return d.RTPPacketWriter, nil
}

// TODO: This would normally be exposed by RTP Session
func (d *DialogWebrtc) WriteRTCP(pkts []rtcp.Packet) error {
	return d.peerConnection.WriteRTCP(pkts)
}

func (m *DialogWebrtc) AudioReaderDTMF() (*DTMFReader, error) {
	ar, err := m.AudioReader()
	if err != nil {
		return nil, err
	}
	return &DTMFReader{
		dtmfReader:  media.NewRTPDTMFReader(media.CodecTelephoneEvent8000, m.RTPPacketReader, ar),
		rtpDeadline: m.mediaSession,
	}, nil
}

func (m *DialogWebrtc) AudioWriterDTMF() (*DTMFWriter, error) {
	aw, err := m.AudioWriter()
	if err != nil {
		return nil, err
	}
	return &DTMFWriter{
		dtmfWriter: media.NewRTPDTMFWriter(media.CodecTelephoneEvent8000, m.RTPPacketWriter, aw),
	}, nil
}

func (m *DialogWebrtc) Echo() error {
	audioR, err := m.AudioReader()
	if err != nil {
		return err
	}

	audioW, err := m.AudioWriter()
	if err != nil {
		return err
	}

	_, err = media.Copy(audioR, audioW)
	if err != nil {
		return err
	}
	return nil
}
