// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2026, Emir Aganovic

package diago

import (
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/emiago/diago/media"
	"github.com/pion/rtcp"
)

const webrtcFailureCleanupTimeout = 5 * time.Second

// DialogWebrtc exposes the direct ICE + DTLS-SRTP media stack negotiated
// through SIP. It is independent from DialogWebrtcPion, which uses a Pion
// PeerConnection and remains available for browser-oriented calls.
type DialogWebrtc struct {
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

func (d *DialogWebrtc) init(sess *media.MediaSessionWebrtc, rtpSess *media.RTPSessionWebrtc) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.mediaSession = sess
	d.rtpSession = rtpSess
	codec := sess.Codec()
	d.RTPPacketReader = media.NewRTPPacketReader(rtpSess, codec)
	d.RTPPacketWriter = media.NewRTPPacketWriter(rtpSess, codec)
}

// MediaSession returns the direct WebRTC transport session.
func (d *DialogWebrtc) MediaSession() *media.MediaSessionWebrtc {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.mediaSession
}

// RTPSession returns the RTP/RTCP statistics session.
func (d *DialogWebrtc) RTPSession() *media.RTPSessionWebrtc {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.rtpSession
}

// OnReadRTCP sets a callback for received RTCP packets. Passing nil disables it.
func (d *DialogWebrtc) OnReadRTCP(f func(rtcp.Packet, media.RTPReadStats)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.rtpSession != nil {
		d.rtpSession.OnReadRTCP(f)
	}
}

// OnWriteRTCP sets a callback for sent RTCP packets. Passing nil disables it.
func (d *DialogWebrtc) OnWriteRTCP(f func(rtcp.Packet, media.RTPWriteStats)) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.rtpSession != nil {
		d.rtpSession.OnWriteRTCP(f)
	}
}

// OnClose adds a cleanup hook. Hooks run once when Close is first called.
func (d *DialogWebrtc) OnClose(f func() error) {
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
func (d *DialogWebrtc) Close() error {
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

type AudioReaderWebrtcOption func(*DialogWebrtc) error

// WithAudioReaderWebrtcProps returns the negotiated codec and ICE pair.
func WithAudioReaderWebrtcProps(props *MediaProps) AudioReaderWebrtcOption {
	return func(d *DialogWebrtc) error {
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

func WithAudioReaderWebrtcDTMF(reader *DTMFReader) AudioReaderWebrtcOption {
	return func(d *DialogWebrtc) error {
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
func (d *DialogWebrtc) AudioReader(opts ...AudioReaderWebrtcOption) (io.Reader, error) {
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

func (d *DialogWebrtc) audioReaderUnsafe() io.Reader {
	if d.audioReader != nil {
		return d.audioReader
	}
	return d.RTPPacketReader
}

func (d *DialogWebrtc) SetAudioReader(reader io.Reader) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.audioReader = reader
}

type AudioWriterWebrtcOption func(*DialogWebrtc) error

// WithAudioWriterWebrtcProps returns the negotiated codec and ICE pair.
func WithAudioWriterWebrtcProps(props *MediaProps) AudioWriterWebrtcOption {
	return func(d *DialogWebrtc) error {
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

func WithAudioWriterWebrtcDTMF(writer *DTMFWriter) AudioWriterWebrtcOption {
	return func(d *DialogWebrtc) error {
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
func (d *DialogWebrtc) AudioWriter(opts ...AudioWriterWebrtcOption) (io.Writer, error) {
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

func (d *DialogWebrtc) audioWriterUnsafe() io.Writer {
	if d.audioWriter != nil {
		return d.audioWriter
	}
	return d.RTPPacketWriter
}

func (d *DialogWebrtc) SetAudioWriter(writer io.Writer) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.audioWriter = writer
}

func (d *DialogWebrtc) AudioReaderDTMF() (*DTMFReader, error) {
	reader := &DTMFReader{}
	_, err := d.AudioReader(WithAudioReaderWebrtcDTMF(reader))
	return reader, err
}

func (d *DialogWebrtc) AudioWriterDTMF() (*DTMFWriter, error) {
	writer := &DTMFWriter{}
	_, err := d.AudioWriter(WithAudioWriterWebrtcDTMF(writer))
	return writer, err
}

// Echo copies received encoded audio back to the remote peer.
func (d *DialogWebrtc) Echo() error {
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
func (d *DialogWebrtc) PlaybackCreate() (AudioPlayback, error) {
	props := MediaProps{}
	writer, err := d.AudioWriter(WithAudioWriterWebrtcProps(&props))
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
func (d *DialogWebrtc) AudioStereoRecordingCreate(wavFile *os.File) (AudioStereoRecordingWav, error) {
	readerProps, writerProps := MediaProps{}, MediaProps{}
	reader, err := d.AudioReader(WithAudioReaderWebrtcProps(&readerProps))
	if err != nil {
		return AudioStereoRecordingWav{}, err
	}
	writer, err := d.AudioWriter(WithAudioWriterWebrtcProps(&writerProps))
	if err != nil {
		return AudioStereoRecordingWav{}, err
	}
	return newDialogRecordingWav(wavFile, reader, readerProps, writer, writerProps)
}
