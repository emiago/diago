// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2026, Emir Aganovic

package media

import (
	"context"
	"crypto/tls"
	"errors"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/emiago/diago/testdata"
	"github.com/pion/ice/v4"
	"github.com/stretchr/testify/require"
)

func TestMediaSessionWebrtcICEUDPConfiguration(t *testing.T) {
	t.Run("defaults to UDP IPv4 and IPv6", func(t *testing.T) {
		networkTypes, err := webRTCNetworkTypes(MediaSessionWebrtcConfig{})
		require.NoError(t, err)
		require.Equal(t, []ice.NetworkType{ice.NetworkTypeUDP4, ice.NetworkTypeUDP6}, networkTypes)
	})

	t.Run("maps backend neutral families", func(t *testing.T) {
		networkTypes, err := webRTCNetworkTypes(MediaSessionWebrtcConfig{
			IPFamilies: []ICEIPFamily{ICEIPFamilyIPv6, ICEIPFamilyIPv4, ICEIPFamilyIPv4},
		})
		require.NoError(t, err)
		require.Equal(t, []ice.NetworkType{ice.NetworkTypeUDP6, ice.NetworkTypeUDP4}, networkTypes)
	})

	t.Run("rejects deprecated TCP input", func(t *testing.T) {
		_, err := webRTCNetworkTypes(MediaSessionWebrtcConfig{
			NetworkTypes: []ice.NetworkType{ice.NetworkTypeTCP4},
		})
		require.ErrorContains(t, err, "UDP ICE only")

		session := &MediaSessionWebrtc{Codecs: []Codec{CodecAudioUlaw}}
		err = session.Init(t.Context(), MediaSessionWebrtcConfig{
			NetworkTypes: []ice.NetworkType{ice.NetworkTypeTCP4},
			DTLS: DTLSConfig{
				Certificates: []tls.Certificate{testdata.ServerCertificate()},
			},
		})
		require.ErrorIs(t, err, ErrICEGathering)
	})

	t.Run("rejects mixed configuration APIs", func(t *testing.T) {
		_, err := webRTCNetworkTypes(MediaSessionWebrtcConfig{
			IPFamilies:   []ICEIPFamily{ICEIPFamilyIPv4},
			NetworkTypes: []ice.NetworkType{ice.NetworkTypeUDP4},
		})
		require.ErrorContains(t, err, "cannot both be set")
	})
}

func TestMediaSessionWebrtcICECandidatePolicy(t *testing.T) {
	candidateTypes, err := webRTCCandidateTypes(MediaSessionWebrtcConfig{
		CandidateTypes: []ICECandidateType{
			ICECandidateHost,
			ICECandidateServerReflexive,
			ICECandidateRelay,
			ICECandidateHost,
		},
	})
	require.NoError(t, err)
	require.Equal(t, []ice.CandidateType{
		ice.CandidateTypeHost,
		ice.CandidateTypeServerReflexive,
		ice.CandidateTypeRelay,
	}, candidateTypes)

	_, err = webRTCCandidateTypes(MediaSessionWebrtcConfig{
		CandidateTypes: []ICECandidateType{99},
	})
	require.ErrorContains(t, err, "unsupported ICE candidate type")
}

func TestMediaSessionWebrtcICETimeouts(t *testing.T) {
	timeouts, err := normalizeICETimeouts(ICETimeouts{})
	require.NoError(t, err)
	require.Equal(t, DefaultICEDisconnectedTimeout, timeouts.Disconnected)
	require.Equal(t, DefaultICEFailedTimeout, timeouts.Failed)
	require.Equal(t, DefaultICEKeepaliveInterval, timeouts.Keepalive)

	timeouts, err = normalizeICETimeouts(ICETimeouts{
		Disconnected: 2 * time.Second,
		Failed:       3 * time.Second,
		Keepalive:    500 * time.Millisecond,
	})
	require.NoError(t, err)
	require.Equal(t, 2*time.Second, timeouts.Disconnected)
	require.Equal(t, 3*time.Second, timeouts.Failed)
	require.Equal(t, 500*time.Millisecond, timeouts.Keepalive)

	_, err = normalizeICETimeouts(ICETimeouts{Disconnected: -time.Second})
	require.ErrorContains(t, err, "cannot be negative")
}

