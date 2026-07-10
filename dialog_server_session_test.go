// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/emiago/diago/media"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationDialogServerEarlyMedia(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var dialer *Diago
	{
		ua, _ := sipgo.NewUA(sipgo.WithUserAgent("server"))
		defer ua.Close()

		dg := NewDiago(ua, WithTransport(
			Transport{
				Transport: "udp",
				BindHost:  "127.0.0.1",
				BindPort:  15020,
			},
		))

		// Run listener to accepte reinvites, but it should not receive any request
		err := dg.ServeBackground(ctx, nil)
		require.NoError(t, err)

		dialer = dg
	}

	ua, _ := sipgo.NewUA()
	defer ua.Close()

	dg := NewDiago(ua, WithTransport(
		Transport{
			Transport: "udp",
			BindHost:  "127.0.0.1",
			BindPort:  15010,
		},
	))

	waitDialog := make(chan *DialogServerSession)
	err := dg.ServeBackground(ctx, func(d *DialogServerSession) {
		t.Log("Call received")
		waitDialog <- d
		<-d.Context().Done()
	})
	require.NoError(t, err)

	allResponses := []sip.Response{}
	wg := sync.WaitGroup{}
	wg.Add(1)
	go func() {
		defer wg.Done()
		dialog, err := dialer.Invite(ctx, sip.Uri{User: "dialer", Host: "127.0.0.1", Port: 15010}, InviteOptions{
			OnResponse: func(res *sip.Response) error {
				t.Log("Received resp", res.StatusCode)
				allResponses = append(allResponses, *res.Clone())
				return nil
			},
		})
		if err != nil {
			t.Log("Failed to dial", err)
			return
		}
		defer dialog.Close()
		<-dialog.Context().Done()
		t.Log("Dialog done")
	}()

	d := <-waitDialog

	err = d.ProgressMedia()
	require.NoError(t, err)

	// It is valid to also send 180
	time.Sleep(500 * time.Millisecond)
	require.NoError(t, d.Ringing())

	// We can play some file ringtone
	playback, err := d.PlaybackCreate()
	require.NoError(t, err)
	_, err = playback.PlayFile("testdata/files/demo-echodone.wav")
	require.NoError(t, err)

	// We can now answer
	err = d.Answer()
	require.NoError(t, err)

	// New playback is needed to follow new media session
	playback, err = d.PlaybackCreate()
	require.NoError(t, err)
	_, err = playback.PlayFile("testdata/files/demo-echodone.wav")
	require.NoError(t, err)
	d.Hangup(context.TODO())

	wg.Wait()
	require.Len(t, allResponses, 3)
	assert.Equal(t, 183, allResponses[0].StatusCode)
	assert.Equal(t, 180, allResponses[1].StatusCode)
	assert.Equal(t, 200, allResponses[2].StatusCode)
}

func TestIntegrationDialogServerReinvite(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	{
		ua, _ := sipgo.NewUA(sipgo.WithUserAgent("server"))
		defer ua.Close()

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
			t.Log("Dialog done")
		}()
	}

	ua, _ := sipgo.NewUA()
	defer ua.Close()

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

