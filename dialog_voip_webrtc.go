// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2026, Emir Aganovic

package diago

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"

	"github.com/emiago/diago/media"
	"github.com/pion/rtcp"
)

// DialogVoipWebrtc exposes the direct ICE + DTLS-SRTP media stack negotiated
// through SIP. It is independent from DialogWebrtc, which uses a Pion
// PeerConnection and remains available for browser-oriented calls.
type DialogVoipWebrtc struct {
	mu sync.Mutex

	mediaSession *media.MediaSessionWebrtc
	rtpSession   *media.RTPSessionWebrtc

	// RTPPacketReader is the default RTP payload reader. Prefer AudioReader when
	// an application may install media interceptors.
	RTPPacketReader *media.RTPPacketReader
	// RTPPacketWriter is the default RTP packetizer. Prefer AudioWriter when an
	// application may install media interceptors.
	RTPPacketWriter *media.RTPPacketWriter

	audioReader io.Reader
	audioWriter io.Writer
	onClose     func() error
	closed      bool
}

func (d *DialogVoipWebrtc) init(sess *media.MediaSessionWebrtc, rtpSess *media.RTPSessionWebrtc) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.mediaSession = sess
	d.rtpSession = rtpSess
	codec := sess.Codec()
	d.RTPPacketReader = media.NewRTPPacketReader(rtpSess, codec)
	d.RTPPacketWriter = media.NewRTPPacketWriter(rtpSess, codec)
}

// MediaSession returns the direct WebRTC transport session.
func (d *DialogVoipWebrtc) MediaSession() *media.MediaSessionWebrtc {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.mediaSession
}

// RTPSession returns the RTP/RTCP statistics session.
func (d *DialogVoipWebrtc) RTPSession() *media.RTPSessionWebrtc {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.rtpSession
}

// OnReadRTCP sets a callback for received RTCP packets. Passing nil disables it.
func (d *DialogVoipWebrtc) OnReadRTCP(f func(rtcp.Packet, media.RTPReadStats)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.rtpSession != nil {
		d.rtpSession.OnReadRTCP(f)
	}
}

// OnWriteRTCP sets a callback for sent RTCP packets. Passing nil disables it.
func (d *DialogVoipWebrtc) OnWriteRTCP(f func(rtcp.Packet, media.RTPWriteStats)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.rtpSession != nil {
		d.rtpSession.OnWriteRTCP(f)
	}
}

// OnClose adds a cleanup hook. Hooks run once when Close is first called.
func (d *DialogVoipWebrtc) OnClose(f func() error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if f == nil {
		return
	}
	if d.onClose == nil {
		d.onClose = f
		return
	}
	previous := d.onClose
	d.onClose = func() error { return errors.Join(previous(), f()) }
}

// Close stops RTCP monitoring and closes ICE, DTLS and SRTP resources.
func (d *DialogVoipWebrtc) Close() error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	d.closed = true
	onClose := d.onClose
	d.onClose = nil
	rtpSess := d.rtpSession
	sess := d.mediaSession
	d.mu.Unlock()

	var hookErr, rtpErr, mediaErr error
	if onClose != nil {
		hookErr = onClose()
	}
	if rtpSess != nil {
		rtpErr = rtpSess.MonitorClose()
	}
	if sess != nil {
		mediaErr = sess.Close()
	}
	return errors.Join(hookErr, rtpErr, mediaErr)
}

type AudioReaderVoipWebrtcOption func(*DialogVoipWebrtc) error

// WithAudioReaderVoipWebrtcProps returns the negotiated codec and ICE pair.
func WithAudioReaderVoipWebrtcProps(props *MediaProps) AudioReaderVoipWebrtcOption {
	return func(d *DialogVoipWebrtc) error {
		if props == nil {
			return fmt.Errorf("media props are nil")
		}
		if d.mediaSession == nil {
			return fmt.Errorf("no media setup")
		}
		props.Codec = d.mediaSession.Codec()
		props.Laddr = d.mediaSession.Laddr
		props.Raddr = d.mediaSession.Raddr
		return nil
	}
}

func WithAudioReaderVoipWebrtcDTMF(reader *DTMFReader) AudioReaderVoipWebrtcOption {
	return func(d *DialogVoipWebrtc) error {
		if reader == nil {
			return fmt.Errorf("DTMF reader is nil")
		}
		if d.mediaSession == nil || d.RTPPacketReader == nil {
			return fmt.Errorf("no media setup")
		}
		reader.dtmfReader = media.NewRTPDTMFReader(
			media.CodecTelephoneEvent8000,
			d.RTPPacketReader,
			d.audioReaderUnsafe(),
		)
		reader.rtpDeadline = d.mediaSession
		d.audioReader = reader
		return nil
	}
}

