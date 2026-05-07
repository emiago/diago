package diago

import (
	"context"
	"fmt"
	"net"
	"testing"

	"github.com/emiago/diago/media"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/pion/webrtc/v3"
	"github.com/stretchr/testify/require"
)

func TestWebrtcOutgoing(t *testing.T) {
	webrtcAPI, err := newWebrtcAPI([]net.IP{net.IPv4(127, 0, 0, 1)})
	require.NoError(t, err)
	{
		ua, _ := sipgo.NewUA()
		dg := NewDiago(ua,
			WithTransport(Transport{
				Transport:      "tcp",
				BindHost:       "127.0.0.1",
				BindPort:       15060,
				RewriteContact: true,
			}),
		)
		err := dg.ServeBackground(context.TODO(), func(d *DialogServerSession) {
			err := func() error {
				peerConnection, err := webrtcAPI.NewPeerConnection(defaultWebrtcConfig)
				if err != nil {
					return err
				}

				sd := webrtc.SessionDescription{
					Type: webrtc.SDPTypeOffer,
					SDP:  string(d.InviteRequest.Body()),
				}
				if err = peerConnection.SetRemoteDescription(sd); err != nil {
					panic(err)
				}

				answer, err := peerConnection.CreateAnswer(nil)
				if err != nil {
					return fmt.Errorf("failed to create answer: %w", err)
				}

				// Create channel that is blocked until ICE Gathering is complete
				gatherComplete := webrtc.GatheringCompletePromise(peerConnection)

				// Sets the LocalDescription, and sta
				if err = peerConnection.SetLocalDescription(answer); err != nil {
					return err
				}

				<-gatherComplete

				// localSDP := peerConnection.LocalDescription().SDP
				// if err := d.RespondSDP([]byte(localSDP)); err != nil {
				// 	return err
				// }
				mess := &media.MediaSession{}
				if err := mess.InitWithSDP([]byte(answer.SDP)); err != nil {
					return err
				}

				d.InitMediaSession(mess, nil, nil)
				if err := d.Answer(); err != nil {
					return err
				}

				<-d.Context().Done()
				return nil
				// return d.Hangup(d.Context())
			}()
			if err != nil {
				panic(err)
			}
		})
		require.NoError(t, err)
	}

	ua, _ := sipgo.NewUA()
	dg := NewDiago(ua, WithTransport(
		Transport{
			Transport:      "tcp",
			BindHost:       "127.0.0.1",
			BindPort:       15061,
			RewriteContact: true,
		},
	))

	dialog, err := dg.NewDialog(sip.Uri{User: "123", Host: "127.0.0.1", Port: 15060}, NewDialogOptions{Transport: "tcp"})
	require.NoError(t, err)

	m, err := dialog.InviteWebrtc(context.TODO(), InviteWebrtcOptions{})
	require.NoError(t, err)
	defer m.Close()

	dialog.Hangup(context.TODO())
}