func TestIntegrationDialogServerRefer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	var dialer *Diago
	{
		ua, _ := sipgo.NewUA(sipgo.WithUserAgent("dialer"))
		defer ua.Close()

		dg := NewDiago(ua, WithTransport(
			Transport{
				Transport: "udp",
				BindHost:  "127.0.0.1",
				BindPort:  15071,
				ID:        "udp",
			},
		))

		// Run listener to accepte reinvites, but it should not receive any request
		err := dg.ServeBackground(ctx, nil)
		require.NoError(t, err)
		dialer = dg
	}

	dialCall := func() {
		dialog, err := dialer.NewDialog(sip.Uri{User: "dialer", Host: "127.0.0.1", Port: 15070}, NewDialogOptions{})
		require.NoError(t, err)

		go func() {
			err := dialog.Invite(ctx, InviteClientOptions{
				OnRefer: func(referDialog *DialogClientSession) error {
					// referDialog.
					if err := referDialog.Invite(ctx, InviteClientOptions{}); err != nil {
						return err
					}
					if err := referDialog.Ack(ctx); err != nil {
						return err
					}

					return referDialog.Hangup(ctx)
				},
			})
			require.NoError(t, err)

			dialog.Ack(ctx)
			<-dialog.Context().Done()
			t.Log("Dialog done")
		}()
	}

	// UAS that accepts REFER
	// waitReferDialog := make(chan *DialogServerSession)
	{
		ua, _ := sipgo.NewUA()
		defer ua.Close()

		dg := NewDiago(ua, WithTransport(
			Transport{
				Transport: "udp",
				BindHost:  "127.0.0.1",
				BindPort:  15072,
			},
		))

		err := dg.ServeBackground(ctx, func(d *DialogServerSession) {
			t.Log("Call INVITE due to REFER received")
			// waitReferDialog <- d
			switch d.ToUser() {
			case "busy":
				d.Respond(sip.StatusBusyHere, "Busy Here", nil)
				return
			case "noanswer":
				d.Ringing()
				return
			default:
				d.Answer()
			}

			<-d.Context().Done()
		})
		require.NoError(t, err)
	}

	ua, _ := sipgo.NewUA()
	defer ua.Close()

	dg := NewDiago(ua, WithTransport(
		Transport{
			Transport: "udp",
			BindHost:  "127.0.0.1",
			BindPort:  15070,
		},
	))

	waitDialog := make(chan *DialogServerSession)
	err := dg.ServeBackground(ctx, func(d *DialogServerSession) {
		t.Log("Call received")
		waitDialog <- d
		<-d.Context().Done()
	})
	require.NoError(t, err)

	t.Run("Successfull", func(t *testing.T) {
		dialCall()
		d := <-waitDialog
		defer d.Hangup(ctx)

		err = d.Answer()
		require.NoError(t, err)

		referState := make(chan int)
		err = d.ReferOptions(d.Context(), sip.Uri{Host: "127.0.0.1", Port: 15072}, ReferServerOptions{
			OnNotify: func(statusCode int) {
				referState <- statusCode
			},
		})
		require.NoError(t, err)

		assert.Equal(t, 100, <-referState)
		assert.Equal(t, 200, <-referState)
	})

	t.Run("UnreachableRefer", func(t *testing.T) {
		dialCall()
		d := <-waitDialog
		defer d.Hangup(ctx)

		err = d.Answer()
		require.NoError(t, err)

		referState := make(chan int)
		err = d.ReferOptions(d.Context(), sip.Uri{User: "noanswer", Host: "127.0.0.1", Port: 15072}, ReferServerOptions{
			OnNotify: func(statusCode int) {
				referState <- statusCode
			},
		})
		require.NoError(t, err)

		assert.Equal(t, 100, <-referState)
		assert.Equal(t, sip.StatusTemporarilyUnavailable, <-referState)
	})

	t.Run("BusyRefer", func(t *testing.T) {
		dialCall()
		d := <-waitDialog
		defer d.Hangup(ctx)

		err = d.Answer()
		require.NoError(t, err)

		referState := make(chan int)
		err = d.ReferOptions(d.Context(), sip.Uri{User: "busy", Host: "127.0.0.1", Port: 15072}, ReferServerOptions{
			OnNotify: func(statusCode int) {
				referState <- statusCode
			},
		})
		require.NoError(t, err)

		assert.Equal(t, 100, <-referState)
		assert.Equal(t, sip.StatusBusyHere, <-referState)
	})
}

