// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package media

import (
	"crypto/tls"
	"log/slog"
	"net"
	"testing"

	"github.com/emiago/diago/testdata"
	"github.com/emiago/dtls/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// dtlsHandshakeOverTransport runs a real DTLS handshake between transport and a
// peer socket, and returns the local conn. The teardown paths under test are
// pion's own, so they have to be reached through a completed handshake rather
// than a hand built Conn.
func dtlsHandshakeOverTransport(t *testing.T, transport net.PacketConn, peer *net.UDPConn, peerAddr net.Addr) *dtls.Conn {
	t.Helper()

	server, err := dtlsServer(transport, peerAddr, []tls.Certificate{testdata.ServerCertificate()})
	require.NoError(t, err)

	client, err := dtlsClient(peer, transport.LocalAddr(), []tls.Certificate{testdata.ClientCertificate()}, "")
	require.NoError(t, err)
	t.Cleanup(func() { _ = client.Close() })

	serverErr := make(chan error, 1)
	go func() { serverErr <- server.Handshake() }()
	require.NoError(t, client.Handshake())
	require.NoError(t, <-serverErr)

	return server
}

// TestDTLSTeardownLeavesSessionTransportOpen asserts the DTLS stack cannot take
// the media transport down with it.
//
// RFC 5764 section 5.1.2 multiplexes DTLS onto the socket that carries media,
// but the DTLS stack still treats that socket as its own: every teardown path
// ends in nextConn.Close(). Under ICE the socket is one stream of a mux, whose
// Close tears down the nominated pair, so a fatal alert or a peer hangup would
// take RTP and RTCP with it and silence a call that is otherwise healthy.
func TestDTLSTeardownLeavesSessionTransportOpen(t *testing.T) {
	local, remote := udpConnPair(t)
	peer := remote.(*udpPeer).UDPConn

	s := newICEBindSession(t)
	s.iceMux = newICEMux(local, remote.LocalAddr())
	t.Cleanup(func() { _ = s.iceMux.Close() })
	s.rtpConn = s.iceMux.rtp

	server := dtlsHandshakeOverTransport(t, s.dtlsTransport(), peer, remote.LocalAddr())

	// close_notify is the gentlest of the teardowns that reach nextConn.Close();
	// a fatal alert arrives at the same place via close(false).
	require.NoError(t, server.Close())

	// The nominated pair must still carry media.
	rtpPkt := []byte{0x80, 8, 0x00, 0x01, 0xbe, 0xef}
	_, err := peer.WriteTo(rtpPkt, local.LocalAddr())
	require.NoError(t, err)
	assert.Equal(t, rtpPkt, readWithTimeout(t, s.iceMux.rtp),
		"RTP must survive DTLS teardown: the DTLS stack does not own the transport")
}

func TestDTLSSetup(t *testing.T) {
	clientAddr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 15333}
	serverAddr := &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 15444}
	slog.SetLogLoggerLevel(slog.LevelDebug)

	listener, err := net.ListenUDP("udp", serverAddr)
	require.NoError(t, err)
	defer listener.Close()

	serverConn, err := dtlsServer(listener, clientAddr, []tls.Certificate{testdata.ServerCertificate()})
	require.NoError(t, err)
	defer serverConn.Close()

	listenerClient, err := net.ListenUDP("udp", clientAddr)
	if err != nil {
		panic(err)
	}
	defer listenerClient.Close()

	clientConn, err := dtlsClient(listenerClient, serverAddr, []tls.Certificate{testdata.ClientCertificate()}, "")
	require.NoError(t, err)
	defer clientConn.Close()

	serverErr := make(chan error)
	go func() {
		serverErr <- serverConn.Handshake()
	}()
	err = clientConn.Handshake()
	require.NoError(t, err)
	require.NoError(t, <-serverErr)
}

func TestDTLSFingerprint(t *testing.T) {
	fingerprint, err := dtlsSHA256Fingerprint(testdata.ClientCertificate())
	require.NoError(t, err)
	t.Log(fingerprint)

	fingerprint, err = dtlsSHA256Fingerprint(testdata.ServerCertificate())
	require.NoError(t, err)
	t.Log(fingerprint)
}
