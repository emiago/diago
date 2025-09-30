// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"bytes"
	"context"
	"testing"

	"github.com/emiago/diago/media"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationDialogClientEarlyMedia(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	{
		ua, _ := sipgo.NewUA(sipgo.WithUserAgent("server"))
		defer ua.Close()

		dg := NewDiago(ua, WithTransport(
			Transport{
				Transport: "udp",
				BindHost:  "127.0.0.1",
				BindPort:  15060,
			},
		))
		err := dg.ServeBackground(ctx, func(d *DialogServerSession) {
			t.Log("Call received")
			d.Trying()
			if err := d.ProgressMedia(); err != nil {
				t.Log("Failed to progress media", err)
				return
			}

			// Write frame
			w, _ := d.AudioWriter()
			if _, err := w.Write(bytes.Repeat([]byte{0, 100}, 80)); err != nil {
				t.Log("Failed to write frame", err)
				return
			}

			if err := d.Answer(); err != nil {
				t.Log("Failed to answer", err)
				return
			}
			return
		})
		require.NoError(t, err)
	}

	ua, _ := sipgo.NewUA()
	defer ua.Close()

	dg := newDialer(ua)
	err := dg.ServeBackground(context.TODO(), func(d *DialogServerSession) {})
	require.NoError(t, err)

	dialog, err := dg.NewDialog(sip.Uri{User: "dialer", Host: "127.0.0.1", Port: 15060}, NewDialogOptions{})
	require.NoError(t, err)
	defer dialog.Close()

	err = dialog.Invite(ctx, InviteClientOptions{
		EarlyMediaDetect: true,
	})
	require.ErrorIs(t, err, ErrClientEarlyMedia)

	// Now we should be able to read media
	r, err := dialog.AudioReader()
	require.NoError(t, err)

	// Read early media in background
	var earlyMediaBuf []byte
	doneEarly := make(chan struct{})
	go func() {
		defer close(doneEarly)
		earlyMediaBuf, _ = media.ReadAll(r, 160)
	}()

	dialog.WaitAnswer(ctx, sipgo.AnswerOptions{})
	dialog.Ack(ctx)

	<-dialog.Context().Done()
	<-doneEarly
	assert.Len(t, earlyMediaBuf, 160) // 1 frame
}

func TestIntegrationDialogClientReinvite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	{
		ua, _ := sipgo.NewUA(sipgo.WithUserAgent("server"))
		defer ua.Close()

		dg := NewDiago(ua, WithTransport(
			Transport{
				Transport: "udp",
				BindHost:  "127.0.0.1",
				BindPort:  15060,
			},
		))
		err := dg.ServeBackground(ctx, func(d *DialogServerSession) {
			t.Log("Call received")
			d.AnswerOptions(AnswerOptions{OnMediaUpdate: func(d *DialogMedia) {

			}})
			<-d.Context().Done()
		})
		require.NoError(t, err)
	}

	ua, _ := sipgo.NewUA()
	defer ua.Close()

	dg := newDialer(ua)
	err := dg.ServeBackground(context.TODO(), func(d *DialogServerSession) {})
	require.NoError(t, err)

	dialog, err := dg.Invite(ctx, sip.Uri{User: "dialer", Host: "127.0.0.1", Port: 15060}, InviteOptions{})
	require.NoError(t, err)

	err = dialog.ReInvite(ctx)
	require.NoError(t, err)

	dialog.Hangup(ctx)
}

func TestDialogClientInviteFailed(t *testing.T) {
	reqCh := make(chan *sip.Request)
	dg := testDiagoClient(t, func(req *sip.Request) *sip.Response {
		reqCh <- req
		return sip.NewResponseFromRequest(req, 500, "", nil)
	})

	t.Run("WithCallerid", func(t *testing.T) {
		opts := InviteClientOptions{}
		opts.WithCaller("Test", "123456", "example.com")
		dialog, err := dg.NewDialog(sip.Uri{User: "alice", Host: "localhost"}, NewDialogOptions{})
		require.NoError(t, err)
		go dialog.Invite(context.Background(), opts)
		req := <-reqCh
		assert.Equal(t, "Test", req.From().DisplayName)
		assert.Equal(t, "123456", req.From().Address.User)
		assert.NotEmpty(t, req.From().Params["tag"])
	})

	t.Run("WithAnonymous", func(t *testing.T) {
		opts := InviteClientOptions{}
		opts.WithAnonymousCaller()
		dialog, err := dg.NewDialog(sip.Uri{User: "alice", Host: "localhost"}, NewDialogOptions{})
		require.NoError(t, err)
		go dialog.Invite(context.Background(), opts)
		req := <-reqCh
		assert.Equal(t, "Anonymous", req.From().DisplayName)
		assert.Equal(t, "anonymous", req.From().Address.User)
		assert.NotEmpty(t, req.From().Params["tag"])
	})
}

func TestIntegrationDialogClientBadMediaNegotiation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	requests := []sip.Message{}
	responses := []sip.Message{}

	{
		ua, _ := sipgo.NewUA(sipgo.WithUserAgent("server"))
		defer ua.Close()
		ua.TransportLayer().OnMessage(func(msg sip.Message) {
			requests = append(requests, msg)
		})

		dg := NewDiago(ua, WithTransport(
			Transport{
				Transport: "udp",
				BindHost:  "127.0.0.1",
				BindPort:  15060,
			},
		),
		)

		err := dg.ServeBackground(ctx, func(d *DialogServerSession) {
			t.Log("Call received")
			if err := d.Answer(); err != nil {
				t.Log("Error on answer", err)
				return
			}
			<-d.Context().Done()
		})
		require.NoError(t, err)
	}

	ua, _ := sipgo.NewUA()
	defer ua.Close()

	ua.TransportLayer().OnMessage(func(msg sip.Message) {
		responses = append(responses, msg)
	})

	dg := newDialer(ua)
	err := dg.ServeBackground(context.TODO(), func(d *DialogServerSession) {})
	require.NoError(t, err)

	// Media negotiaton should fail and call should be terminated
	_, err = dg.Invite(ctx, sip.Uri{User: "dialer", Host: "127.0.0.1", Port: 15060}, InviteOptions{
		OnResponse: func(res *sip.Response) error {
			// Fake Bad SDP
			res.SetBody([]byte("Bad SDP"))
			return nil
		},
	})
	t.Log(err)
	require.Error(t, err)
	require.Len(t, requests, 3)
	require.Len(t, responses, 2)

	// Termination of dialog should be this correct
	assert.EqualValues(t, "INVITE", requests[0].(*sip.Request).Method)
	assert.EqualValues(t, 200, responses[0].(*sip.Response).StatusCode)
	assert.EqualValues(t, "ACK", requests[1].(*sip.Request).Method)
	assert.EqualValues(t, "BYE", requests[2].(*sip.Request).Method)
	assert.EqualValues(t, 200, responses[1].(*sip.Response).StatusCode)
}