func TestIntegrationDialogServerReferRejection(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Dialer WITHOUT OnRefer handler — will reject REFER with 406
	var dialer *Diago
	{
		ua, _ := sipgo.NewUA(sipgo.WithUserAgent("dialer-norefer"))
		defer ua.Close()

		dg := NewDiago(ua, WithTransport(
			Transport{
				Transport: "udp",
				BindHost:  "127.0.0.1",
				BindPort:  15081,
				ID:        "udp",
			},
		))

		err := dg.ServeBackground(ctx, nil)
		require.NoError(t, err)
		dialer = dg
	}

	dialCall := func() {
		dialog, err := dialer.NewDialog(sip.Uri{User: "dialer", Host: "127.0.0.1", Port: 15080}, NewDialogOptions{})
		require.NoError(t, err)

		go func() {
			// No OnRefer set — REFER will be rejected by dialer
			err := dialog.Invite(ctx, InviteClientOptions{})
			require.NoError(t, err)

			dialog.Ack(ctx)
			<-dialog.Context().Done()
			t.Log("Dialog done")
		}()
	}

	ua, _ := sipgo.NewUA()
	defer ua.Close()

	dg := NewDiago(ua, WithTransport(
		Transport{
			Transport: "udp",
			BindHost:  "127.0.0.1",
			BindPort:  15080,
		},
	))

	waitDialog := make(chan *DialogServerSession)
	err := dg.ServeBackground(ctx, func(d *DialogServerSession) {
		t.Log("Call received")
		waitDialog <- d
		<-d.Context().Done()
	})
	require.NoError(t, err)

	t.Run("ReferRejected405_AutoBYE", func(t *testing.T) {
		dialCall()
		d := <-waitDialog

		err = d.Answer()
		require.NoError(t, err)

		// Send REFER without OnNotify — auto-BYE mode
		// Dialer has no OnRefer handler, so it responds 406 Not Acceptable
		err = d.ReferOptions(d.Context(), sip.Uri{Host: "127.0.0.1", Port: 15082}, ReferServerOptions{})
		// REFER should return error (non-202 response)
		assert.Error(t, err)

		// Dialog should be terminated (BYE sent automatically)
		select {
		case <-d.Context().Done():
			// Good — dialog ended due to auto-BYE
		case <-time.After(5 * time.Second):
			t.Fatal("Dialog did not terminate after REFER rejection — BYE was not sent")
		}
	})

	t.Run("ReferRejected_WithOnNotify_NoBYE", func(t *testing.T) {
		dialCall()
		d := <-waitDialog

		err = d.Answer()
		require.NoError(t, err)

		// Send REFER WITH OnNotify — caller manages lifecycle
		err = d.ReferOptions(d.Context(), sip.Uri{Host: "127.0.0.1", Port: 15082}, ReferServerOptions{
			OnNotify: func(statusCode int) {
				// Caller handles it
			},
		})
		// REFER should return error (non-202 response)
		assert.Error(t, err)

		// Dialog should NOT be auto-terminated — caller owns lifecycle
		select {
		case <-d.Context().Done():
			t.Fatal("Dialog was terminated but OnNotify was set — should not auto-BYE")
		case <-time.After(1 * time.Second):
			// Good — dialog still alive, caller has control
		}

		// Clean up
		d.Hangup(ctx)
	})
}

