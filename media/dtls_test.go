// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package media

import (
	"crypto/tls"
	"log/slog"
	"net"
	"testing"

	"github.com/emiago/diago/testdata"
	"github.com/stretchr/testify/require"
)

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
