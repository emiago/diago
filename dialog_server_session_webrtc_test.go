// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2026, Emir Aganovic

package diago

// Direct WebRTC server-dialog integration tests.

import (
	"bytes"
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/emiago/diago/media"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/pion/ice/v4"
	"github.com/stretchr/testify/require"
)

func TestIntegrationDialogWebrtcICEControlsBidirectionalRTP(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	loopbackOnly := func(addr netip.Addr) bool { return addr.IsLoopback() }
	webRTCConfig := media.MediaSessionWebrtcConfig{
		IPFamilies:      []media.ICEIPFamily{media.ICEIPFamilyIPv4},
		CandidateTypes:  []media.ICECandidateType{media.ICECandidateHost},
		IncludeLoopback: true,
		IPFilter:        loopbackOnly,
		RemoteIPFilter:  loopbackOnly,
		Timeouts: media.ICETimeouts{
			Disconnected: 2 * time.Second,
			Failed:       3 * time.Second,
			Keepalive:    500 * time.Millisecond,
		},
	}

	serverUA, err := sipgo.NewUA(sipgo.WithUserAgent("webrtc-ice-controls-server"))
	require.NoError(t, err)
	defer serverUA.Close()
	server := NewDiago(serverUA, WithTransport(Transport{
		Transport: "tcp",
		BindHost:  "127.0.0.1",
		BindPort:  0,
	}))

	serverMedia := make(chan *DialogWebrtc, 1)
	serverErr := make(chan error, 1)
	serverConnected := make(chan struct{}, 1)
	require.NoError(t, server.ServeBackground(ctx, func(dialog *DialogServerSession) {
		med, answerErr := dialog.AnswerWebrtc(AnswerWebrtcOptions{
			WebrtcConfig: webRTCConfig,
			OnICEStateChange: func(state ice.ConnectionState) {
				if state == ice.ConnectionStateConnected || state == ice.ConnectionStateCompleted {
					select {
					case serverConnected <- struct{}{}:
					default:
					}
				}
			},
		})
		if answerErr != nil {
			serverErr <- answerErr
			return
		}
		serverMedia <- med
		<-dialog.Context().Done()
	}))
	require.NotZero(t, server.transports[0].BindPort)

	clientUA, err := sipgo.NewUA(sipgo.WithUserAgent("webrtc-ice-controls-client"))
	require.NoError(t, err)
	defer clientUA.Close()
	client := NewDiago(clientUA, WithTransport(Transport{
		Transport: "tcp",
		BindHost:  "127.0.0.1",
		BindPort:  0,
	}))
	dialog, err := client.NewDialog(sip.Uri{
		User: "media",
		Host: "127.0.0.1",
		Port: server.transports[0].BindPort,
	}, NewDialogOptions{Transport: "tcp"})
	require.NoError(t, err)
	defer dialog.Close()

	clientConnected := make(chan struct{}, 1)
	inviteCtx, inviteCancel := context.WithTimeout(ctx, 10*time.Second)
	defer inviteCancel()
	clientMed, err := dialog.InviteWebrtc(inviteCtx, InviteWebrtcOptions{
		WebrtcConfig: webRTCConfig,
		OnICEStateChange: func(state ice.ConnectionState) {
			if state == ice.ConnectionStateConnected || state == ice.ConnectionStateCompleted {
				select {
				case clientConnected <- struct{}{}:
				default:
				}
			}
		},
	})
	require.NoError(t, err)
	defer clientMed.Close()

	var serverMed *DialogWebrtc
	select {
	case serverMed = <-serverMedia:
	case err = <-serverErr:
		t.Fatalf("server failed to answer controlled WebRTC call: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for controlled WebRTC server media")
	}
	defer serverMed.Close()

	select {
	case <-clientConnected:
	case <-time.After(time.Second):
		t.Fatal("client did not report a connected ICE state")
	}
	select {
	case <-serverConnected:
	case <-time.After(time.Second):
		t.Fatal("server did not report a connected ICE state")
	}

	require.NoError(t, clientMed.MediaSession().StopRTP(1, 5*time.Second))
	require.NoError(t, serverMed.MediaSession().StopRTP(1, 5*time.Second))

	clientWriter, err := clientMed.AudioWriter()
	require.NoError(t, err)
	serverReader, err := serverMed.AudioReader()
	require.NoError(t, err)
	clientPayload := bytes.Repeat([]byte{0x31}, int(media.CodecAudioUlaw.SampleTimestamp()))
	n, err := clientWriter.Write(clientPayload)
	require.NoError(t, err)
	require.Equal(t, len(clientPayload), n)
	received := make([]byte, media.RTPBufSize)
	n, err = serverReader.Read(received)
	require.NoError(t, err)
	require.Equal(t, clientPayload, received[:n])

	serverWriter, err := serverMed.AudioWriter()
	require.NoError(t, err)
	clientReader, err := clientMed.AudioReader()
	require.NoError(t, err)
	serverPayload := bytes.Repeat([]byte{0x73}, int(media.CodecAudioUlaw.SampleTimestamp()))
	n, err = serverWriter.Write(serverPayload)
	require.NoError(t, err)
	require.Equal(t, len(serverPayload), n)
	n, err = clientReader.Read(received)
	require.NoError(t, err)
	require.Equal(t, serverPayload, received[:n])

	require.Equal(t, uint64(1), clientMed.RTPSession().WriteStats().PacketsCount)
	require.Equal(t, uint64(1), clientMed.RTPSession().ReadStats().PacketsCount)
	require.Equal(t, uint64(1), serverMed.RTPSession().WriteStats().PacketsCount)
	require.Equal(t, uint64(1), serverMed.RTPSession().ReadStats().PacketsCount)
	require.NoError(t, dialog.Hangup(ctx))
}

