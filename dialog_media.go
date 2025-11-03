// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"slices"
	"sync"
	"time"

	"github.com/emiago/diago/audio"
	"github.com/emiago/diago/media"
	"github.com/emiago/diago/media/sdp"
	"github.com/emiago/sipgo/sip"
)

var (
	HTTPDebug = os.Getenv("HTTP_DEBUG") == "true"

	DefaultPlaybackHTTPClient = http.Client{
		Timeout: 20 * time.Second,
	}

	errNoRTPSession = errors.New("no rtp session")
)

func init() {
	if HTTPDebug {
		DefaultPlaybackHTTPClient.Transport = &loggingTransport{}
	}
}

// DialogMedia is common struct for server and client session and it shares same functionality
// which is mostly arround media
type DialogMedia struct {
	mu sync.Mutex

	// media session is RTP local and remote
	// it is forked on media changes and updated on writer and reader
	// must be mutex protected
	// It MUST be always created on Media Session Init
	// Only safe to use after dialog Answered (Completed state)
	mediaSession *media.MediaSession

	// rtp session is created for usage with RTPPacketReader and RTPPacketWriter
	// it adds RTCP layer and RTP monitoring before passing packets to MediaSession
	rtpSession *media.RTPSession
	// Packet reader is default reader for RTP audio stream
	// Use always AudioReader to get current Audio reader
	// Use this only as read only
	// It MUST be always created on Media Session Init
	// Only safe to use after dialog Answered (Completed state)
	RTPPacketReader *media.RTPPacketReader

	// Packet writer is default writer for RTP audio stream
	// Use always AudioWriter to get current Audio reader
	// Use this only as read only
	RTPPacketWriter *media.RTPPacketWriter

	// In case we are chaining audio readers
	audioReader io.Reader
	audioWriter io.Writer

	// lastInvite is actual last invite sent by remote REINVITE
	// We do not use sipgo as this needs mutex but also keeping original invite
	lastInvite *sip.Request

	onClose       func() error
	onMediaUpdate func(*DialogMedia)

	closed bool
}

func (d *DialogMedia) Close() error {
	// Any hook attached
	// Prevent double exec
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	d.closed = true

	onClose := d.onClose
	d.onClose = nil
	m := d.mediaSession

	d.mu.Unlock()

	var e1, e2 error
	if onClose != nil {
		e1 = onClose()
	}

	if m != nil {
		e2 = m.Close()
	}
	return errors.Join(e1, e2)
}

func (d *DialogMedia) OnClose(f func() error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.onCloseUnsafe(f)
}

func (d *DialogMedia) onCloseUnsafe(f func() error) {
	if d.onClose != nil {
		prev := d.onClose
		d.onClose = func() error {
			return errors.Join(prev(), f())
		}
		return
	}
	d.onClose = f
}

func (d *DialogMedia) InitMediaSession(m *media.MediaSession, r *media.RTPPacketReader, w *media.RTPPacketWriter) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.initMediaSessionUnsafe(m, r, w)
}

func (d *DialogMedia) initMediaSessionUnsafe(m *media.MediaSession, r *media.RTPPacketReader, w *media.RTPPacketWriter) {
	d.mediaSession = m
	d.RTPPacketReader = r
	d.RTPPacketWriter = w
}

func (d *DialogMedia) initRTPSessionUnsafe(m *media.MediaSession, rtpSess *media.RTPSession) {
	d.mediaSession = m
	d.rtpSession = rtpSess
	d.RTPPacketReader = media.NewRTPPacketReaderSession(rtpSess)
	d.RTPPacketWriter = media.NewRTPPacketWriterSession(rtpSess)
}

func (d *DialogMedia) initMediaSessionFromConf(conf MediaConfig) error {
	if d.mediaSession != nil {
		// To allow testing or customizing current underhood session, this may be
		// precreated, so we want to return if already initialized.
		// Ex: To fake IO on RTP connection or different media stacks
		return nil
	}

	bindIP := conf.bindIP
	if bindIP == nil {
		var err error
		bindIP, _, err = sip.ResolveInterfacesIP("ip4", nil)
		if err != nil {
			return err
		}
	}

	sess := &media.MediaSession{
		Codecs:     slices.Clone(conf.Codecs),
		Laddr:      net.UDPAddr{IP: bindIP, Port: 0},
		ExternalIP: conf.externalIP,
		Mode:       sdp.ModeSendrecv,
		SecureRTP:  conf.secureRTP,
		SRTPAlg:    conf.SecureRTPAlg,
		RTPNAT:     conf.rtpNAT,
	}

	if err := sess.Init(); err != nil {
		return err
	}
	d.mediaSession = sess
	return nil
}

