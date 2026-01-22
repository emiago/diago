// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package testdata

import (
	"crypto/tls"
	"crypto/x509"
	_ "embed"
)

// This will generate TLS certificates needed for test below
// openssl is required
//go:generate bash -c "./generate_certs_rsa.sh"

var (
	//go:embed certs/rootca-cert.pem
	rootCA []byte

	//go:embed certs/server.crt
	serverCRT []byte

	//go:embed certs/server.key
	serverKEY []byte

	//go:embed certs/client.crt
	clientCRT []byte

	//go:embed certs/client.key
	clientKEY []byte
)

func ServerCertificate() tls.Certificate {
	cert, err := tls.X509KeyPair(serverCRT, serverKEY)
	if err != nil {
		panic(err)
	}
	return cert
}

func ServerTLSConfig() *tls.Config {
	cert := ServerCertificate()

	cfg := &tls.Config{
		InsecureSkipVerify: true,
		Certificates:       []tls.Certificate{cert},
	}

	return cfg
}

func ClientCertificate() tls.Certificate {
	cert, err := tls.X509KeyPair(clientCRT, clientKEY)
	if err != nil {
		panic(err)
	}
	return cert
}

func ClientTLSConfig() *tls.Config {
	cert := ClientCertificate()

	roots := x509.NewCertPool()

	ok := roots.AppendCertsFromPEM(rootCA)
	if !ok {
		panic("failed to parse root certificate")
	}

	tlsConf := &tls.Config{
		Certificates: []tls.Certificate{cert},
		RootCAs:      roots,
		// InsecureSkipVerify: false,
		// InsecureSkipVerify: true,
		// MinVersion:         tls.VersionTLS12,
	}

	return tlsConf
}
