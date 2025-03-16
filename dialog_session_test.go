// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"context"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/emiago/diago/media"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newDialer(ua *sipgo.UserAgent) *Diago {
	return NewDiago(ua, WithTransport(Transport{Transport: "udp", BindHost: "127.0.0.1", BindPort: 0}))
}

func dialogEcho(sess DialogSession) error {
	audioR, err := sess.Media().AudioReader()
	if err != nil {
		return err
	}

	audioW, err := sess.Media().AudioWriter()
	if err != nil {
		return err
	}

	_, err = media.Copy(audioR, audioW)
	if err != nil {
		return err
	}
	return nil
}

func TestIntegrationInbound(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Create transaction users, as many as needed.
	ua, _ := sipgo.NewUA(
		sipgo.WithUserAgent("inbound"),
	)
	defer ua.Close()

	dg := NewDiago(ua)

	err := dg.ServeBackground(ctx, func(d *DialogServerSession) {
		// Add some routing
		if d.ToUser() == "alice" {
			d.Progress()
			d.Ringing()
			d.Answer()

			dialogEcho(d)
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
		defer ua.Close()

		phone := newDialer(ua)
		// Start listener in order to reuse UDP listener
		err := phone.ServeBackground(context.TODO(), func(d *DialogServerSession) {})
		require.NoError(t, err)

		// Forbiddden
		_, err = phone.Invite(context.TODO(), sip.Uri{User: "noroute", Host: "127.0.0.1", Port: 5060}, InviteOptions{})
		require.Error(t, err)

		// Answered call
		dialog, err := phone.Invite(context.TODO(), sip.Uri{User: "alice", Host: "127.0.0.1", Port: 5060}, InviteOptions{})
		require.NoError(t, err)
		defer dialog.Close()

		// Confirm media traveling
		audioR, err := dialog.AudioReader()
		require.NoError(t, err)

		audioW, err := dialog.AudioWriter()
		require.NoError(t, err)

		writeN, _ := audioW.Write([]byte("my audio"))
		readN, _ := audioR.Read(make([]byte, 100))
		assert.Equal(t, writeN, readN, "media echo failed")
		dialog.Hangup(ctx)
	}
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

func TestIntegrationDialogCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ua, _ := sipgo.NewUA()
	defer ua.Close()
	port := 15000 + rand.IntN(999)
	dg := NewDiago(ua, WithTransport(
		Transport{
			Transport: "udp",
			BindHost:  "127.0.0.1",
			BindPort:  port,
		},
	))

	dg.ServeBackground(ctx, func(d *DialogServerSession) {
		ctx := d.Context()
		d.Progress()
		d.Ringing()

		<-ctx.Done()
	})

	{
		ua, _ := sipgo.NewUA()
		defer ua.Close()

		dg := newDialer(ua)
		dg.ServeBackground(context.TODO(), func(d *DialogServerSession) {})

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		_, err := dg.Invite(ctx, sip.Uri{User: "test", Host: "127.0.0.1", Port: port}, InviteOptions{
			OnResponse: func(res *sip.Response) error {
				if res.StatusCode == sip.StatusRinging {
					cancel()
					// return context.Canceled
				}
				return nil
			},
		})
		require.ErrorIs(t, err, context.Canceled)
	}

}