// RTPSession returns underhood rtp session
// NOTE: this can be nil
func (d *DialogMedia) RTPSession() *media.RTPSession {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.rtpSession
}

func (d *DialogMedia) MediaSession() *media.MediaSession {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.mediaSession
}

func (d *DialogMedia) handleMediaUpdate(req *sip.Request, tx sip.ServerTransaction, contactHDR sip.Header) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.lastInvite = req

	if err := d.sdpReInviteUnsafe(req.Body()); err != nil {
		return tx.Respond(sip.NewResponseFromRequest(req, sip.StatusRequestTerminated, "Request Terminated - "+err.Error(), nil))
	}

	// Reply with updated SDP
	sd := d.mediaSession.LocalSDP()
	res := sip.NewResponseFromRequest(req, sip.StatusOK, "OK", sd)
	res.AppendHeader(contactHDR)
	res.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	return tx.Respond(res)
}

// Must be protected with lock
func (d *DialogMedia) sdpReInviteUnsafe(sdp []byte) error {
	if d.mediaSession == nil {
		return fmt.Errorf("no media session present")
	}

	if err := d.sdpUpdateUnsafe(sdp); err != nil {
		return err
	}

	if d.onMediaUpdate != nil {
		d.onMediaUpdate(d)
	}

	return nil
}

func (d *DialogMedia) checkEarlyMedia(remoteSDP []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	// RTP Session is only created when negotiation is finished. We use this to detect existing media
	if d.rtpSession == nil {
		return errNoRTPSession
	}
	return d.sdpUpdateUnsafe(remoteSDP)
}

func (d *DialogMedia) sdpUpdateUnsafe(sdp []byte) error {
	msess := d.mediaSession.Fork()
	if err := msess.RemoteSDP(sdp); err != nil {
		return fmt.Errorf("sdp update media remote SDP applying failed: %w", err)
	}

	// Stop existing rtp
	if d.rtpSession != nil {
		if err := d.rtpSession.Close(); err != nil {
			return err
		}
	}

	rtpSess := media.NewRTPSession(msess)
	d.onCloseUnsafe(func() error {
		return rtpSess.Close()
	})

	if err := rtpSess.MonitorBackground(); err != nil {
		rtpSess.Close()
		return err
	}

	d.RTPPacketReader.UpdateRTPSession(rtpSess)
	d.RTPPacketWriter.UpdateRTPSession(rtpSess)

	// update the reference
	d.mediaSession = msess
	d.rtpSession = rtpSess
	return nil
}

type AudioReaderOption func(d *DialogMedia) error

type MediaProps struct {
	Codec media.Codec
	Laddr string
	Raddr string
}

func WithAudioReaderMediaProps(p *MediaProps) AudioReaderOption {
	return func(d *DialogMedia) error {
		p.Codec = media.CodecAudioFromSession(d.mediaSession)
		p.Laddr = d.mediaSession.Laddr.String()
		p.Raddr = d.mediaSession.Raddr.String()
		return nil
	}
}

// WithAudioReaderRTPStats creates RTP Statistics interceptor on audio reader
func WithAudioReaderRTPStats(hook media.OnRTPReadStats) AudioReaderOption {
	return func(d *DialogMedia) error {
		r := &media.RTPStatsReader{
			Reader:         d.getAudioReader(),
			RTPSession:     d.rtpSession,
			OnRTPReadStats: hook,
		}
		d.audioReader = r
		return nil
	}
}

// WithAudioReaderDTMF creates DTMF interceptor
func WithAudioReaderDTMF(r *DTMFReader) AudioReaderOption {
	return func(d *DialogMedia) error {
		r.dtmfReader = media.NewRTPDTMFReader(media.CodecTelephoneEvent8000, d.RTPPacketReader, d.getAudioReader())
		r.mediaSession = d.mediaSession

		d.audioReader = r
		return nil
	}
}

