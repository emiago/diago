// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package media

import (
	"net"
	"strings"
	"testing"

	"github.com/emiago/diago/media/sdp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newICEBindSession builds a session far enough to exercise the bind step
// only. It deliberately does not run Init, so no ICE agent is created and no
// candidate is gathered: these are bind side invariants.
func newICEBindSession(t *testing.T) *MediaSession {
	t.Helper()
	s := &MediaSession{
		Codecs:    []Codec{CodecAudioUlaw},
		Mode:      sdp.ModeSendrecv,
		SecureRTP: SecureRTPModeDTLS,
		ICEConf:   &ICEConfig{},
	}
	s.Laddr = net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0}
	return s
}

// TestICEBindsSingleSocket pins the single socket layout. ICE nominates one
// candidate pair, so a second RTCP socket would be bound for nothing and its
// port would never appear in any SDP.
func TestICEBindsSingleSocket(t *testing.T) {
	s := newICEBindSession(t)
	require.True(t, s.iceEnabled())

	require.NoError(t, s.createListeners(&s.Laddr))
	t.Cleanup(func() {
		if s.iceUDPConn != nil {
			_ = s.iceUDPConn.Close()
		}
	})

	require.NotNil(t, s.iceUDPConn, "ICE session must bind its socket")
	assert.Nil(t, s.rtpConn, "ICE session must not bind a separate RTP socket")
	assert.Nil(t, s.rtcpConn, "ICE session must not bind a separate RTCP socket")
	assert.True(t, s.rtcpMux, "ICE forces rtcp-mux")

	// The SDP m=audio port is taken from Laddr, and the ICE agent gathers its
	// host candidate from this socket. They must be the same port or the offer
	// advertises a port nothing listens on.
	bound := s.iceUDPConn.LocalAddr().(*net.UDPAddr)
	assert.Equal(t, bound.Port, s.Laddr.Port)
	assert.NotZero(t, s.Laddr.Port, "ephemeral port must be resolved onto Laddr")
}

// TestNonICEBindsTwoSockets guards the established layout: without ICE the
// session still binds RTP and RTCP on adjacent ports.
func TestNonICEBindsTwoSockets(t *testing.T) {
	s := newICEBindSession(t)
	s.ICEConf = nil
	require.False(t, s.iceEnabled())

	require.NoError(t, s.createListeners(&s.Laddr))
	t.Cleanup(func() { _ = s.Close() })

	require.NotNil(t, s.rtpConn)
	require.NotNil(t, s.rtcpConn)
	assert.Nil(t, s.iceUDPConn)
	assert.False(t, s.rtcpMux)

	rtp := s.rtpConn.LocalAddr().(*net.UDPAddr)
	rtcp := s.rtcpConn.LocalAddr().(*net.UDPAddr)
	assert.Equal(t, rtp.Port+1, rtcp.Port, "RTCP stays on the RTP port + 1")
}

// TestICEDisabledWithoutDTLS pins the gate. ICE is only signalled on the DTLS
// profile, so an ICEConf on a plain RTP or SDES session must not change the
// socket layout: nothing would carry the candidates.
func TestICEDisabledWithoutDTLS(t *testing.T) {
	for _, mode := range []int{SecureRTPModeNone, SecureRTPModeSDES} {
		s := newICEBindSession(t)
		s.SecureRTP = mode

		assert.False(t, s.iceEnabled(), "ICE must stay off for SecureRTP=%d", mode)

		require.NoError(t, s.createListeners(&s.Laddr))
		assert.NotNil(t, s.rtpConn, "non DTLS session keeps its RTP socket")
		assert.NotNil(t, s.rtcpConn, "non DTLS session keeps its RTCP socket")
		require.NoError(t, s.Close())
	}
}

func TestDTLSEndpointRole(t *testing.T) {
	t.Run("inferred from remote addr", func(t *testing.T) {
		s := &MediaSession{}
		assert.Equal(t, DTLSEndpointRoleOfferer, s.dtlsEndpointRole(),
			"no remote address yet means we are offering")

		s.Raddr = net.UDPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 5000}
		assert.Equal(t, DTLSEndpointRoleAnswerer, s.dtlsEndpointRole(),
			"knowing the remote means we are answering")
	})

	t.Run("explicit role wins", func(t *testing.T) {
		s := &MediaSession{DTLSRole: DTLSEndpointRoleOfferer}
		s.Raddr = net.UDPAddr{IP: net.IPv4(10, 0, 0, 1), Port: 5000}
		assert.Equal(t, DTLSEndpointRoleOfferer, s.dtlsEndpointRole())
	})
}

