package diago

import (
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
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

	audioReader io.Reader
	audioWriter io.Writer
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
	d.onClose = nil
	d.mu.Unlock()
	if onClose == nil {
		return nil
	}
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

func (d *DialogWebrtc) AudioReader(opts ...AudioReaderWebrtcOption) (io.Reader, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	for _, o := range opts {
		if err := o(d); err != nil {
			return nil, err
		}
	}

	if d.audioReader != nil {
		return d.audioReader, nil
	}

	return d.RTPPacketReader, nil
}

func (d *DialogWebrtc) SetAudioReader(r io.Reader) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.audioReader = r
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

func (d *DialogWebrtc) AudioWriter(opts ...AudioWriterWebrtcOption) (io.Writer, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	for _, o := range opts {
		if err := o(d); err != nil {
			return nil, err
		}
	}

	if d.audioWriter != nil {
		return d.audioWriter, nil
	}

	return d.RTPPacketWriter, nil
}

func (d *DialogWebrtc) SetAudioWriter(w io.Writer) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.audioWriter = w
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

// PlaybackCreate creates playback for audio
func (d *DialogWebrtc) PlaybackCreate() (AudioPlayback, error) {
	mprops := MediaProps{}
	w := d.audioWriterProps(&mprops)
	if w == nil {
		return AudioPlayback{}, fmt.Errorf("no media setup")
	}

	if mprops.Codec.SampleRate == 0 {
		return AudioPlayback{}, fmt.Errorf("no codec defined")
	}
	p := NewAudioPlayback(w, mprops.Codec)
	// On each play it needs reset RTP timestamp
	p.onPlay = d.RTPPacketWriter.ResetTimestamp
	return p, nil
}

func (d *DialogWebrtc) audioWriterProps(p *MediaProps) io.Writer {
	d.mu.Lock()
	defer d.mu.Unlock()

	WithAudioWriterWebrtcProps(p)(d)
	return d.RTPPacketWriter
}

// AudioStereoRecordingCreate creates wav recording.
// MUST call Close for correct storing
func (d *DialogWebrtc) AudioStereoRecordingCreate(wawFile *os.File) (AudioStereoRecordingWav, error) {
	arProps, awProps := MediaProps{}, MediaProps{}
	ar, err := d.AudioReader(WithAudioReaderWebrtcProps(&arProps))
	if err != nil {
		return AudioStereoRecordingWav{}, err
	}

	aw, err := d.AudioWriter(WithAudioWriterWebrtcProps(&awProps))
	if err != nil {
		return AudioStereoRecordingWav{}, err
	}
	return newDialogRecordingWav(wawFile, ar, arProps, aw, awProps)
}