func TestIntegrationDialogServerReferTimeout(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Dialer with OnRefer that accepts but never sends final NOTIFY (simulates lost NOTIFY)
	var dialer *Diago
	{
		ua, _ := sipgo.NewUA(sipgo.WithUserAgent("dialer-timeout"))
		defer ua.Close()

		dg := NewDiago(ua, WithTransport(
			Transport{
				Transport: "udp",
				BindHost:  "127.0.0.1",
				BindPort:  15091,
				ID:        "udp",
			},
		))

		err := dg.ServeBackground(ctx, nil)
		require.NoError(t, err)
		dialer = dg
	}

	// UAS that accepts the REFER'd INVITE but never finishes (simulates hang)
	{
		ua, _ := sipgo.NewUA()
		defer ua.Close()

		dg := NewDiago(ua, WithTransport(
			Transport{
				Transport: "udp",
				BindHost:  "127.0.0.1",
				BindPort:  15092,
			},
		))

		err := dg.ServeBackground(ctx, func(d *DialogServerSession) {
			// Accept call but just hang — never complete to trigger final NOTIFY
			d.Ringing()
			<-d.Context().Done()
		})
		require.NoError(t, err)
	}

	ua, _ := sipgo.NewUA()
	defer ua.Close()

	dg := NewDiago(ua, WithTransport(
		Transport{
			Transport: "udp",
			BindHost:  "127.0.0.1",
			BindPort:  15090,
		},
	))

	waitDialog := make(chan *DialogServerSession)
	err := dg.ServeBackground(ctx, func(d *DialogServerSession) {
		t.Log("Call received")
		waitDialog <- d
		<-d.Context().Done()
	})
	require.NoError(t, err)

	t.Run("NotifyTimeout_AutoBYE", func(t *testing.T) {
		dialCall := func() {
			dialog, err := dialer.NewDialog(sip.Uri{User: "dialer", Host: "127.0.0.1", Port: 15090}, NewDialogOptions{})
			require.NoError(t, err)

			go func() {
				err := dialog.Invite(ctx, InviteClientOptions{
					OnRefer: func(referDialog *DialogClientSession) error {
						// Accept REFER but send INVITE to a target that never answers
						if err := referDialog.Invite(ctx, InviteClientOptions{}); err != nil {
							return err
						}
						// Never send final NOTIFY >= 200 (simulates lost/stuck scenario)
						<-referDialog.Context().Done()
						return nil
					},
				})
				require.NoError(t, err)
				dialog.Ack(ctx)
				<-dialog.Context().Done()
			}()
		}

		dialCall()
		d := <-waitDialog

		err = d.Answer()
		require.NoError(t, err)

		// Send REFER with short timeout — no OnNotify (auto-BYE mode)
		err = d.ReferOptions(d.Context(), sip.Uri{Host: "127.0.0.1", Port: 15092}, ReferServerOptions{
			ReferTimeout: 3 * time.Second, // Short timeout for test
		})
		require.NoError(t, err)

		// Dialog should terminate within timeout + margin
		select {
		case <-d.Context().Done():
			// Good — dialog ended due to timeout BYE
		case <-time.After(10 * time.Second):
			t.Fatal("Dialog did not terminate after NOTIFY timeout — BYE was not sent")
		}
	})
}

func TestIntegrationDialogServerPlayback(t *testing.T) {
	rtpBuf := newRTPWriterBuffer()
	dialog := &DialogServerSession{
		DialogMedia: DialogMedia{
			mediaSession:    &media.MediaSession{Codecs: []media.Codec{media.CodecAudioUlaw}},
			RTPPacketWriter: media.NewRTPPacketWriter(rtpBuf, media.CodecAudioUlaw),
		},
	}

	playback, err := dialog.PlaybackCreate()
	require.NoError(t, err)

	initTS := dialog.RTPPacketWriter.InitTimestamp()
	_, err = playback.PlayFile("testdata/files/demo-echodone.wav")
	require.NoError(t, err)
	diffTS := dialog.RTPPacketWriter.PacketHeader.Timestamp - initTS
	assert.Greater(t, diffTS, uint32(1000))

	time.Sleep(100 * time.Millisecond) // 4 frames
	initTS = dialog.RTPPacketWriter.InitTimestamp()
	_, err = playback.PlayFile("testdata/files/demo-echodone.wav")
	require.NoError(t, err)
	diffTS2 := dialog.RTPPacketWriter.PacketHeader.Timestamp - initTS
	t.Log(initTS, diffTS2)

	// Timestamp should be offset more than previous diff by Sleep
	assert.Greater(t, diffTS2, diffTS+5*media.CodecAudioUlaw.SampleTimestamp())
}