func TestMediaSessionWebrtcICEIPFilter(t *testing.T) {
	filter := webRTCIPFilter(func(addr netip.Addr) bool {
		return addr == netip.MustParseAddr("127.0.0.1")
	})
	require.True(t, filter(net.ParseIP("127.0.0.1")))
	require.False(t, filter(net.ParseIP("192.0.2.1")))
	require.False(t, filter(net.IP{1, 2, 3}))
}

func TestMediaSessionWebrtcICEStateAndTypedError(t *testing.T) {
	states := make([]ice.ConnectionState, 0, 2)
	session := &MediaSessionWebrtc{
		onICEStateChange: func(state ice.ConnectionState) {
			states = append(states, state)
		},
	}
	session.notifyICEStateChange(ice.ConnectionStateChecking)
	session.notifyICEStateChange(ice.ConnectionStateConnected)
	require.Equal(t, []ice.ConnectionState{
		ice.ConnectionStateChecking,
		ice.ConnectionStateConnected,
	}, states)
	err := (&MediaSessionWebrtc{}).Init(
		t.Context(),
		MediaSessionWebrtcConfig{},
		func(ice.ConnectionState) {},
		func(ice.ConnectionState) {},
	)
	require.ErrorContains(t, err, "only one ICE state handler")

	underlying := errors.New("checks timed out")
	err = &ICEError{
		Phase: ICEPhaseConnection,
		Err:   underlying,
	}
	require.ErrorIs(t, err, ErrICEConnection)
	require.ErrorIs(t, err, underlying)
	var typedErr *ICEError
	require.ErrorAs(t, err, &typedErr)
	require.Equal(t, ICEPhaseConnection, typedErr.Phase)
}

func TestIntegrationMediaSessionWebrtcICEConnectTimeout(t *testing.T) {
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()

	session := &MediaSessionWebrtc{Codecs: []Codec{CodecAudioUlaw}}
	defer session.Close()
	require.NoError(t, session.Init(ctx, MediaSessionWebrtcConfig{
		IPFamilies:      []ICEIPFamily{ICEIPFamilyIPv4},
		CandidateTypes:  []ICECandidateType{ICECandidateHost},
		IncludeLoopback: true,
		IPFilter:        func(addr netip.Addr) bool { return addr.IsLoopback() },
		DTLS:            DTLSConfig{Certificates: []tls.Certificate{testdata.ServerCertificate()}},
	}))

	remoteOffer := []byte("v=0\r\n" +
		"o=- 1 1 IN IP4 127.0.0.1\r\n" +
		"s=-\r\n" +
		"t=0 0\r\n" +
		"m=audio 9 UDP/TLS/RTP/SAVPF 0\r\n" +
		"c=IN IP4 127.0.0.1\r\n" +
		"a=mid:0\r\n" +
		"a=rtcp-mux\r\n" +
		"a=sendrecv\r\n" +
		"a=ice-ufrag:remote\r\n" +
		"a=ice-pwd:remote-password-for-timeout-test\r\n" +
		"a=fingerprint:sha-256 00:11\r\n" +
		"a=setup:actpass\r\n" +
		"a=rtpmap:0 PCMU/8000\r\n" +
		"a=candidate:1 1 udp 2130706431 127.0.0.1 9 typ host\r\n" +
		"a=end-of-candidates\r\n")
	require.NoError(t, session.RemoteSDP(ctx, remoteOffer, false))

	finalizeCtx, finalizeCancel := context.WithTimeout(ctx, 100*time.Millisecond)
	defer finalizeCancel()
	started := time.Now()
	err := session.Finalize(finalizeCtx)
	require.ErrorIs(t, err, ErrICEConnection)
	require.Less(t, time.Since(started), time.Second)
	var iceErr *ICEError
	require.ErrorAs(t, err, &iceErr)
	require.Equal(t, ICEPhaseConnection, iceErr.Phase)
}
