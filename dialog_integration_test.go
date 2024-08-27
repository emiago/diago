// SPDX-License-Identifier: MPL-2.0
// Copyright (C) 2024 Emir Aganovic

package diago

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/emiago/diago/media"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/emiago/sipgox"
	"github.com/pion/rtp"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) {
	lev, err := zerolog.ParseLevel(os.Getenv("LOG_LEVEL"))
	if err != nil || lev == zerolog.NoLevel {
		lev = zerolog.InfoLevel
	}

	zerolog.TimeFieldFormat = zerolog.TimeFormatUnixMicro
	log.Logger = zerolog.New(zerolog.ConsoleWriter{
		Out:        os.Stdout,
		TimeFormat: time.StampMicro,
	}).With().Timestamp().Logger().Level(lev)

	sip.SIPDebug = os.Getenv("SIP_DEBUG") != ""

	m.Run()
}

func TestIntegrationInbound(t *testing.T) {
	// Create transaction users, as many as needed.
	ua, _ := sipgo.NewUA(
		sipgo.WithUserAgent("inbound"),
	)
	dg := NewDiago(ua)

	ctx := context.TODO()

	err := dg.ServeBackground(ctx, func(d *DialogServerSession) {
		// Add some routing
		if d.ToUser() == "alice" {
			d.Progress()
			d.Ringing()
			d.Answer()

			<-d.Context().Done()
			return
		}

		d.Respond(sip.StatusForbidden, "Forbidden", nil)

		<-d.Context().Done()
	})
	require.NoError(t, err)

	// Transaction User is basically driving dialog session
	// It can be inbound/UAS or outbound/UAC behavior

	// TU can ReceiveCall and it will create a DialogSessionServer
	// TU can Dial endpoint and create a DialogSessionClient (Channel)
	// DialogSessionClient can be bridged with other sessions

	{
		ua, _ := sipgo.NewUA()
		phone := sipgox.NewPhone(ua)

		// Forbiddden
		_, err := phone.Dial(context.TODO(), sip.Uri{User: "noroute", Host: "127.0.0.1", Port: 5060}, sipgox.DialOptions{})
		require.Error(t, err)

		// Answered call
		dialog, err := phone.Dial(context.TODO(), sip.Uri{User: "alice", Host: "127.0.0.1", Port: 5060}, sipgox.DialOptions{})
		require.NoError(t, err)
		defer dialog.Close()

		dialog.Hangup(ctx)
	}
}

