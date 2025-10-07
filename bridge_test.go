// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/emiago/diago/media"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestBridgeProxy(t *testing.T) {
	b := NewBridge()
	b.WaitDialogsNum = 99 // Do not start proxy

	incoming := &DialogServerSession{
		DialogMedia: DialogMedia{
			mediaSession: &media.MediaSession{
				Codecs: []media.Codec{media.CodecAudioAlaw},
			},
			audioReader:     bytes.NewBuffer(make([]byte, 9999)),
			audioWriter:     bytes.NewBuffer(make([]byte, 0)),
			RTPPacketReader: media.NewRTPPacketReader(nil, media.CodecAudioAlaw),
			RTPPacketWriter: media.NewRTPPacketWriter(nil, media.CodecAudioAlaw),
		},
	}
	outgoing := &DialogClientSession{
		DialogMedia: DialogMedia{
			mediaSession: &media.MediaSession{
				Codecs: []media.Codec{media.CodecAudioAlaw},
			},
			audioReader:     bytes.NewBuffer(make([]byte, 9999)),
			audioWriter:     bytes.NewBuffer(make([]byte, 0)),
			RTPPacketReader: media.NewRTPPacketReader(nil, media.CodecAudioAlaw),
			RTPPacketWriter: media.NewRTPPacketWriter(nil, media.CodecAudioAlaw),
		},
	}

	err := b.AddDialogSession(incoming)
	require.NoError(t, err)
	err = b.AddDialogSession(outgoing)
	require.NoError(t, err)

	err = b.proxyMedia()
	require.ErrorIs(t, err, io.EOF)

	// Confirm all data is proxied
	assert.Equal(t, 9999, incoming.audioWriter.(*bytes.Buffer).Len())
	assert.Equal(t, 9999, outgoing.audioWriter.(*bytes.Buffer).Len())
}

func TestBridgeNoTranscodingAllowed(t *testing.T) {
	b := NewBridge()
	// b.waitDialogsNum = 99 // Do not start proxy

	incoming := &DialogServerSession{
		DialogMedia: DialogMedia{
			mediaSession: &media.MediaSession{
				Codecs: []media.Codec{media.CodecAudioAlaw},
			},
			// RTPPacketReader: media.NewRTPPacketReader(nil, media.CodecAudioAlaw),
			// RTPPacketWriter: media.NewRTPPacketWriter(nil, media.CodecAudioAlaw),
		},
	}
	outgoing := &DialogClientSession{
		DialogMedia: DialogMedia{
			mediaSession: &media.MediaSession{
				Codecs: []media.Codec{media.CodecAudioUlaw},
			},
			// RTPPacketReader: media.NewRTPPacketReader(nil, media.CodecAudioUlaw),
			// RTPPacketWriter: media.NewRTPPacketWriter(nil, media.CodecAudioUlaw),
		},
	}

	err := b.AddDialogSession(incoming)
	require.NoError(t, err)
	err = b.AddDialogSession(outgoing)
	require.Error(t, err)
}

func TestIntegrationBridging(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// Create transaction users, as many as needed.
	ua, _ := sipgo.NewUA(
		sipgo.WithUserAgent("inbound"),
	)
	defer ua.Close()
	tu := NewDiago(ua, WithTransport(
		Transport{
			Transport: "udp",
			BindHost:  "127.0.0.1",
			BindPort:  5090,
		},
	))

	err := tu.ServeBackground(ctx, func(in *DialogServerSession) {
		in.Trying()
		in.Ringing()
		in.Answer()

		inCtx := in.Context()
		ctx, cancel := context.WithTimeout(inCtx, 15*time.Second)
		defer cancel()

		// Wa want to bridge this call with originator
		bridge := NewBridge()
		// Add us in bridge
		if err := bridge.AddDialogSession(in); err != nil {
			t.Log("Adding dialog in bridge failed", err)
			return
		}

		out, err := tu.InviteBridge(ctx, sip.Uri{User: "test", Host: "127.0.0.200", Port: 5090}, &bridge, InviteOptions{})
		if err != nil {
			t.Log("Dialing failed", err)
			return
		}

		outCtx := out.Context()
		defer func() {
			hctx, hcancel := context.WithTimeout(outCtx, 5*time.Second)
			out.Hangup(hctx)
			hcancel()
		}()

		// This is beauty, as you can even easily detect who hangups
		select {
		case <-inCtx.Done():
		case <-outCtx.Done():
		}

		// How to now do bridging
	})
	assert.NoError(t, err)

	{
		ua, _ := sipgo.NewUA()
		defer ua.Close()

		dg := NewDiago(ua, WithTransport(
			Transport{
				Transport: "udp",
				BindHost:  "127.0.0.200",
				BindPort:  5090,
			},
		))

		err := dg.ServeBackground(context.Background(), func(d *DialogServerSession) {
			ctx := d.Context()
			err = d.Answer()
			require.NoError(t, err)

			// ms := d.mediaSession
			buf := make([]byte, media.RTPBufSize)
			r, _ := d.AudioReader()
			n, err := r.Read(buf)
			require.NoError(t, err)

			w, _ := d.AudioWriter()
			w.Write(buf[:n])
			require.NoError(t, err)

			<-ctx.Done()
		})
		require.NoError(t, err)
	}

	{
		ua, _ := sipgo.NewUA()
		defer ua.Close()

		dg := newDialer(ua)
		dialog, err := dg.Invite(context.TODO(), sip.Uri{Host: "127.0.0.1", Port: 5090}, InviteOptions{})
		require.NoError(t, err)
		defer dialog.Close()

		w, _ := dialog.AudioWriter()
		_, err = w.Write([]byte("1234"))
		require.NoError(t, err)

		buf := make([]byte, media.RTPBufSize)
		r, _ := dialog.AudioReader()
		r.Read(buf)
		require.NoError(t, err)

		t.Log("Hanguping")
		dialog.Hangup(ctx)
	}
}