// AudioReader returns an encoded audio payload reader.
func (d *DialogVoipWebrtc) AudioReader(opts ...AudioReaderVoipWebrtcOption) (io.Reader, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.mediaSession == nil || d.RTPPacketReader == nil {
		return nil, fmt.Errorf("no media setup")
	}
	for _, opt := range opts {
		if err := opt(d); err != nil {
			return nil, err
		}
	}
	return d.audioReaderUnsafe(), nil
}

func (d *DialogVoipWebrtc) audioReaderUnsafe() io.Reader {
	if d.audioReader != nil {
		return d.audioReader
	}
	return d.RTPPacketReader
}

func (d *DialogVoipWebrtc) SetAudioReader(reader io.Reader) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.audioReader = reader
}

type AudioWriterVoipWebrtcOption func(*DialogVoipWebrtc) error

// WithAudioWriterVoipWebrtcProps returns the negotiated codec and ICE pair.
func WithAudioWriterVoipWebrtcProps(props *MediaProps) AudioWriterVoipWebrtcOption {
	return func(d *DialogVoipWebrtc) error {
		if props == nil {
			return fmt.Errorf("media props are nil")
		}
		if d.mediaSession == nil {
			return fmt.Errorf("no media setup")
		}
		props.Codec = d.mediaSession.Codec()
		props.Laddr = d.mediaSession.Laddr
		props.Raddr = d.mediaSession.Raddr
		return nil
	}
}

func WithAudioWriterVoipWebrtcDTMF(writer *DTMFWriter) AudioWriterVoipWebrtcOption {
	return func(d *DialogVoipWebrtc) error {
		if writer == nil {
			return fmt.Errorf("DTMF writer is nil")
		}
		if d.RTPPacketWriter == nil {
			return fmt.Errorf("no media setup")
		}
		writer.dtmfWriter = media.NewRTPDTMFWriter(
			media.CodecTelephoneEvent8000,
			d.RTPPacketWriter,
			d.audioWriterUnsafe(),
		)
		d.audioWriter = writer
		return nil
	}
}

// AudioWriter returns an encoded audio payload writer.
func (d *DialogVoipWebrtc) AudioWriter(opts ...AudioWriterVoipWebrtcOption) (io.Writer, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.mediaSession == nil || d.RTPPacketWriter == nil {
		return nil, fmt.Errorf("no media setup")
	}
	for _, opt := range opts {
		if err := opt(d); err != nil {
			return nil, err
		}
	}
	return d.audioWriterUnsafe(), nil
}

func (d *DialogVoipWebrtc) audioWriterUnsafe() io.Writer {
	if d.audioWriter != nil {
		return d.audioWriter
	}
	return d.RTPPacketWriter
}

func (d *DialogVoipWebrtc) SetAudioWriter(writer io.Writer) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.audioWriter = writer
}

func (d *DialogVoipWebrtc) AudioReaderDTMF() (*DTMFReader, error) {
	reader := &DTMFReader{}
	_, err := d.AudioReader(WithAudioReaderVoipWebrtcDTMF(reader))
	return reader, err
}

func (d *DialogVoipWebrtc) AudioWriterDTMF() (*DTMFWriter, error) {
	writer := &DTMFWriter{}
	_, err := d.AudioWriter(WithAudioWriterVoipWebrtcDTMF(writer))
	return writer, err
}

// Echo copies received encoded audio back to the remote peer.
func (d *DialogVoipWebrtc) Echo() error {
	reader, err := d.AudioReader()
	if err != nil {
		return err
	}
	writer, err := d.AudioWriter()
	if err != nil {
		return err
	}
	_, err = media.Copy(reader, writer)
	return err
}

// PlaybackCreate creates playback over the negotiated WebRTC RTP writer.
func (d *DialogVoipWebrtc) PlaybackCreate() (AudioPlayback, error) {
	props := MediaProps{}
	writer, err := d.AudioWriter(WithAudioWriterVoipWebrtcProps(&props))
	if err != nil {
		return AudioPlayback{}, err
	}
	if props.Codec.SampleRate == 0 {
		return AudioPlayback{}, fmt.Errorf("no codec defined")
	}
	playback := NewAudioPlayback(writer, props.Codec)
	playback.onPlay = d.RTPPacketWriter.ResetTimestamp
	return playback, nil
}

// AudioStereoRecordingCreate creates a stereo WAV recording pipeline. The
// caller must use the returned recording's reader and writer and call Close.
func (d *DialogVoipWebrtc) AudioStereoRecordingCreate(wavFile *os.File) (AudioStereoRecordingWav, error) {
	readerProps, writerProps := MediaProps{}, MediaProps{}
	reader, err := d.AudioReader(WithAudioReaderVoipWebrtcProps(&readerProps))
	if err != nil {
		return AudioStereoRecordingWav{}, err
	}
	writer, err := d.AudioWriter(WithAudioWriterVoipWebrtcProps(&writerProps))
	if err != nil {
		return AudioStereoRecordingWav{}, err
	}
	return newDialogRecordingWav(wavFile, reader, readerProps, writer, writerProps)
}
