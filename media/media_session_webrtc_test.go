// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2026, Emir Aganovic

package media

import (
	"context"
	"crypto/tls"
	"strings"
	"testing"
	"time"

	"github.com/emiago/diago/testdata"
	"github.com/pion/ice/v4"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v3"
	"github.com/stretchr/testify/require"
)

func TestMediaSessionWebrtcICEAndSRTP(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	newSession := func(certConfig DTLSConfig) *MediaSessionWebrtc {
		return &MediaSessionWebrtc{
			Codecs: []Codec{CodecAudioUlaw, CodecAudioAlaw},
			Mode:   "sendrecv",
			Config: MediaSessionWebrtcConfig{DTLS: certConfig},
		}
	}
	offerer := newSession(DTLSConfig{Certificates: []tls.Certificate{testdata.ClientCertificate()}})
	answerer := newSession(DTLSConfig{Certificates: []tls.Certificate{testdata.ServerCertificate()}})
	defer offerer.Close()
	defer answerer.Close()

	offerConfig := offerer.Config
	offerConfig.NetworkTypes = []ice.NetworkType{ice.NetworkTypeUDP4}
	offerConfig.IncludeLoopback = true
	offerConfig.InterfaceFilter = testLoopbackInterface
	answerConfig := answerer.Config
	answerConfig.NetworkTypes = []ice.NetworkType{ice.NetworkTypeUDP4}
	answerConfig.IncludeLoopback = true
	answerConfig.InterfaceFilter = testLoopbackInterface
	require.NoError(t, offerer.Init(ctx, offerConfig))
	require.NoError(t, answerer.Init(ctx, answerConfig))

	offer, err := offerer.LocalSDP(ctx, false)
	require.NoError(t, err)
	require.Contains(t, string(offer), "a=rtcp-mux\r\n")
	require.Contains(t, string(offer), "a=setup:actpass\r\n")
	require.Contains(t, string(offer), "a=end-of-candidates\r\n")
	require.NotContains(t, string(offer), "a=ice-options:trickle")

	require.NoError(t, answerer.RemoteSDP(ctx, offer, false))
	answer, err := answerer.LocalSDP(ctx, true)
	require.NoError(t, err)
	require.Contains(t, string(answer), "a=setup:active\r\n")
	require.NoError(t, offerer.RemoteSDP(ctx, answer, true))
	offerRTPSession := NewRTPSessionWebrtc(offerer)
	answerRTPSession := NewRTPSessionWebrtc(answerer)
	offerWriter := NewRTPPacketWriter(offerRTPSession, offerer.Codec())
	answerReader := NewRTPPacketReader(answerRTPSession, answerer.Codec())

	finalized := make(chan error, 2)
	go func() { finalized <- offerer.Finalize(ctx) }()
	go func() { finalized <- answerer.Finalize(ctx) }()
	require.NoError(t, <-finalized)
	require.NoError(t, <-finalized)
	require.NotEmpty(t, offerer.Laddr)
	require.NotEmpty(t, offerer.Raddr)

	offerRTPSession.RTCPReportInterval = 25 * time.Millisecond
	answerRTPSession.RTCPReportInterval = 25 * time.Millisecond
	receiverReports := make(chan *rtcp.ReceiverReport, 1)
	senderReports := make(chan *rtcp.SenderReport, 1)
	offerRTPSession.OnReadRTCP(func(pkt rtcp.Packet, _ RTPReadStats) {
		if report, ok := pkt.(*rtcp.ReceiverReport); ok {
			select {
			case receiverReports <- report:
			default:
			}
		}
	})
	answerRTPSession.OnReadRTCP(func(pkt rtcp.Packet, _ RTPReadStats) {
		if report, ok := pkt.(*rtcp.SenderReport); ok {
			select {
			case senderReports <- report:
			default:
			}
		}
	})
	require.NoError(t, offerRTPSession.MonitorBackground())
	require.NoError(t, answerRTPSession.MonitorBackground())
	defer offerRTPSession.MonitorClose()
	defer answerRTPSession.MonitorClose()

	payload := []byte("audio-over-ice-dtls-srtp")
	_, err = offerWriter.WriteSamples(payload, CodecAudioUlaw.SampleTimestamp(), true, CodecAudioUlaw.PayloadType)
	require.NoError(t, err)
	require.NoError(t, answerer.StopRTP(1, 2*time.Second))
	readBuf := make([]byte, 160)
	n, err := answerReader.Read(readBuf)
	require.NoError(t, err)
	require.Equal(t, payload, readBuf[:n])
	require.Equal(t, uint64(1), offerRTPSession.WriteStats().PacketsCount)
	require.Equal(t, uint64(1), answerRTPSession.ReadStats().PacketsCount)

	select {
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	case report := <-receiverReports:
		require.Len(t, report.Reports, 1)
		require.Equal(t, offerWriter.SSRC, report.Reports[0].SSRC)
	}
	select {
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	case report := <-senderReports:
		require.Equal(t, offerWriter.SSRC, report.SSRC)
		require.Equal(t, uint32(1), report.PacketCount)
		require.Equal(t, uint32(len(payload)), report.OctetCount)
	}
}

