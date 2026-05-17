package diago

import (
	"context"
	"testing"

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