// AudioReader gets current audio reader. It MUST be called after Answer.
// Use AuidioListen for optimized reading.
// Reading buffer should be equal or bigger of media.RTPBufSize
// Options allow more intercepting audio reading like Stats or DTMF
// NOTE that this interceptors will stay,
func (d *DialogMedia) AudioReader(opts ...AudioReaderOption) (io.Reader, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	for _, o := range opts {
		if err := o(d); err != nil {
			return nil, err
		}
	}
	return d.getAudioReader(), nil
}

func (d *DialogMedia) getAudioReader() io.Reader {
	if d.audioReader != nil {
		return d.audioReader
	}
	return d.RTPPacketReader
}

// audioReaderProps
func (d *DialogMedia) audioReaderProps(p *MediaProps) io.Reader {
	d.mu.Lock()
	defer d.mu.Unlock()

	WithAudioReaderMediaProps(p)(d)
	return d.getAudioReader()
}

// SetAudioReader adds/changes audio reader.
// Use this when you want to have interceptors of your audio
func (d *DialogMedia) SetAudioReader(r io.Reader) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.audioReader = r
}

type AudioWriterOption func(d *DialogMedia) error

func WithAudioWriterMediaProps(p *MediaProps) AudioWriterOption {
	return func(d *DialogMedia) error {
		p.Codec = media.CodecAudioFromSession(d.mediaSession)
		p.Laddr = d.mediaSession.Laddr.String()
		p.Raddr = d.mediaSession.Raddr.String()
		return nil
	}
}

// WithAudioReaderRTPStats creates RTP Statistics interceptor on audio reader
func WithAudioWriterRTPStats(hook media.OnRTPWriteStats) AudioWriterOption {
	return func(d *DialogMedia) error {
		w := media.RTPStatsWriter{
			Writer:          d.getAudioWriter(),
			RTPSession:      d.rtpSession,
			OnRTPWriteStats: hook,
		}
		d.audioWriter = &w
		return nil
	}
}

// WithAudioWriterDTMF creates DTMF interceptor
func WithAudioWriterDTMF(r *DTMFWriter) AudioWriterOption {
	return func(d *DialogMedia) error {
		r.dtmfWriter = media.NewRTPDTMFWriter(media.CodecTelephoneEvent8000, d.RTPPacketWriter, d.getAudioWriter())
		r.mediaSession = d.mediaSession
		d.audioWriter = r
		return nil
	}
}

func (d *DialogMedia) AudioWriter(opts ...AudioWriterOption) (io.Writer, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	for _, o := range opts {
		if err := o(d); err != nil {
			return nil, err
		}
	}

	return d.getAudioWriter(), nil
}

func (d *DialogMedia) getAudioWriter() io.Writer {
	if d.audioWriter != nil {
		return d.audioWriter
	}
	return d.RTPPacketWriter
}

func (d *DialogMedia) audioWriterProps(p *MediaProps) io.Writer {
	d.mu.Lock()
	defer d.mu.Unlock()

	WithAudioWriterMediaProps(p)(d)
	return d.getAudioWriter()
}

// SetAudioWriter adds/changes audio reader.
// Use this when you want to have pipelines of your audio
func (d *DialogMedia) SetAudioWriter(r io.Writer) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.audioWriter = r
}

func (d *DialogMedia) Media() *DialogMedia {
	return d
}

// Echo does audio echo for you
func (d *DialogMedia) Echo() error {
	audioR, err := d.AudioReader()
	if err != nil {
		return err
	}
	audioW, err := d.AudioWriter()
	if err != nil {
		return err
	}

	_, err = media.Copy(audioR, audioW)
	return err
}

// PlaybackCreate creates playback for audio
func (d *DialogMedia) PlaybackCreate() (AudioPlayback, error) {
	mprops := MediaProps{}
	w := d.audioWriterProps(&mprops)
	if w == nil {
		return AudioPlayback{}, fmt.Errorf("no media setup")
	}
	p := NewAudioPlayback(w, mprops.Codec)
	// On each play it needs reset RTP timestamp
	p.onPlay = d.RTPPacketWriter.ResetTimestamp
	return p, nil
}