func TestMediaSessionWebrtcPionBrowserPeer(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// A Pion PeerConnection exercises the same offer/answer, ICE, DTLS-SRTP
	// and track-facing behavior a browser uses, while remaining deterministic
	// enough for a package integration test.
	mediaEngine := &webrtc.MediaEngine{}
	require.NoError(t, mediaEngine.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMU, ClockRate: 8000, Channels: 1},
		PayloadType:        0,
	}, webrtc.RTPCodecTypeAudio))
	settingEngine := webrtc.SettingEngine{}
	settingEngine.SetNetworkTypes([]webrtc.NetworkType{webrtc.NetworkTypeUDP4})
	settingEngine.SetIncludeLoopbackCandidate(true)
	settingEngine.SetInterfaceFilter(testLoopbackInterface)
	api := webrtc.NewAPI(webrtc.WithMediaEngine(mediaEngine), webrtc.WithSettingEngine(settingEngine))
	peer, err := api.NewPeerConnection(webrtc.Configuration{})
	require.NoError(t, err)
	defer peer.Close()

	localTrack, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMU, ClockRate: 8000, Channels: 1},
		"audio", "browser",
	)
	require.NoError(t, err)
	sender, err := peer.AddTrack(localTrack)
	require.NoError(t, err)
	go func() {
		buf := make([]byte, 1500)
		for {
			if _, _, readErr := sender.Read(buf); readErr != nil {
				return
			}
		}
	}()
	remoteTrack := make(chan *webrtc.TrackRemote, 1)
	peer.OnTrack(func(track *webrtc.TrackRemote, _ *webrtc.RTPReceiver) {
		remoteTrack <- track
	})

	offer, err := peer.CreateOffer(nil)
	require.NoError(t, err)
	gathered := webrtc.GatheringCompletePromise(peer)
	require.NoError(t, peer.SetLocalDescription(offer))
	select {
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	case <-gathered:
	}

	session := &MediaSessionWebrtc{Codecs: []Codec{CodecAudioUlaw}, Mode: "sendrecv"}
	defer session.Close()
	conf := MediaSessionWebrtcConfig{
		NetworkTypes:    []ice.NetworkType{ice.NetworkTypeUDP4},
		IncludeLoopback: true,
		InterfaceFilter: testLoopbackInterface,
		DTLS:            DTLSConfig{Certificates: []tls.Certificate{testdata.ServerCertificate()}},
	}
	require.NoError(t, session.Init(ctx, conf))
	require.NoError(t, session.RemoteSDP(ctx, []byte(peer.LocalDescription().SDP), false))
	answer, err := session.LocalSDP(ctx, true)
	require.NoError(t, err)
	require.NoError(t, peer.SetRemoteDescription(webrtc.SessionDescription{Type: webrtc.SDPTypeAnswer, SDP: string(answer)}))
	rtpSession := NewRTPSessionWebrtc(session)
	packetWriter := NewRTPPacketWriter(rtpSession, session.Codec())
	require.NoError(t, session.Finalize(ctx))

	payload := []byte("browser-compatible-audio")
	_, err = packetWriter.WriteSamples(payload, CodecAudioUlaw.SampleTimestamp(), true, CodecAudioUlaw.PayloadType)
	require.NoError(t, err)
	select {
	case <-ctx.Done():
		t.Fatal(ctx.Err())
	case track := <-remoteTrack:
		pkt, _, readErr := track.ReadRTP()
		require.NoError(t, readErr)
		require.Equal(t, payload, pkt.Payload)
	}
}

func testLoopbackInterface(name string) bool {
	return name == "lo"
}

func TestMediaSessionWebrtcRejectsTrickleOnlySDP(t *testing.T) {
	// This is a signalling-policy test, not an ICE implementation limit. SIP
	// has nowhere to deliver later candidates until an explicit trickle-over-SIP
	// extension is designed, so an offer without candidates must fail clearly.
	sdpBody := strings.ReplaceAll(`v=0
o=- 1 1 IN IP4 127.0.0.1
s=-
t=0 0
m=audio 9 UDP/TLS/RTP/SAVPF 0
c=IN IP4 0.0.0.0
a=mid:0
a=rtcp-mux
a=sendrecv
a=ice-ufrag:test
a=ice-pwd:testpasswordtestpassword
a=fingerprint:sha-256 00:11
a=setup:actpass
a=rtpmap:0 PCMU/8000
`, "\n", "\r\n")

	session := &MediaSessionWebrtc{
		Codecs: []Codec{CodecAudioUlaw},
		agent:  &ice.Agent{}, // parsing reaches candidate validation before agent use
	}
	err := session.RemoteSDP(context.Background(), []byte(sdpBody), false)
	require.ErrorContains(t, err, "trickle ICE is not supported")
}
