// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2026, Emir Aganovic

package diago

import (
	"context"
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

	mediaSession        *media.MediaSessionWebrtc
	rtpSession          *media.RTPSessionWebrtc
	pendingMediaSession *media.MediaSessionWebrtc

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

func (d *DialogWebrtc) Init(sess *media.MediaSessionWebrtc, rtpSess *media.RTPSessionWebrtc) {
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
	pendingSess := d.pendingMediaSession
	d.pendingMediaSession = nil
	d.mu.Unlock()

	var hookErr, rtpErr, mediaErr, pendingErr error
	if onClose != nil {
		hookErr = onClose()
	}
	if rtpSess != nil {
		rtpErr = rtpSess.MonitorClose()
	}
	if sess != nil {
		mediaErr = sess.Close()
	}
	if pendingSess != nil && pendingSess != sess {
		pendingErr = pendingSess.Close()
	}
	return errors.Join(hookErr, rtpErr, mediaErr, pendingErr)
}

func (d *DialogWebrtc) registerDialogCallbacks(c *dialogCallbacks) {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.mediaHanshaker = d
	c.onMediaFailure = d.abortPendingMediaSession
}

func (d *DialogWebrtc) onLocalSDP(ctx context.Context, answered bool, mode string) ([]byte, error) {
	d.mu.Lock()
	current := d.mediaSession
	pending := d.pendingMediaSession
	d.mu.Unlock()
	if current == nil {
		return nil, fmt.Errorf("dialog WebRTC media is not initialized")
	}
	if answered {
		if pending == nil {
			return nil, fmt.Errorf("dialog WebRTC re-INVITE has no pending offer")
		}
		return pending.LocalSDP(ctx, true)
	}

	fork, err := current.Fork(ctx)
	if err != nil {
		return nil, err
	}
	if mode != "" {
		fork.Mode = mode
	}
	d.setPendingMediaSession(fork)
	localSDP, err := fork.LocalSDP(ctx, false)
	if err != nil {
		d.abortPendingMediaSession()
		return nil, err
	}
	return localSDP, nil
}

func (d *DialogWebrtc) onRemoteSDP(ctx context.Context, remoteSDP []byte, offered bool) error {
	if remoteSDP == nil {
		return nil
	}

	d.mu.Lock()
	current := d.mediaSession
	pending := d.pendingMediaSession
	d.mu.Unlock()
	if current == nil {
		return fmt.Errorf("dialog WebRTC media is not initialized")
	}

	if offered {
		if pending == nil {
			return fmt.Errorf("dialog WebRTC re-INVITE has no pending local offer")
		}
		if err := pending.RemoteSDP(ctx, remoteSDP, true); err != nil {
			d.abortPendingMediaSession()
			return err
		}
		if err := pending.Finalize(ctx); err != nil {
			d.abortPendingMediaSession()
			return err
		}
		if pending == current {
			d.commitCurrentMediaSession()
			return nil
		}
		return d.replaceMediaSession(pending)
	}

	// A browser may re-offer codec or direction changes without restarting
	// ICE. MediaSessionWebrtc stages those changes on the current transport.
	// Changed credentials require a fresh ICE/DTLS/SRTP transport instead.
	if err := current.RemoteSDP(ctx, remoteSDP, false); err == nil {
		d.setPendingMediaSession(current)
		return nil
	} else if !errors.Is(err, media.ErrWebRTCICERestart) {
		return err
	}

	fork, err := current.Fork(ctx)
	if err != nil {
		return err
	}
	if err = fork.RemoteSDP(ctx, remoteSDP, false); err != nil {
		_ = fork.Close()
		return err
	}
	d.setPendingMediaSession(fork)
	return nil
}

func (d *DialogWebrtc) onFinalize(ctx context.Context) error {
	d.mu.Lock()
	pending := d.pendingMediaSession
	current := d.mediaSession
	d.mu.Unlock()
	if pending == nil {
		return nil
	}
	if err := pending.Finalize(ctx); err != nil {
		d.abortPendingMediaSession()
		return err
	}
	if pending == current {
		d.commitCurrentMediaSession()
		return nil
	}
	return d.replaceMediaSession(pending)
}

func (d *DialogWebrtc) setPendingMediaSession(sess *media.MediaSessionWebrtc) {
	d.mu.Lock()
	previous := d.pendingMediaSession
	d.pendingMediaSession = sess
	current := d.mediaSession
	d.mu.Unlock()
	if previous != nil && previous != current && previous != sess {
		_ = previous.Close()
	}
}

func (d *DialogWebrtc) abortPendingMediaSession() {
	d.mu.Lock()
	pending := d.pendingMediaSession
	current := d.mediaSession
	d.pendingMediaSession = nil
	d.mu.Unlock()
	if pending == current {
		current.Rollback()
	} else if pending != nil {
		_ = pending.Close()
	}
}

func (d *DialogWebrtc) commitCurrentMediaSession() {
	d.mu.Lock()
	d.pendingMediaSession = nil
	if d.RTPPacketWriter != nil && d.rtpSession != nil && d.mediaSession != nil {
		d.RTPPacketWriter.UpdateWriter(d.rtpSession, d.mediaSession.Codec())
	}
	d.mu.Unlock()
}

func (d *DialogWebrtc) replaceMediaSession(sess *media.MediaSessionWebrtc) error {
	d.mu.Lock()
	oldMediaSession := d.mediaSession
	oldRTPSession := d.rtpSession
	reader := d.RTPPacketReader
	writer := d.RTPPacketWriter
	closed := d.closed
	d.mu.Unlock()
	if closed || oldMediaSession == nil || oldRTPSession == nil || reader == nil || writer == nil {
		_ = sess.Close()
		return fmt.Errorf("dialog WebRTC media is not available for replacement")
	}

	rtpSession := oldRTPSession.Fork(sess)
	if err := rtpSession.MonitorBackground(); err != nil {
		_ = sess.Close()
		return err
	}

	d.mu.Lock()
	if d.closed || d.pendingMediaSession != sess {
		d.mu.Unlock()
		_ = rtpSession.MonitorClose()
		_ = sess.Close()
		return fmt.Errorf("dialog WebRTC media replacement was canceled")
	}
	reader.UpdateReader(rtpSession)
	writer.UpdateWriter(rtpSession, sess.Codec())
	d.mediaSession = sess
	d.rtpSession = rtpSession
	d.pendingMediaSession = nil
	if dtmfReader, ok := d.audioReader.(*DTMFReader); ok {
		dtmfReader.rtpDeadline = sess
	}
	d.mu.Unlock()

	return errors.Join(oldRTPSession.MonitorClose(), oldMediaSession.Close())
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