func TestIntegrationDialogWebrtcServerReInviteKeepsMediaWrappers(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()

	loopbackOnly := func(addr netip.Addr) bool { return addr.IsLoopback() }
	webRTCConfig := media.MediaSessionWebrtcConfig{
		IPFamilies:      []media.ICEIPFamily{media.ICEIPFamilyIPv4},
		CandidateTypes:  []media.ICECandidateType{media.ICECandidateHost},
		IncludeLoopback: true,
		IPFilter:        loopbackOnly,
		RemoteIPFilter:  loopbackOnly,
	}

	serverUA, err := sipgo.NewUA(sipgo.WithUserAgent("webrtc-server-reinvite"))
	require.NoError(t, err)
	defer serverUA.Close()
	server := NewDiago(serverUA, WithTransport(Transport{
		Transport: "tcp", BindHost: "127.0.0.1", BindPort: 0,
	}))
	type serverCall struct {
		dialog *DialogServerSession
		media  *DialogWebrtc
	}
	serverCallReady := make(chan serverCall, 1)
	serverErr := make(chan error, 1)
	require.NoError(t, server.ServeBackground(ctx, func(dialog *DialogServerSession) {
		med, answerErr := dialog.AnswerWebrtc(AnswerWebrtcOptions{WebrtcConfig: webRTCConfig})
		if answerErr != nil {
			serverErr <- answerErr
			return
		}
		serverCallReady <- serverCall{dialog: dialog, media: med}
		<-dialog.Context().Done()
	}))

	clientUA, err := sipgo.NewUA(sipgo.WithUserAgent("webrtc-client-reinvite-peer"))
	require.NoError(t, err)
	defer clientUA.Close()
	client := NewDiago(clientUA, WithTransport(Transport{
		Transport: "tcp", BindHost: "127.0.0.1", BindPort: 0,
	}))
	dialog, err := client.NewDialog(sip.Uri{
		User: "media", Host: "127.0.0.1", Port: server.transports[0].BindPort,
	}, NewDialogOptions{Transport: "tcp"})
	require.NoError(t, err)
	defer dialog.Close()

	callCtx, callCancel := context.WithTimeout(ctx, 15*time.Second)
	defer callCancel()
	clientMed, err := dialog.InviteWebrtc(callCtx, InviteWebrtcOptions{WebrtcConfig: webRTCConfig})
	require.NoError(t, err)
	defer clientMed.Close()

	var call serverCall
	select {
	case call = <-serverCallReady:
	case err = <-serverErr:
		t.Fatalf("server failed to answer direct WebRTC call: %v", err)
	case <-callCtx.Done():
		t.Fatal("timed out waiting for direct WebRTC server media")
	}
	defer call.media.Close()

	clientReader := clientMed.RTPPacketReader
	clientWriter := clientMed.RTPPacketWriter
	serverReader := call.media.RTPPacketReader
	serverWriter := call.media.RTPPacketWriter
	oldClientMedia := clientMed.MediaSession()
	oldServerMedia := call.media.MediaSession()

	require.NoError(t, call.dialog.ReInvite(callCtx))
	require.Eventually(t, func() bool {
		return clientMed.MediaSession() != oldClientMedia
	}, time.Second, time.Millisecond, "client did not commit the re-INVITE on ACK")
	require.NotSame(t, oldClientMedia, clientMed.MediaSession())
	require.NotSame(t, oldServerMedia, call.media.MediaSession())
	require.Same(t, clientReader, clientMed.RTPPacketReader)
	require.Same(t, clientWriter, clientMed.RTPPacketWriter)
	require.Same(t, serverReader, call.media.RTPPacketReader)
	require.Same(t, serverWriter, call.media.RTPPacketWriter)

	require.NoError(t, clientMed.MediaSession().StopRTP(1, 5*time.Second))
	require.NoError(t, call.media.MediaSession().StopRTP(1, 5*time.Second))
	serverPayload := bytes.Repeat([]byte{0x62}, int(media.CodecAudioUlaw.SampleTimestamp()))
	n, err := serverWriter.Write(serverPayload)
	require.NoError(t, err)
	require.Equal(t, len(serverPayload), n)
	received := make([]byte, media.RTPBufSize)
	n, err = clientReader.Read(received)
	require.NoError(t, err)
	require.Equal(t, serverPayload, received[:n])

	clientPayload := bytes.Repeat([]byte{0x26}, int(media.CodecAudioUlaw.SampleTimestamp()))
	n, err = clientWriter.Write(clientPayload)
	require.NoError(t, err)
	require.Equal(t, len(clientPayload), n)
	n, err = serverReader.Read(received)
	require.NoError(t, err)
	require.Equal(t, clientPayload, received[:n])
	require.NoError(t, dialog.Hangup(ctx))
}