// PlaybackControlCreate creates playback for audio with controls like mute unmute
func (d *DialogMedia) PlaybackControlCreate() (AudioPlaybackControl, error) {
	// NOTE we should avoid returning pointers for any IN dialplan to avoid heap
	mprops := MediaProps{}
	w := d.audioWriterProps(&mprops)

	if w == nil {
		return AudioPlaybackControl{}, fmt.Errorf("no media setup")
	}
	// Audio is controled via audio reader/writer
	control := &audioControl{
		Writer: w,
	}

	p := AudioPlaybackControl{
		AudioPlayback: NewAudioPlayback(control, mprops.Codec),
		control:       control,
	}
	return p, nil
}

// PlaybackRingtoneCreate is creating playback for ringtone
//
// Experimental
func (d *DialogMedia) PlaybackRingtoneCreate() (AudioRingtone, error) {
	mprops := MediaProps{}
	w := d.audioWriterProps(&mprops)
	if w == nil {
		return AudioRingtone{}, fmt.Errorf("no media setup")
	}

	ringtone, err := audio.RingtoneLoadPCM(mprops.Codec)
	if err != nil {
		return AudioRingtone{}, err
	}

	encoder := audio.PCMEncoderWriter{}
	if err := encoder.Init(mprops.Codec, w); err != nil {
		return AudioRingtone{}, err
	}

	ar := AudioRingtone{
		writer:       &encoder,
		ringtone:     ringtone,
		sampleSize:   mprops.Codec.Samples16(),
		mediaSession: d.mediaSession,
	}
	return ar, nil
}

// AudioStereoRecordingCreate creates Stereo Recording audio Pipeline and stores as Wav file format
// For audio to be recorded use AudioReader and AudioWriter from Recording
//
// Tips:
// If you want to make permanent in audio pipeline use SetAudioReader, SetAudioWriter
//
// NOTE: API WILL change
func (d *DialogMedia) AudioStereoRecordingCreate(wawFile *os.File) (AudioStereoRecordingWav, error) {
	mpropsW := MediaProps{}
	aw := d.audioWriterProps(&mpropsW)
	if aw == nil {
		return AudioStereoRecordingWav{}, fmt.Errorf("no media setup")
	}

	mpropsR := MediaProps{}
	ar := d.audioReaderProps(&mpropsR)
	if ar == nil {
		return AudioStereoRecordingWav{}, fmt.Errorf("no media setup")
	}
	codec := mpropsW.Codec
	if mpropsR.Codec != mpropsW.Codec {
		return AudioStereoRecordingWav{}, fmt.Errorf("codecs of reader and writer need to match for stereo")
	}
	// Create wav file to store recording
	// Now create WavWriter to have Wav Container written
	wavWriter := audio.NewWavWriter(wawFile)

	mon := audio.MonitorPCMStereo{}
	if err := mon.Init(wavWriter, codec, ar, aw); err != nil {
		wavWriter.Close()
		return AudioStereoRecordingWav{}, err
	}

	r := AudioStereoRecordingWav{
		wawWriter: wavWriter,
		mon:       mon,
	}
	return r, nil
}

// Listen keeps reading stream until it gets closed or deadlined
// Use ListenBackground or ListenContext for better control
func (d *DialogMedia) Listen() (err error) {
	buf := make([]byte, media.RTPBufSize)
	audioReader, err := d.AudioReader()
	if err != nil {
		return err
	}

	for {
		_, err := audioReader.Read(buf)
		if err != nil {
			return err
		}
	}
}

// ListenBackground listens on stream in background and allows correct stoping of stream on network layer
func (d *DialogMedia) ListenBackground() (stop func() error, err error) {
	buf := make([]byte, media.RTPBufSize)
	audioReader, err := d.AudioReader()
	if err != nil {
		return nil, err
	}

	wg := sync.WaitGroup{}
	var readErr error
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			_, err := audioReader.Read(buf)
			if err != nil {
				if err, ok := err.(net.Error); ok && err.Timeout() {
					return
				}
				readErr = err
				return
			}
		}
	}()

	return func() error {
		if err := d.mediaSession.StopRTP(1, 0); err != nil {
			return err
		}
		wg.Wait() // This makes sure we have exited reading
		return readErr
	}, nil
}

