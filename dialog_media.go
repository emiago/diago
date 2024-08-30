// SPDX-License-Identifier: MPL-2.0
// Copyright (C) 2024 Emir Aganovic

package diago

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"os"
	"sync"
	"time"

	"github.com/emiago/diago/media"
	"github.com/emiago/diago/media/sdp"
	"github.com/rs/zerolog/log"
)

var (
	HTTPDebug = os.Getenv("HTTP_DEBUG") == "true"
	// TODO remove client singleton
	client = http.Client{
		Timeout: 10 * time.Second,
	}
)

func init() {
	if HTTPDebug {
		client.Transport = &loggingTransport{}
	}
}

// DialogMedia is io.ReaderWriter for RTP. By default it exposes RTP Read and Write.
// Not thread safe and must be protected by lock
type DialogMedia struct {
	// media session is RTP local and remote
	// it is forked on media changes and updated on writer and reader
	// must be mutex protected
	mu sync.Mutex

	mediaSession *media.MediaSession

	// Packet reader is default reader for RTP audio stream
	// Use always AudioReader to get current Audio reader
	// This is not thread safe! Use AudioReaderWithProps to get media safely
	RTPPacketReader *media.RTPPacketReader

	// Packet writer is default writer for RTP audio stream
	// Use always AudioWriter to get current Audio reader
	// This is not thread safe! Use AudioWriterWithProps to get media safely
	RTPPacketWriter *media.RTPPacketWriter

	// In case we are chaining audio readers
	audioReader io.Reader
	audioWriter io.Writer

	formats sdp.Formats
}

func (d *DialogMedia) InitMediaSession(m *media.MediaSession, r *media.RTPPacketReader, w *media.RTPPacketWriter) {
	d.mu.Lock()
	defer d.mu.Unlock()

	d.mediaSession = m
	d.RTPPacketReader = r
	d.RTPPacketWriter = w
}

// Must be protected with lock
func (d *DialogMedia) sdpReInvite(sdp []byte) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	msess := d.mediaSession.Fork()
	if err := msess.RemoteSDP(sdp); err != nil {
		log.Error().Err(err).Msg("reinvite media remote SDP applying failed")
		return fmt.Errorf("Malformed SDP")
	}

	d.mediaSession = msess

	rtpSess := media.NewRTPSession(msess)
	d.RTPPacketReader.UpdateRTPSession(rtpSess)
	d.RTPPacketWriter.UpdateRTPSession(rtpSess)
	rtpSess.MonitorBackground()

	log.Info().
		Str("formats", msess.Formats.String()).
		Str("localAddr", msess.Laddr.String()).
		Str("remoteAddr", msess.Raddr.String()).
		Msg("Media/RTP session updated")
	return nil
}

func (d *DialogMedia) MediaSession() *media.MediaSession {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.mediaSession
}

func (d *DialogMedia) AudioReader() io.Reader {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.audioReader != nil {
		return d.audioReader
	}
	return d.RTPPacketReader
}

type MediaProps struct {
	Codec media.Codec
}

// AudioReaderWithProps Parses MediaProps with current reader
func (d *DialogMedia) AudioReaderWithProps(p *MediaProps) io.Reader {
	d.mu.Lock()
	defer d.mu.Unlock()

	p.Codec = media.CodecFromSession(d.mediaSession)
	if d.audioReader != nil {
		return d.audioReader
	}
	return d.RTPPacketReader
}

// SetAudioReader adds/changes audio reader.
// Use this when you want to have interceptors of your audio
func (d *DialogMedia) SetAudioReader(r io.Reader) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.audioReader = r
}

func (d *DialogMedia) AudioWriter() io.Writer {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.audioReader != nil {
		return d.audioWriter
	}
	return d.RTPPacketWriter
}

func (d *DialogMedia) AudioWriterWithProps(p *MediaProps) io.Writer {
	d.mu.Lock()
	defer d.mu.Unlock()

	p.Codec = media.CodecFromSession(d.mediaSession)
	if d.audioReader != nil {
		return d.audioWriter
	}
	return d.RTPPacketWriter
}

// SetAudioWriter adds/changes audio reader.
// Use this when you want to have interceptors of your audio
func (d *DialogMedia) SetAudioWriter(r io.Writer) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.audioWriter = r
}

func (d *DialogMedia) Media() *DialogMedia {
	return d
}

// PlaybackCreate creates playback for audio
func (d *DialogMedia) PlaybackCreate() (AudioPlayback, error) {
	mprops := MediaProps{}
	w := d.AudioWriterWithProps(&mprops)
	if w == nil {
		return AudioPlayback{}, fmt.Errorf("no media setup")
	}
	p := NewAudioPlayback(w, mprops.Codec)
	return p, nil
}

// PlaybackControlCreate creates playback for audio with controls like mute unmute
func (d *DialogMedia) PlaybackControlCreate() (AudioPlaybackControl, error) {
	// NOTE we should avoid returning pointers for any IN dialplan to avoid heap
	mprops := MediaProps{}
	w := d.AudioWriterWithProps(&mprops)

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

// Listen is main function to be called when we want to listen stream on this dialog.
// Example:
// - you attach your media reader/writer. ex Recording
// - Once ready you start to listen on stream
//
// This approach gives caller control when to listen stream but also it removes
// need to
func (m *DialogMedia) Listen() error {
	buf := make([]byte, media.RTPBufSize)
	r := m.AudioReader()
	for {
		_, err := r.Read(buf)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}

			return err
		}
	}
}

// TODO Listen With Control. Same as Audio playback control we may want to mute/unmute incoming stream

type loggingTransport struct{}

func (s *loggingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	bytes, _ := httputil.DumpRequestOut(r, false)

	resp, err := http.DefaultTransport.RoundTrip(r)
	// err is returned after dumping the response

	respBytes, _ := httputil.DumpResponse(resp, false)
	bytes = append(bytes, respBytes...)

	log.Debug().Msgf("HTTP Debug:\n%s\n", bytes)

	return resp, err
}
