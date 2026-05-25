package diago

import (
	"bytes"
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/emiago/diago/audio"
	"github.com/emiago/diago/media"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/pion/webrtc/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func newWebrtcDialer(ua *sipgo.UserAgent) *Diago {
	return NewDiago(ua, WithTransport(Transport{Transport: "tcp", BindHost: "127.0.0.1", BindPort: 0}))
}

func TestDialogClientSessionWebrtc(t *testing.T) {
	ctx := t.Context()

	// Create transaction users, as many as needed.
	ua, _ := sipgo.NewUA(
		sipgo.WithUserAgent("inbound"),
	)
	defer ua.Close()

	dg := NewDiago(ua,
		WithTransport(
			Transport{
				Transport: "tcp",
				BindHost:  "127.0.0.1",
				BindPort:  15060,
			},
		),
	)

	err := dg.ServeBackground(ctx, func(d *DialogServerSession) {
		// t.Log("Call received", d.InviteRequest)
		// Add some routing
		if d.ToUser() == "echo" {
			d.Trying()
			d.Ringing()

			med, err := d.AnswerWebrtc(AnswerWebrtcOptions{
				WebrtcConfig: webrtc.Configuration{},
			})
			if err != nil {
				t.Log("Failed to answer webrtc", "error", err)
				return
			}
			defer med.Close()

			if err := med.Echo(); err != nil {
				t.Log("Failed to echo", "error", err)
				return
			}
			<-d.Context().Done()
			return
		}

		if d.ToUser() == "hanguper" {
			d.Trying()
			med, err := d.AnswerWebrtc(AnswerWebrtcOptions{})
			if err != nil {
				t.Log("Failed to asnwer webrtc", "error", err)
				return
			}
			defer med.Close()
			d.Hangup(d.Context())
			return
		}

		d.Respond(sip.StatusForbidden, "Forbidden", nil)

		<-d.Context().Done()
	})
	require.NoError(t, err)

	t.Run("HanguperClientNoServe", func(t *testing.T) {
		// We want to confirm that diago can receive BYE without Binding to IP, which will reflect Contact Header
		ua, _ := sipgo.NewUA()
		defer ua.Close()

		// Has no listener just UAC. Contact will hold empheral port
		phone := newWebrtcDialer(ua)

		dialog, err := phone.NewDialog(sip.Uri{User: "hanguper", Host: "127.0.0.1", Port: 15060}, NewDialogOptions{
			Transport: "tcp",
		})
		require.NoError(t, err)

		// Hanguped
		med, err := dialog.InviteWebrtc(context.TODO(), InviteWebrtcOptions{})
		require.NoError(t, err)
		defer med.Close()
		<-dialog.Context().Done()
	})

	/* 	t.Run("HanguperClientWithServe", func(t *testing.T) {
		// We want to confirm that diago can receive BYE on Binded IP
		ua, _ := sipgo.NewUA()
		defer ua.Close()

		phone := newDialer(ua)
		// listening but stil with empheral port
		err := phone.ServeBackground(context.TODO(), func(d *DialogServerSession) {})
		require.NoError(t, err)

		ports := phone.server.TransportLayer().ListenPorts("udp")
		require.Len(t, ports, 1)
		// Hanguped
		dialog, err := phone.Invite(context.TODO(), sip.Uri{User: "hanguper", Host: "127.0.0.1", Port: 5060}, InviteOptions{})
		require.NoError(t, err)
		<-dialog.Context().Done()
		assert.Equal(t, dialog.InviteRequest.Via().Port, dialog.InviteRequest.Contact().Address.Port)
	}) */

	t.Run("Dialer", func(t *testing.T) {
		ua, _ := sipgo.NewUA()
		defer ua.Close()

		phone := newWebrtcDialer(ua)

		/* // Forbiddden
		_, err = phone.Invite(context.TODO(), sip.Uri{User: "noroute", Host: "127.0.0.1", Port: 5060}, InviteOptions{})
		require.Error(t, err)

		// Hanguped
		dialog, err := phone.Invite(context.TODO(), sip.Uri{User: "hanguper", Host: "127.0.0.1", Port: 5060}, InviteOptions{})
		require.NoError(t, err)
		<-dialog.Context().Done() */

		// Answered call
		dialog, err := phone.NewDialog(sip.Uri{User: "echo", Host: "127.0.0.1", Port: 15060}, NewDialogOptions{
			Transport: "tcp",
		})
		require.NoError(t, err)
		defer dialog.Close()

		med, err := dialog.InviteWebrtc(context.TODO(), InviteWebrtcOptions{})
		require.NoError(t, err)
		defer med.Close()

		audioReaderProps := MediaProps{}
		audioR, err := med.AudioReader(WithAudioReaderWebrtcProps(&audioReaderProps))
		require.NoError(t, err)
		assert.Equal(t, media.CodecAudioUlaw, audioReaderProps.Codec)

		audioWriterProps := MediaProps{}
		audioW, err := med.AudioWriter(WithAudioWriterWebrtcProps(&audioWriterProps))
		require.NoError(t, err)
		assert.Equal(t, media.CodecAudioUlaw, audioWriterProps.Codec)

		// Confirm media traveling
		writeN, _ := audioW.Write([]byte("my audio"))
		readN, _ := audioR.Read(make([]byte, 100))
		assert.Equal(t, writeN, readN, "media echo failed")
		dialog.Hangup(ctx)
	})

}

func TestIntegrationDialogWebrtcClientReinviteMedia(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	beep, _ := audio.BeepLoadPCM(media.CodecAudioUlaw)
	numPkts := len(beep) / media.CodecAudioUlaw.Samples16()

	t.Log("Size beep", len(beep), numPkts)
	audioReceived := make(chan []byte)
	{
		ua, _ := sipgo.NewUA(sipgo.WithUserAgent("server"))
		defer ua.Close()

		dg := NewDiago(ua, WithTransport(
			Transport{
				Transport: "tcp",
				BindHost:  "127.0.0.1",
				BindPort:  15079,
			},
		))
		digServer := NewDigestServer()
		err := dg.ServeBackground(ctx, func(d *DialogServerSession) {
			t.Log("New INVITE")
			if err := digServer.AuthorizeDialog(d, DigestAuth{
				Username: "test",
				Password: "test",
			}); err != nil {
				return
			}

			med, err := d.AnswerWebrtc(AnswerWebrtcOptions{})
			if err != nil {
				t.Log("Failed to answer webrtc", "error", err)
				return
			}
			defer med.Close()

			// ar, _ := d.AudioReader()
			ar := med.RTPPacketReader
			ctx, cancel := context.WithCancel(context.Background())
			go func() {
				defer cancel()
				beepEncoded, _ := media.ReadAll(ar, 160)
				audioReceived <- beepEncoded
			}()

			time.Sleep(60 * time.Millisecond)
			require.NotNil(t, med.peerConnection)
			require.NotNil(t, med.peerConnection.CurrentLocalDescription())
			ld := med.peerConnection.CurrentLocalDescription()
			lsdp, err := ld.Unmarshal()
			require.NoError(t, err)
			lsdp.MediaDescriptions[0].ConnectionInformation.Address.Address = "127.0.0.2"
			newSDP, _ := lsdp.Marshal()

			// we do not care for now with peer connection and media
			_, err = d.reInviteSDP(d.Context(), newSDP)
			require.NoError(t, err)

			<-ctx.Done()
		})
		require.NoError(t, err)
	}

	ua, _ := sipgo.NewUA()
	defer ua.Close()

	dg := newWebrtcDialer(ua)
	// err := dg.ServeBackground(context.TODO(), func(d *DialogServerSession) {})
	// require.NoError(t, err)
	dialog, err := dg.NewDialog(sip.Uri{User: "dialer", Host: "127.0.0.1", Port: 15079}, NewDialogOptions{Transport: "tcp"})
	require.NoError(t, err)
	defer dialog.Close()

	med, err := dialog.InviteWebrtc(ctx, InviteWebrtcOptions{
		OnMediaUpdate: func(d *DialogWebrtc) {
			fmt.Println("Media update", d.peerConnection.RemoteDescription().SDP)
		},
		Username: "test",
		Password: "test",
	})
	defer med.Close()
	require.NoError(t, err)
	err = dialog.Ack(ctx)
	require.NoError(t, err)

	require.NoError(t, err)
	pb, _ := med.PlaybackCreate()
	_, err = pb.Play(bytes.NewBuffer(beep), "audio/pcm")
	require.NoError(t, err)

	err = dialog.Hangup(ctx)
	require.NoError(t, err)
	// remoteAudio := <-audioReceived

	// 1 packet will not be consumed due to update of RTP packets
	// assert.GreaterOrEqual(t, len(remoteAudio)/160, numPkts-1)
}