// ListenContext listens until context is canceled.
func (d *DialogMedia) ListenContext(pctx context.Context) error {
	buf := make([]byte, media.RTPBufSize)
	ctx, cancel := context.WithCancel(pctx)
	defer cancel()

	go func() {
		<-ctx.Done()
		if pctx.Err() != nil {
			d.mediaSession.StopRTP(1, 0)
		}
	}()
	audioReader, err := d.AudioReader()
	if err != nil {
		return err
	}
	for {
		_, err := audioReader.Read(buf)
		if err != nil {
			if err, ok := err.(net.Error); ok && err.Timeout() {
				return nil
			}
			return err
		}
	}
}

func (d *DialogMedia) ListenUntil(dur time.Duration) error {
	buf := make([]byte, media.RTPBufSize)

	d.mediaSession.StopRTP(1, dur)
	audioReader, err := d.AudioReader()
	if err != nil {
		return err
	}
	for {
		_, err := audioReader.Read(buf)
		if err != nil {
			return err
		}
	}
}

func (d *DialogMedia) StopRTP(rw int8, dur time.Duration) error {
	return d.mediaSession.StopRTP(rw, dur)
}

func (d *DialogMedia) StartRTP(rw int8, dur time.Duration) error {
	return d.mediaSession.StartRTP(rw)
}

type DTMFReader struct {
	mediaSession *media.MediaSession
	dtmfReader   *media.RTPDtmfReader
	onDTMF       func(dtmf rune) error
}

// AudioReaderDTMF is DTMF over RTP. It reads audio and provides hook for dtmf while listening for audio
// Use Listen or OnDTMF after this call
func (m *DialogMedia) AudioReaderDTMF() *DTMFReader {
	ar, _ := m.AudioReader()
	return &DTMFReader{
		dtmfReader:   media.NewRTPDTMFReader(media.CodecTelephoneEvent8000, m.RTPPacketReader, ar),
		mediaSession: m.mediaSession,
	}
}

func (d *DTMFReader) Listen(onDTMF func(dtmf rune) error, dur time.Duration) error {
	d.onDTMF = onDTMF
	buf := make([]byte, media.RTPBufSize)
	for {
		if _, err := d.readDeadline(buf, dur); err != nil {
			return err
		}
	}
}

// readDeadline(reads RTP until
func (d *DTMFReader) readDeadline(buf []byte, dur time.Duration) (n int, err error) {
	mediaSession := d.mediaSession
	if dur > 0 {
		// Stop RTP
		mediaSession.StopRTP(1, dur)
		defer mediaSession.StartRTP(2)
	}
	return d.Read(buf)
}

// OnDTMF must be called before audio reading
func (d *DTMFReader) OnDTMF(onDTMF func(dtmf rune) error) {
	d.onDTMF = onDTMF
}

// Read exposes io.Reader that can be used as AudioReader
func (d *DTMFReader) Read(buf []byte) (n int, err error) {
	// This is optimal way of reading audio and DTMF
	dtmfReader := d.dtmfReader
	n, err = dtmfReader.Read(buf)
	if err != nil {
		return n, err
	}

	if dtmf, ok := dtmfReader.ReadDTMF(); ok {
		if err := d.onDTMF(dtmf); err != nil {
			return n, err
		}
	}
	return n, nil
}

type DTMFWriter struct {
	mediaSession *media.MediaSession
	dtmfWriter   *media.RTPDtmfWriter
}

func (m *DialogMedia) AudioWriterDTMF() *DTMFWriter {
	return &DTMFWriter{
		dtmfWriter:   media.NewRTPDTMFWriter(media.CodecTelephoneEvent8000, m.RTPPacketWriter, m.getAudioWriter()),
		mediaSession: m.mediaSession,
	}
}

func (w *DTMFWriter) WriteDTMF(dtmf rune) error {
	return w.dtmfWriter.WriteDTMF(dtmf)
}

// AudioReader exposes DTMF audio writer. You should use this for parallel audio processing
func (w *DTMFWriter) AudioWriter() *media.RTPDtmfWriter {
	return w.dtmfWriter
}

// Write exposes as io.Writer that can be used as AudioWriter
func (w *DTMFWriter) Write(buf []byte) (n int, err error) {
	return w.dtmfWriter.Write(buf)
}
