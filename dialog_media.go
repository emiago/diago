// SPDX-License-Identifier: BSD-2-Clause
// Copyright (C) 2024 Emir Aganovic

package diago

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httputil"
	"os"
	"time"

	"github.com/emiago/diago/audio"
	"github.com/emiago/diago/media"
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
	mediaSession *media.MediaSession

	RTPPacketWriter *media.RTPPacketWriter
	RTPPacketReader *media.RTPPacketReader
}

// Must be protected with lock
func (d *DialogMedia) sdpReInvite(sdp []byte) error {
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

// DialogSession interface
func (d *DialogMedia) Media() *DialogMedia {
	return d
}

func (d *DialogMedia) PlaybackCreate() (Playback, error) {
	// NOTE we should avoid returning pointers for any IN dialplan to avoid heap
	rtpPacketWriter := d.RTPPacketWriter
	pt := rtpPacketWriter.PayloadType
	enc, err := audio.NewPCMEncoder(pt, rtpPacketWriter)
	if err != nil {
		return Playback{}, err
	}

	p := Playback{
		writer:     enc,
		SampleRate: rtpPacketWriter.SampleRate,
		SampleDur:  20 * time.Millisecond,
	}
	return p, nil
}

func (d *DialogMedia) PlaybackControlCreate() (PlaybackControl, error) {
	// NOTE we should avoid returning pointers for any IN dialplan to avoid heap
	rtpPacketWriter := d.RTPPacketWriter
	if rtpPacketWriter == nil {
		return PlaybackControl{}, fmt.Errorf("no media setup")
	}

	pt := rtpPacketWriter.PayloadType
	enc, err := audio.NewPCMEncoder(pt, rtpPacketWriter)
	if err != nil {
		return PlaybackControl{}, err
	}

	// Audio is controled via audio reader/writer
	control := &audioControl{
		Writer: enc,
	}

	p := PlaybackControl{
		Playback: Playback{
			writer:     control,
			SampleRate: rtpPacketWriter.SampleRate,
			SampleDur:  20 * time.Millisecond,
		},
		control: control,
	}
	return p, nil
}

func (d *DialogMedia) PlaybackFile(ctx context.Context, filename string) error {
	m := d.Media()
	if m.RTPPacketWriter == nil {
		return fmt.Errorf("call not answered")
	}

	p, err := d.PlaybackCreate()
	if err != nil {
		return err
	}

	err = p.PlayFile(ctx, filename)
	return err
}

func (d *DialogMedia) PlaybackURL(ctx context.Context, urlStr string) error {
	m := d.Media()
	if m.RTPPacketWriter == nil {
		return fmt.Errorf("call not answered")
	}

	p, err := d.PlaybackCreate()
	if err != nil {
		return err
	}

	err = p.PlayURL(ctx, urlStr)
	return err
}

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