// TestGenerateSDPWithICE asserts the offer carries what a peer needs to run
// ICE against us: credentials, candidates and rtcp-mux.
func TestGenerateSDPWithICE(t *testing.T) {
	iceSet := &iceSetup{
		ufrag: "abcd1234",
		pwd:   "0123456789abcdef012345",
		candidates: []string{
			"candidate:1 1 UDP 2130706431 192.0.2.1 10000 typ host",
			"candidate:2 1 UDP 1694498815 198.51.100.1 10000 typ srflx",
		},
		rtcpMux: true,
	}
	out := string(generateSDPForAudio(1, 1, "UDP/TLS/RTP/SAVP",
		net.IPv4(192, 0, 2, 1), net.IPv4(192, 0, 2, 1), 10000,
		sdp.ModeSendrecv, []Codec{CodecAudioUlaw}, sdesInline{}, nil, iceSet))

	assert.Contains(t, out, "a=ice-ufrag:abcd1234")
	assert.Contains(t, out, "a=ice-pwd:0123456789abcdef012345")
	assert.Contains(t, out, "a=candidate:1 1 UDP 2130706431 192.0.2.1 10000 typ host")
	assert.Contains(t, out, "a=candidate:2 1 UDP 1694498815 198.51.100.1 10000 typ srflx")
	assert.Contains(t, out, "a=rtcp-mux")

	// Every attribute must be a proper a= line.
	for _, line := range strings.Split(strings.TrimSpace(out), "\r\n") {
		assert.NotEmpty(t, line)
	}
}

// TestGenerateSDPWithoutICEHasNoICEAttrs is the counterpart: a plain session
// must not leak ICE or rtcp-mux attributes into its offer. A UDP trunk neither
// understands them nor wants the extra datagram size.
func TestGenerateSDPWithoutICEHasNoICEAttrs(t *testing.T) {
	out := string(generateSDPForAudio(1, 1, "RTP/AVP",
		net.IPv4(192, 0, 2, 1), net.IPv4(192, 0, 2, 1), 10000,
		sdp.ModeSendrecv, []Codec{CodecAudioUlaw}, sdesInline{}, nil, nil))

	assert.NotContains(t, out, "a=ice-ufrag")
	assert.NotContains(t, out, "a=ice-pwd")
	assert.NotContains(t, out, "a=candidate")
	assert.NotContains(t, out, "a=rtcp-mux")
}

// TestRemoteICERequiresCredentials asserts a peer that signals no ICE
// credentials is rejected rather than silently falling back to a transport the
// session never bound.
func TestRemoteICERequiresCredentials(t *testing.T) {
	tests := []struct {
		name  string
		attrs []string
		errIs string
	}{
		{
			name:  "no ufrag or pwd",
			attrs: []string{"rtcp-mux"},
			errIs: "ice-ufrag",
		},
		{
			name:  "ufrag without pwd",
			attrs: []string{"ice-ufrag:remote01", "rtcp-mux"},
			errIs: "ice-ufrag",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			s := newICEBindSession(t)
			err := s.remoteICE(tc.attrs)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.errIs)
		})
	}
}

// TestRemoteICERequiresRTCPMux asserts a peer that will not mux RTCP is
// rejected. An ICE session has one nominated pair and therefore no second port
// to put RTCP on, so accepting the call would leave RTCP nowhere to go.
//
// The session is bound first, on purpose. listenICE sets rtcpMux for every ICE
// session to record our own intent, so a check written against that field
// would pass here while never firing in production. The remote's answer has to
// be tracked on its own.
func TestRemoteICERequiresRTCPMux(t *testing.T) {
	s := newICEBindSession(t)
	require.NoError(t, s.createListeners(&s.Laddr))
	t.Cleanup(func() { _ = s.Close() })
	require.True(t, s.rtcpMux, "bind records our own intent to mux")

	err := s.remoteICE([]string{"ice-ufrag:remote01", "ice-pwd:remotepwd0123456789"})
	require.Error(t, err, "a remote that did not offer rtcp-mux must be rejected")
	assert.Contains(t, err.Error(), "rtcp-mux")
}

// TestDTLSTransportSelection asserts the DTLS handshake runs on the DTLS
// stream once ICE is up, and on the RTP socket otherwise. Handing DTLS the RTP
// stream under ICE would starve the handshake, since records are routed away
// from it.
func TestDTLSTransportSelection(t *testing.T) {
	t.Run("without ice uses rtp socket", func(t *testing.T) {
		s := newICEBindSession(t)
		s.ICEConf = nil
		require.NoError(t, s.createListeners(&s.Laddr))
		t.Cleanup(func() { _ = s.Close() })

		assert.Equal(t, s.rtpConn, s.dtlsTransport())
	})

	t.Run("with ice uses dtls stream of mux", func(t *testing.T) {
		local, remote := udpConnPair(t)
		s := newICEBindSession(t)
		s.iceMux = newICEMux(local, remote.LocalAddr())
		t.Cleanup(func() { _ = s.iceMux.Close() })

		s.rtpConn = s.iceMux.rtp
		assert.Equal(t, s.iceMux.dtls, s.dtlsTransport())
		assert.NotEqual(t, s.rtpConn, s.dtlsTransport(),
			"DTLS must not read the RTP stream, records are routed away from it")
	})
}
