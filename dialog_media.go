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

	// Packet readers are default readers for RTP audio stream
	// Use always AudioReader to get current Audio reader
	RTPPacketWriter *media.RTPPacketWriter
	RTPPacketReader *media.RTPPacketReader

	audioReader io.Reader
	audioWriter io.Writer

	formats sdp.Formats
}

func (d *DialogMedia) SetSession(m *media.MediaSession, r *media.RTPPacketReader, w *media.RTPPacketWriter) {
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

func (d *DialogMedia) SetAudioWriter(r io.Writer) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.audioWriter = r
}

// DialogSession interface
func (d *DialogMedia) Media() *DialogMedia {
	return d
}

// PlaybackCreate creates playback for audio
func (d *DialogMedia) PlaybackCreate() (AudioPlayback, error) {
	rtpPacketWriter := d.RTPPacketWriter
	pt := rtpPacketWriter.PayloadType
	codec := media.CodecFromPayloadType(pt)

	w := d.AudioWriter()
	p := NewAudioPlayback(w, codec)
	return p, nil
}

// PlaybackControlCreate creates playback for audio with controls like mute unmute
func (d *DialogMedia) PlaybackControlCreate() (AudioPlaybackControl, error) {
	// NOTE we should avoid returning pointers for any IN dialplan to avoid heap
	rtpPacketWriter := d.RTPPacketWriter
	if rtpPacketWriter == nil {
		return AudioPlaybackControl{}, fmt.Errorf("no media setup")
	}
	pt := rtpPacketWriter.PayloadType

	// Audio is controled via audio reader/writer
	w := d.AudioWriter()
	control := &audioControl{
		Writer: w,
	}

	codec := media.CodecFromPayloadType(pt)
	p := AudioPlaybackControl{
		AudioPlayback: NewAudioPlayback(control, codec),
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
