// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2026, Emir Aganovic

package diago

import (
	"bytes"
	"context"
	"net/netip"
	"testing"
	"time"

	"github.com/emiago/diago/media"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	websip "github.com/emiago/websip"
	"github.com/stretchr/testify/require"
)

func testDiagoWebsipPair(t *testing.T) (*DiagoWebsip, *DiagoWebsip) {
	return testDiagoWebsipPairOptions(t, nil, nil)
}

func testDiagoWebsipPairOptions(t *testing.T, clientOptions, serverOptions []DiagoWebsipOption) (*DiagoWebsip, *DiagoWebsip) {
	t.Helper()
	clientConn, serverConn := websip.NewMemoryConnPair()
	clientUA, err := websip.NewUA(clientConn, websip.WithIdentity(sip.Uri{
		Scheme: "sip",
		User:   "alice",
		Host:   "example.com",
	}))
	require.NoError(t, err)
	serverUA, err := websip.NewUA(serverConn)
	require.NoError(t, err)
	client, err := NewDiagoWebsip(clientUA, clientOptions...)
	require.NoError(t, err)
	server, err := NewDiagoWebsip(serverUA, serverOptions...)
	require.NoError(t, err)
	server.WebsipServer().OnRegister(func(tx *websip.RegisterTransaction) {
		require.NoError(t, tx.RespondStatus(sip.StatusOK, "OK"))
	})
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
	defer cancel()
	_, err = client.WebsipClient().Register(ctx)
	require.NoError(t, err)
	t.Cleanup(func() {
		_ = client.Close()
		_ = server.Close()
	})
	return client, server
}

func TestDiagoWebsipMediaConfigCodecs(t *testing.T) {
	mediaConfig := MediaConfig{Codecs: []media.Codec{media.CodecAudioAlaw}}
	client, server := testDiagoWebsipPairOptions(
		t,
		[]DiagoWebsipOption{WithWebsipMediaConfig(mediaConfig)},
		[]DiagoWebsipOption{WithWebsipMediaConfig(mediaConfig)},
	)
	webRTCConfig := testWebsipWebrtcConfig()
	type answerResult struct {
		media *DialogWebrtc
		err   error
	}
	serverMedia := make(chan answerResult, 1)
	server.OnDialog(func(dialog *DialogWebsipServerSession) {
		med, err := dialog.Answer(AnswerWebsipOptions{WebrtcConfig: webRTCConfig})
		serverMedia <- answerResult{media: med, err: err}
		if err == nil {
			<-dialog.Context().Done()
		}
	})

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	dialog, clientMedia, err := client.Invite(ctx, sip.Uri{Scheme: "sip", User: "bob", Host: "example.com"}, InviteWebsipOptions{
		WebrtcConfig: webRTCConfig,
	})
	require.NoError(t, err)
	var remoteResult answerResult
	select {
	case remoteResult = <-serverMedia:
	case <-ctx.Done():
		t.Fatal("server did not establish configured media")
	}
	require.NoError(t, remoteResult.err)
	require.Equal(t, []media.Codec{media.CodecAudioAlaw}, clientMedia.MediaSession().Codecs)
	require.Equal(t, []media.Codec{media.CodecAudioAlaw}, remoteResult.media.MediaSession().Codecs)
	require.NoError(t, dialog.Hangup(ctx))
}

