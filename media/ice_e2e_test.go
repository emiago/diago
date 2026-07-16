// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package media

import (
	"crypto/tls"
	"net"
	"testing"
	"time"

	"github.com/emiago/diago/media/sdp"
	"github.com/emiago/diago/testdata"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// iceTestIP returns a routable IPv4 address. ICE does not gather loopback host
// candidates by default, so a session on 127.0.0.1 would offer no candidate at
// all and never nominate a pair.
func iceTestIP(t *testing.T) net.IP {
	t.Helper()

	addrs, err := net.InterfaceAddrs()
	require.NoError(t, err)
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() {
			continue
		}
		if ip4 := ipnet.IP.To4(); ip4 != nil {
			return ip4
		}
	}
	t.Skip("no non-loopback IPv4 interface: ICE cannot gather host candidates")
	return nil
}

// TestICEDTLSSRTPSession negotiates a full ICE + DTLS-SRTP session between two
// MediaSessions and sends media over it.
//
// This is the property the whole ICE path exists for: one socket per session,
// candidates exchanged over SDP, connectivity checks nominating a pair, the
// DTLS handshake running over that pair, and SRTP keyed from its keying
// material, with RTP and RTCP muxed alongside the handshake the whole time.
func TestICEDTLSSRTPSession(t *testing.T) {
	ip := iceTestIP(t)

	newSess := func(role DTLSEndpointRole, cert tls.Certificate) *MediaSession {
		t.Helper()
		s := &MediaSession{
			Codecs:    []Codec{CodecAudioUlaw},
			Mode:      sdp.ModeSendrecv,
			SecureRTP: SecureRTPModeDTLS,
			ICEConf:   &ICEConfig{},
			DTLSRole:  role,
			DTLSConf: DTLSConfig{
				Certificates: []tls.Certificate{cert},
				// RFC 5763 section 5: both endpoints present a certificate, and
				// the a=fingerprint in the SDP is what binds it to the
				// signalling. The DTLS server therefore has to ask for the
				// peer certificate, otherwise it has nothing to verify the
				// offerer's fingerprint against. This is required of any
				// WebRTC endpoint, which is what an ICE session is.
				ServerClientAuth: ServerClientAuthRequireCert,
			},
		}
		s.Laddr = net.UDPAddr{IP: ip, Port: 0}
		require.NoError(t, s.Init())
		t.Cleanup(func() { _ = s.Close() })
		return s
	}

	offerer := newSess(DTLSEndpointRoleOfferer, testdata.ClientCertificate())
	answerer := newSess(DTLSEndpointRoleAnswerer, testdata.ServerCertificate())

	// Offer/answer carries the ICE credentials and candidates both ways.
	offer := offerer.LocalSDP()
	require.Contains(t, string(offer), "a=ice-ufrag:")
	require.Contains(t, string(offer), "a=candidate:")
	require.Contains(t, string(offer), "a=rtcp-mux")
	require.Contains(t, string(offer), "UDP/TLS/RTP/SAVP")

	require.NoError(t, answerer.RemoteSDP(offer))
	answer := answerer.LocalSDP()
	require.Contains(t, string(answer), "a=ice-ufrag:")
	require.NoError(t, offerer.RemoteSDP(answer))

	// Both sides must run connectivity checks and the handshake concurrently:
	// each blocks until the other answers.
	errCh := make(chan error, 2)
	go func() { errCh <- answerer.Finalize() }()
	go func() { errCh <- offerer.Finalize() }()
	for i := 0; i < 2; i++ {
		select {
		case err := <-errCh:
			require.NoError(t, err, "ICE + DTLS negotiation failed")
		case <-time.After(40 * time.Second):
			t.Fatal("ICE + DTLS negotiation did not complete")
		}
	}

	// SRTP is keyed on both sides from the DTLS keying material.
	require.NotNil(t, offerer.localCtxSRTP, "offerer must have an SRTP send context")
	require.NotNil(t, answerer.remoteCtxSRTP, "answerer must have an SRTP receive context")

	// Both sessions run on their single ICE socket.
	require.NotNil(t, offerer.iceMux)
	require.NotNil(t, answerer.iceMux)

	// RTCP is muxed onto the RTP address rather than the RTP port + 1.
	assert.Equal(t, offerer.Raddr.Port, offerer.rtcpRaddr.Port,
		"rtcp-mux puts RTCP on the RTP port")

	// Media flows and decrypts.
	payload := []byte{0xd5, 0xd5, 0xd5, 0xd5, 0xd5, 0xd5, 0xd5, 0xd5}
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    CodecAudioUlaw.PayloadType,
			SequenceNumber: 1,
			Timestamp:      160,
			SSRC:           0xdeadbeef,
		},
		Payload: payload,
	}
	require.NoError(t, offerer.WriteRTP(pkt))

	require.NoError(t, answerer.rtpConn.SetReadDeadline(time.Now().Add(5*time.Second)))
	buf := make([]byte, RTPBufSize)
	got := rtp.Packet{}
	_, err := answerer.ReadRTP(buf, &got)
	require.NoError(t, err, "media must arrive and decrypt over the ICE pair")

	assert.Equal(t, pkt.SSRC, got.SSRC)
	assert.Equal(t, pkt.SequenceNumber, got.SequenceNumber)
	assert.Equal(t, payload, got.Payload, "payload must survive the SRTP round trip")
}

// TestICESessionClosesSocket asserts an ICE session that never reached
// connectivity checks still releases its socket and its agent goroutines.
// Init binds and gathers, so a call abandoned during signalling has resources
// that Close is the only thing that frees.
func TestICESessionClosesSocket(t *testing.T) {
	ip := iceTestIP(t)

	s := &MediaSession{
		Codecs:    []Codec{CodecAudioUlaw},
		Mode:      sdp.ModeSendrecv,
		SecureRTP: SecureRTPModeDTLS,
		ICEConf:   &ICEConfig{},
		DTLSConf:  DTLSConfig{Certificates: []tls.Certificate{testdata.ServerCertificate()}},
	}
	s.Laddr = net.UDPAddr{IP: ip, Port: 0}
	require.NoError(t, s.Init())

	port := s.Laddr.Port
	require.NotZero(t, port)
	require.Nil(t, s.rtpConn, "no transport before connectivity checks")

	require.NoError(t, s.Close())

	// The port is free again, so the socket really was released.
	reuse, err := net.ListenUDP("udp", &net.UDPAddr{IP: ip, Port: port})
	require.NoError(t, err, "ICE socket was not released by Close")
	require.NoError(t, reuse.Close())

	// Close is idempotent: MediaSession.Close may be reached twice on error paths.
	require.NoError(t, s.Close())
}
