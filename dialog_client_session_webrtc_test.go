// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2026, Emir Aganovic

package diago

// Direct WebRTC client-dialog integration tests.

import (
	"bytes"
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/emiago/diago/media"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/stretchr/testify/require"
)

func TestIntegrationDialogWebrtcBidirectionalRTP(t *testing.T) {
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

	serverUA, err := sipgo.NewUA(sipgo.WithUserAgent("voip-webrtc-server"))
	require.NoError(t, err)
	defer serverUA.Close()
	server := NewDiago(serverUA, WithTransport(Transport{
		Transport: "tcp",
		BindHost:  "127.0.0.1",
		BindPort:  0,
	}))

	serverMedia := make(chan *DialogWebrtc, 1)
	serverErr := make(chan error, 1)
	require.NoError(t, server.ServeBackground(ctx, func(dialog *DialogServerSession) {
		med, answerErr := dialog.AnswerWebrtc(AnswerWebrtcOptions{
			WebrtcConfig: webRTCConfig,
		})
		if answerErr != nil {
			serverErr <- answerErr
			return
		}
		serverMedia <- med
		<-dialog.Context().Done()
	}))
	require.NotZero(t, server.transports[0].BindPort)

	clientUA, err := sipgo.NewUA(sipgo.WithUserAgent("voip-webrtc-client"))
	require.NoError(t, err)
	defer clientUA.Close()
	client := NewDiago(clientUA, WithTransport(Transport{
		Transport: "tcp",
		BindHost:  "127.0.0.1",
		BindPort:  0,
	},
	))
	dialog, err := client.NewDialog(sip.Uri{
		User: "media",
		Host: "127.0.0.1",
		Port: server.transports[0].BindPort,
	}, NewDialogOptions{Transport: "tcp"})
	require.NoError(t, err)
	defer dialog.Close()

	inviteCtx, inviteCancel := context.WithTimeout(ctx, 10*time.Second)
	defer inviteCancel()
	clientMed, err := dialog.InviteWebrtc(inviteCtx, InviteWebrtcOptions{
		WebrtcConfig: webRTCConfig,
	})
	require.NoError(t, err)
	defer clientMed.Close()

	var serverMed *DialogWebrtc
	select {
	case serverMed = <-serverMedia:
	case err = <-serverErr:
		t.Fatalf("server failed to answer direct WebRTC call: %v", err)
	case <-time.After(10 * time.Second):
		t.Fatal("timed out waiting for direct WebRTC server media")
	}
	defer serverMed.Close()

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

func TestIntegrationDialogWebrtcClientReInviteKeepsMediaWrappers(t *testing.T) {
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

	serverUA, err := sipgo.NewUA(sipgo.WithUserAgent("voip-webrtc-reinvite-server"))
	require.NoError(t, err)
	defer serverUA.Close()
	server := NewDiago(serverUA, WithTransport(Transport{
		Transport: "tcp",
		BindHost:  "127.0.0.1",
		BindPort:  0,
	}))
	serverMedia := make(chan *DialogWebrtc, 1)
	serverErr := make(chan error, 1)
	require.NoError(t, server.ServeBackground(ctx, func(dialog *DialogServerSession) {
		med, answerErr := dialog.AnswerWebrtc(AnswerWebrtcOptions{WebrtcConfig: webRTCConfig})
		if answerErr != nil {
			serverErr <- answerErr
			return
		}
		serverMedia <- med
		<-dialog.Context().Done()
	}))

	clientUA, err := sipgo.NewUA(sipgo.WithUserAgent("voip-webrtc-reinvite-client"))
	require.NoError(t, err)
	defer clientUA.Close()
	client := NewDiago(clientUA, WithTransport(Transport{
		Transport: "tcp",
		BindHost:  "127.0.0.1",
		BindPort:  0,
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

	var serverMed *DialogWebrtc
	select {
	case serverMed = <-serverMedia:
	case err = <-serverErr:
		t.Fatalf("server failed to answer direct WebRTC call: %v", err)
	case <-callCtx.Done():
		t.Fatal("timed out waiting for direct WebRTC server media")
	}
	defer serverMed.Close()

	clientReader := clientMed.RTPPacketReader
	clientWriter := clientMed.RTPPacketWriter
	serverReader := serverMed.RTPPacketReader
	serverWriter := serverMed.RTPPacketWriter
	oldClientMedia := clientMed.MediaSession()
	oldServerMedia := serverMed.MediaSession()
	oldClientRTP := clientMed.RTPSession()
	oldServerRTP := serverMed.RTPSession()
	require.NoError(t, oldClientMedia.StopRTP(1, 5*time.Second))
	require.NoError(t, oldServerMedia.StopRTP(1, 5*time.Second))
	preClientPayload := bytes.Repeat([]byte{0x35}, int(media.CodecAudioUlaw.SampleTimestamp()))
	n, err := clientWriter.Write(preClientPayload)
	require.NoError(t, err)
	require.Equal(t, len(preClientPayload), n)
	preReceived := make([]byte, media.RTPBufSize)
	n, err = serverReader.Read(preReceived)
	require.NoError(t, err)
	require.Equal(t, preClientPayload, preReceived[:n])
	preServerPayload := bytes.Repeat([]byte{0x53}, int(media.CodecAudioUlaw.SampleTimestamp()))
	n, err = serverWriter.Write(preServerPayload)
	require.NoError(t, err)
	require.Equal(t, len(preServerPayload), n)
	n, err = clientReader.Read(preReceived)
	require.NoError(t, err)
	require.Equal(t, preServerPayload, preReceived[:n])
	require.Equal(t, uint64(1), oldClientRTP.WriteStats().PacketsCount)
	require.Equal(t, uint64(1), oldClientRTP.ReadStats().PacketsCount)
	type readResult struct {
		payload []byte
		err     error
	}
	blockedRead := make(chan readResult, 1)
	readStarted := make(chan struct{})
	go func() {
		close(readStarted)
		buf := make([]byte, media.RTPBufSize)
		n, readErr := serverReader.Read(buf)
		blockedRead <- readResult{payload: buf[:n], err: readErr}
	}()
	<-readStarted
	time.Sleep(10 * time.Millisecond)

	require.NoError(t, dialog.ReInvite(callCtx))
	require.Eventually(t, func() bool {
		return serverMed.MediaSession() != oldServerMedia
	}, time.Second, time.Millisecond, "server did not commit the re-INVITE on ACK")
	require.NotSame(t, oldClientMedia, clientMed.MediaSession())
	require.NotSame(t, oldServerMedia, serverMed.MediaSession())
	require.NotSame(t, oldClientRTP, clientMed.RTPSession())
	require.NotSame(t, oldServerRTP, serverMed.RTPSession())
	require.Same(t, clientReader, clientMed.RTPPacketReader)
	require.Same(t, clientWriter, clientMed.RTPPacketWriter)
	require.Same(t, serverReader, serverMed.RTPPacketReader)
	require.Same(t, serverWriter, serverMed.RTPPacketWriter)

	require.NoError(t, clientMed.MediaSession().StopRTP(1, 5*time.Second))
	require.NoError(t, serverMed.MediaSession().StopRTP(1, 5*time.Second))
	clientPayload := bytes.Repeat([]byte{0x45}, int(media.CodecAudioUlaw.SampleTimestamp()))
	n, err = clientWriter.Write(clientPayload)
	require.NoError(t, err)
	require.Equal(t, len(clientPayload), n)
	select {
	case result := <-blockedRead:
		require.NoError(t, result.err)
		require.Equal(t, clientPayload, result.payload)
	case <-callCtx.Done():
		t.Fatal("RTP read blocked on the replaced WebRTC transport")
	}

	serverPayload := bytes.Repeat([]byte{0x54}, int(media.CodecAudioUlaw.SampleTimestamp()))
	n, err = serverWriter.Write(serverPayload)
	require.NoError(t, err)
	require.Equal(t, len(serverPayload), n)
	received := make([]byte, media.RTPBufSize)
	n, err = clientReader.Read(received)
	require.NoError(t, err)
	require.Equal(t, serverPayload, received[:n])
	require.Equal(t, uint64(2), clientMed.RTPSession().WriteStats().PacketsCount)
	require.Equal(t, uint64(2), clientMed.RTPSession().ReadStats().PacketsCount)
	require.NoError(t, dialog.Hangup(ctx))
}

func TestIntegrationDialogWebrtcEarlyMedia(t *testing.T) {
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

	serverUA, err := sipgo.NewUA(sipgo.WithUserAgent("voip-webrtc-early-media-server"))
	require.NoError(t, err)
	defer serverUA.Close()
	server := NewDiago(serverUA, WithTransport(Transport{
		Transport: "tcp",
		BindHost:  "127.0.0.1",
		BindPort:  0,
	}))

	allowAnswer := make(chan struct{})
	serverAnswered := make(chan struct{})
	serverErr := make(chan error, 1)
	reportServerErr := func(err error) {
		select {
		case serverErr <- err:
		default:
		}
	}
	require.NoError(t, server.ServeBackground(ctx, func(dialog *DialogServerSession) {
		conf, confErr := prepareWebrtcConfig(webRTCConfig)
		if confErr != nil {
			reportServerErr(confErr)
			return
		}
		sess := &media.MediaSessionWebrtc{Codecs: dialog.mediaConf.Codecs}
		if initErr := sess.Init(dialog.Context(), conf); initErr != nil {
			reportServerErr(initErr)
			return
		}
		med := &DialogWebrtc{}
		defer med.Close()
		if remoteErr := sess.RemoteSDP(dialog.Context(), dialog.InviteRequest.Body(), false); remoteErr != nil {
			reportServerErr(remoteErr)
			return
		}
		localSDP, localErr := sess.LocalSDP(dialog.Context(), true)
		if localErr != nil {
			reportServerErr(localErr)
			return
		}
		if responseErr := dialog.DialogServerSession.Respond(
			sip.StatusSessionInProgress,
			"Session Progress",
			localSDP,
			sip.NewHeader("Content-Type", "application/sdp"),
		); responseErr != nil {
			reportServerErr(responseErr)
			return
		}
		if finalizeErr := sess.Finalize(dialog.Context()); finalizeErr != nil {
			reportServerErr(finalizeErr)
			return
		}
		rtpSess := media.NewRTPSessionWebrtc(sess)
		med.Init(sess, rtpSess)
		if monitorErr := rtpSess.MonitorBackground(); monitorErr != nil {
			reportServerErr(monitorErr)
			return
		}
		writer, writerErr := med.AudioWriter()
		if writerErr != nil {
			reportServerErr(writerErr)
			return
		}
		payload := bytes.Repeat([]byte{0x45}, int(media.CodecAudioUlaw.SampleTimestamp()))
		if _, writeErr := writer.Write(payload); writeErr != nil {
			reportServerErr(writeErr)
			return
		}

		select {
		case <-allowAnswer:
		case <-dialog.Context().Done():
			return
		}
		if responseErr := dialog.RespondSDP(localSDP); responseErr != nil {
			reportServerErr(responseErr)
			return
		}
		close(serverAnswered)
		<-dialog.Context().Done()
	}))

	clientUA, err := sipgo.NewUA(sipgo.WithUserAgent("voip-webrtc-early-media-client"))
	require.NoError(t, err)
	defer clientUA.Close()
	client := NewDiago(clientUA, WithTransport(Transport{
		Transport: "tcp",
		BindHost:  "127.0.0.1",
		BindPort:  0,
	}))
	dialog, err := client.NewDialog(sip.Uri{
		User: "early-media",
		Host: "127.0.0.1",
		Port: server.transports[0].BindPort,
	}, NewDialogOptions{Transport: "tcp"})
	require.NoError(t, err)
	defer dialog.Close()

	inviteCtx, inviteCancel := context.WithTimeout(ctx, 10*time.Second)
	defer inviteCancel()
	clientMed, err := dialog.InviteWebrtc(inviteCtx, InviteWebrtcOptions{
		EarlyMediaDetect: true,
		WebrtcConfig:     webRTCConfig,
	})
	require.ErrorIs(t, err, ErrClientEarlyMedia)
	require.NotNil(t, clientMed)
	defer clientMed.Close()

	reader, err := clientMed.AudioReader()
	require.NoError(t, err)
	received := make([]byte, media.RTPBufSize)
	n, err := reader.Read(received)
	require.NoError(t, err)
	require.Equal(t, bytes.Repeat([]byte{0x45}, int(media.CodecAudioUlaw.SampleTimestamp())), received[:n])

	close(allowAnswer)
	require.NoError(t, dialog.WaitAnswerWebrtc(inviteCtx, clientMed, sipgo.AnswerOptions{}))
	select {
	case <-serverAnswered:
	case err = <-serverErr:
		t.Fatalf("server failed to complete direct WebRTC answer: %v", err)
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for direct WebRTC server to receive ACK")
	}
	require.NoError(t, dialog.Hangup(ctx))

	select {
	case err = <-serverErr:
		t.Fatalf("server failed during direct WebRTC early media: %v", err)
	default:
	}
}