func TestIntegrationBridging(t *testing.T) {
	// Create transaction users, as many as needed.
	ua, _ := sipgo.NewUA(
		sipgo.WithUserAgent("inbound"),
	)
	tu := NewDiago(ua, WithTransport(
		Transport{
			Transport: "udp",
			BindHost:  "127.0.0.1",
			BindPort:  5090,
		},
	))

	ctx := context.TODO()

	err := tu.ServeBackground(ctx, func(in *DialogServerSession) {
		in.Progress()
		in.Ringing()
		in.Answer()

		inCtx := in.Context()
		ctx, cancel := context.WithTimeout(inCtx, 15*time.Second)
		defer cancel()

		// Wa want to bridge this call with originator
		bridge := NewBridge()
		// Add us in bridge
		if err := bridge.AddDialogSession(in); err != nil {
			log.Error().Err(err).Msg("Adding dialog in bridge failed")
			return
		}

		out, err := tu.InviteBridge(ctx, sip.Uri{User: "test", Host: "127.0.0.200", Port: 5090}, &bridge, InviteOptions{})
		if err != nil {
			log.Error().Err(err).Msg("Dialing failed")
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

	// Transaction User is basically driving dialog session
	// It can be inbound/UAS or outbound/UAC behavior

	// TU can ReceiveCall and it will create a DialogSessionServer
	// TU can Dial endpoint and create a DialogSessionClient (Channel)
	// DialogSessionClient can be bridged with other sessions
	ready := make(chan struct{})
	go func() {
		ua, _ := sipgo.NewUA()
		phone := sipgox.NewPhone(ua, sipgox.WithPhoneListenAddr(
			sipgox.ListenAddr{
				Network: "udp",
				Addr:    "127.0.0.200:5090",
			},
		))

		ansCtx := context.WithValue(context.Background(), sipgox.AnswerReadyCtxKey, sipgox.AnswerReadyCtxValue(ready))

		dialog, err := phone.Answer(ansCtx, sipgox.AnswerOptions{})
		require.NoError(t, err)
		defer dialog.Close()

		buf := make([]byte, media.RTPBufSize)
		err = dialog.ReadRTP(buf, &rtp.Packet{})
		require.NoError(t, err)

		err = dialog.WriteRTP(&rtp.Packet{})
		require.NoError(t, err)

		dialog.Hangup(ctx)
	}()
	<-ready

	{
		ua, _ := sipgo.NewUA()
		phone := sipgox.NewPhone(ua, sipgox.WithPhoneListenAddr(
			sipgox.ListenAddr{
				Network: "udp",
				Addr:    "127.0.0.100:5090",
			},
		))
		dialog, err := phone.Dial(context.TODO(), sip.Uri{Host: "127.0.0.1", Port: 5090}, sipgox.DialOptions{})
		require.NoError(t, err)
		defer dialog.Close()

		err = dialog.WriteRTP(&rtp.Packet{})
		require.NoError(t, err)

		buf := make([]byte, media.RTPBufSize)
		err = dialog.ReadRTP(buf, &rtp.Packet{})
		require.NoError(t, err)

		time.Sleep(1 * time.Second)
		dialog.Hangup(ctx)
	}
}

func TestDialogClientReinvite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	{
		ua, _ := sipgo.NewUA(sipgo.WithUserAgent("server"))
		dg := NewDiago(ua, WithTransport(
			Transport{
				Transport: "udp",
				BindHost:  "127.0.0.1",
				BindPort:  15060,
			},
		))
		err := dg.ServeBackground(ctx, func(d *DialogServerSession) {
			t.Log("Call received")
			d.Answer()
			<-d.Context().Done()
		})
		require.NoError(t, err)
	}

	ua, _ := sipgo.NewUA()
	dg := NewDiago(ua)

	dialog, err := dg.Invite(ctx, sip.Uri{User: "dialer", Host: "127.0.0.1", Port: 15060}, InviteOptions{})
	require.NoError(t, err)

	err = dialog.ReInvite(ctx)
	require.NoError(t, err)

	dialog.Hangup(ctx)
}

func TestDialogServerReinvite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	{
		ua, _ := sipgo.NewUA(sipgo.WithUserAgent("server"))
		dg := NewDiago(ua, WithTransport(
			Transport{
				Transport: "udp",
				BindHost:  "127.0.0.1",
				BindPort:  15070,
			},
		))

		// Run listener to accepte reinvites, but it should not receive any request
		err := dg.ServeBackground(ctx, nil)
		require.NoError(t, err)

		go func() {
			dialog, err := dg.Invite(ctx, sip.Uri{User: "dialer", Host: "127.0.0.1", Port: 15060}, InviteOptions{})
			require.NoError(t, err)
			<-dialog.Context().Done()
		}()
	}

	ua, _ := sipgo.NewUA()
	dg := NewDiago(ua, WithTransport(
		Transport{
			Transport: "udp",
			BindHost:  "127.0.0.1",
			BindPort:  15060,
		},
	))

	waitDialog := make(chan *DialogServerSession)
	err := dg.ServeBackground(ctx, func(d *DialogServerSession) {
		t.Log("Call received")
		waitDialog <- d
		<-d.Context().Done()
	})
	require.NoError(t, err)
	d := <-waitDialog

	err = d.Answer()
	require.NoError(t, err)
	err = d.ReInvite(d.Context())
	require.NoError(t, err)

	d.Hangup(context.TODO())
}