func testWebsipWebrtcConfig() media.MediaSessionWebrtcConfig {
	loopbackOnly := func(addr netip.Addr) bool { return addr.IsLoopback() }
	return media.MediaSessionWebrtcConfig{
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
}

func TestDiagoWebsipInviteBidirectionalRTPAndRemoteBye(t *testing.T) {
	client, server := testDiagoWebsipPair(t)
	webRTCConfig := testWebsipWebrtcConfig()
	type serverResult struct {
		dialog *DialogWebsipServerSession
		media  *DialogWebrtc
		err    error
	}
	serverResultCh := make(chan serverResult, 1)
	server.OnDialog(func(dialog *DialogWebsipServerSession) {
		med, err := dialog.Answer(AnswerWebsipOptions{WebrtcConfig: webRTCConfig})
		serverResultCh <- serverResult{dialog: dialog, media: med, err: err}
		if err == nil {
			<-dialog.Context().Done()
		}
	})

	ctx, cancel := context.WithTimeout(t.Context(), 15*time.Second)
	defer cancel()
	clientDialog, err := client.NewDialog(sip.Uri{Scheme: "sip", User: "bob", Host: "example.com"})
	require.NoError(t, err)
	clientMedia, err := clientDialog.Invite(ctx, InviteWebsipOptions{WebrtcConfig: webRTCConfig})
	require.NoError(t, err)
	var result serverResult
	select {
	case result = <-serverResultCh:
	case <-ctx.Done():
		t.Fatal("server did not answer INVITE")
	}
	require.NoError(t, result.err)
	require.NotNil(t, result.media)

	require.NoError(t, clientMedia.MediaSession().StopRTP(1, 5*time.Second))
	require.NoError(t, result.media.MediaSession().StopRTP(1, 5*time.Second))
	clientWriter, err := clientMedia.AudioWriter()
	require.NoError(t, err)
	serverReader, err := result.media.AudioReader()
	require.NoError(t, err)
	payload := bytes.Repeat([]byte{0x31}, int(media.CodecAudioUlaw.SampleTimestamp()))
	_, err = clientWriter.Write(payload)
	require.NoError(t, err)
	received := make([]byte, media.RTPBufSize)
	n, err := serverReader.Read(received)
	require.NoError(t, err)
	require.Equal(t, payload, received[:n])

	serverWriter, err := result.media.AudioWriter()
	require.NoError(t, err)
	clientReader, err := clientMedia.AudioReader()
	require.NoError(t, err)
	payload = bytes.Repeat([]byte{0x73}, int(media.CodecAudioUlaw.SampleTimestamp()))
	_, err = serverWriter.Write(payload)
	require.NoError(t, err)
	n, err = clientReader.Read(received)
	require.NoError(t, err)
	require.Equal(t, payload, received[:n])

	require.NoError(t, result.dialog.Hangup(ctx))
	select {
	case <-clientDialog.Context().Done():
	case <-ctx.Done():
		t.Fatal("client dialog did not terminate after remote BYE")
	}
}

func TestDiagoWebsipCancelPendingInvite(t *testing.T) {
	client, server := testDiagoWebsipPair(t)
	serverDialog := make(chan *DialogWebsipServerSession, 1)
	server.OnDialog(func(dialog *DialogWebsipServerSession) {
		serverDialog <- dialog
		<-dialog.Context().Done()
	})
	clientDialog, err := client.NewDialog(sip.Uri{Scheme: "sip", User: "bob", Host: "example.com"})
	require.NoError(t, err)

	ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
	defer cancel()
	inviteResult := make(chan error, 1)
	go func() {
		_, err := clientDialog.Invite(ctx, InviteWebsipOptions{WebrtcConfig: testWebsipWebrtcConfig()})
		inviteResult <- err
	}()
	select {
	case <-serverDialog:
	case <-ctx.Done():
		t.Fatal("server did not receive pending INVITE")
	}
	require.NoError(t, clientDialog.Hangup(ctx))
	select {
	case err := <-inviteResult:
		var responseErr sipgo.ErrDialogResponse
		require.ErrorAs(t, err, &responseErr)
		require.Equal(t, sip.StatusRequestTerminated, responseErr.Res.StatusCode)
	case <-ctx.Done():
		t.Fatal("INVITE did not finish after CANCEL")
	}
	require.Equal(t, websip.DialogTerminated, clientDialog.State())
}

func TestNewDiagoWebsipRejectsMismatchedHandles(t *testing.T) {
	first, err := websip.NewUA(websip.NewWebSocketDialer())
	require.NoError(t, err)
	second, err := websip.NewUA(websip.NewWebSocketDialer())
	require.NoError(t, err)
	client, err := websip.NewClient(second)
	require.NoError(t, err)
	_, err = NewDiagoWebsip(first, WithWebsipClient(client))
	require.Error(t, err)
	require.NoError(t, first.Close())
	require.NoError(t, second.Close())
}
