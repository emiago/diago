// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package media

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"net"
	"strings"

	"github.com/emiago/dtls/v3"
	"github.com/pion/logging"
)

var (
	DTLSDebug bool
)

const (
	ServerClientAuthNoCert      = int(dtls.NoClientCert)
	ServerClientAuthRequireCert = int(dtls.RequestClientCert)
)

type DTLSConfig struct {
	Certificates []tls.Certificate
	// If used as client this would verify server certificate
	ServerName string

	// ServerClientAuth determines the server's policy for
	// TLS Client Authentication. The default is ServerClientAuthNoCert.
	// Check ServerClientAuth
	ServerClientAuth int

	fingerprints []sdpFingerprints
}

func dtlsServer(conn net.PacketConn, raddr net.Addr, certificates []tls.Certificate) (*dtls.Conn, error) {
	return dtlsServerConf(conn, raddr, DTLSConfig{
		Certificates: certificates,
	})
}

func dtlsServerConf(conn net.PacketConn, raddr net.Addr, conf DTLSConfig) (*dtls.Conn, error) {
	config := &dtls.Config{
		// Use appropriate certificate or generate self-signed
		Certificates: conf.Certificates,
		// CipherSuites: []dtls.CipherSuiteID{
		// 	dtls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
		// },
		SRTPProtectionProfiles: []dtls.SRTPProtectionProfile{
			dtls.SRTP_AEAD_AES_128_GCM,
			dtls.SRTP_AES128_CM_HMAC_SHA1_80,
			// dtls.SRTP_AES128_CM_HMAC_SHA1_32,
		},

		// SignatureSchemes: []tls.SignatureScheme{
		// 	tls.ECDSAWithP256AndSHA256
		// },

		// If you're acting as the server
		// We are verifying Connection fingerprints so we require client cert
		// use dtls.NoClientCert without verfication
		ClientAuth:           dtls.ClientAuthType(conf.ServerClientAuth),
		ExtendedMasterSecret: dtls.RequireExtendedMasterSecret,

		// IT IS STILL UNCLEAR WHY WE CAN NOT READ CERTIFICATE HERE
		VerifyConnection: func(state *dtls.State) error {
			if len(conf.fingerprints) == 0 {
				return nil
			}
			return dtlsVerifyConnection(state, conf.fingerprints)
		},
		StopReaderAfterHandshake: true,
	}

	if DTLSDebug {
		loggerFactory := logging.NewDefaultLoggerFactory()
		loggerFactory.DefaultLogLevel = logging.LogLevelTrace
		config.LoggerFactory = loggerFactory
	}

	return dtls.Server(conn, raddr, config)
}

func dtlsClient(conn net.PacketConn, raddr net.Addr, certificates []tls.Certificate, serverName string) (*dtls.Conn, error) {
	// Client DTLS config
	return dtlsClientConf(conn, raddr, DTLSConfig{
		Certificates: certificates,
		ServerName:   serverName,
	})
}

func dtlsClientConf(conn net.PacketConn, raddr net.Addr, conf DTLSConfig) (*dtls.Conn, error) {
	serverName := conf.ServerName
	config := &dtls.Config{
		Certificates: conf.Certificates,
		// CipherSuites: []dtls.CipherSuiteID{
		// 	dtls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
		// },
		SRTPProtectionProfiles: []dtls.SRTPProtectionProfile{
			dtls.SRTP_AEAD_AES_128_GCM,
			dtls.SRTP_AES128_CM_HMAC_SHA1_80,
		},
		InsecureSkipVerify:   serverName == "", // Accept self-signed certs (for dev)
		ServerName:           serverName,       // If insecure is false
		ExtendedMasterSecret: dtls.RequireExtendedMasterSecret,
		// ClientAuth:           dtls.NoClientCert,
		VerifyConnection: func(state *dtls.State) error {
			if len(conf.fingerprints) == 0 {
				return nil
			}
			return dtlsVerifyConnection(state, conf.fingerprints)
		},
		// ClientAuth: dtls.RequestClientCert,
		StopReaderAfterHandshake: true,
	}

	if DTLSDebug {
		loggerFactory := logging.NewDefaultLoggerFactory()
		loggerFactory.DefaultLogLevel = logging.LogLevelTrace
		config.LoggerFactory = loggerFactory
	}

	return dtls.Client(conn, raddr, config)
}

func dtlsVerifyConnection(state *dtls.State, fingerprints []sdpFingerprints) error {
	if len(state.PeerCertificates) == 0 {
		return fmt.Errorf("no certificate found in dtls")
	}

	remoteCert := state.PeerCertificates[0]
	for _, fp := range fingerprints {
		var remoteFP string
		if fp.alg == "SHA-256" {
			var err error
			remoteFP, err = dtlsSHA256CertificateFingerprint(remoteCert)
			if err != nil {
				return err
			}
		} else {
			DefaultLogger().Debug("Skiping fingerprint due to unsuported alg", "alg", fp.alg)
			continue
		}

		DefaultLogger().Debug("Comparing fingerprint", "alg", fp.alg, "fp", fp.fingerprint, "rfp", remoteFP)
		if fp.fingerprint == remoteFP {
			return nil
		}
	}

	return nil
}

func dtlsSDPAttr(fingerprint string) []string {
	// https://datatracker.ietf.org/doc/html/rfc8842#section-5.1
	//	In order to negotiate a DTLS association, the following SDP attributes are used:
	//
	// The SDP "setup" attribute, defined in [RFC4145], is used to negotiate the DTLS roles;
	// The SDP "fingerprint" attribute, defined in [RFC8122], is used to provide one or more fingerprint values; and The SDP "tls-id" attribute, defined in this specification, is used to identity the DTLS association.

	// The certificate received during the DTLS handshake [RFC6347] MUST match a certificate fingerprint received in SDP "fingerprint"
	return []string{
		//  'active': The endpoint will initiate an outgoing connection.
		// 'passive': The endpoint will accept an incoming connection.

		// 'actpass': The endpoint is willing to accept an incoming
		// connection or to initiate an outgoing connection.

		// 'holdconn': The endpoint does not want the connection to be
		// established for the time being.

		// Unlike for TCP and TLS connections, endpoints MUST NOT use the SDP "setup" attribute "holdconn" value when negotiating a DTLS association.

		// https://datatracker.ietf.org/doc/html/rfc8842#section-5.2
		"a=setup: actpass",

		// A certificate fingerprint is a secure one-way hash of the
		//    Distinguished Encoding Rules (DER) form of the certificate.

		// 		The hash value is represented as a sequence of
		//    uppercase hexadecimal bytes, separated by colons.
		// fingerprint-attribute  =  "fingerprint" ":" hash-func SP fingerprint

		// hash-func              =  "sha-1" / "sha-224" / "sha-256" /
		// 						  "sha-384" / "sha-512" /
		// 						  "md5" / "md2" / token
		// 						  ; Additional hash functions can only come
		// 						  ; from updates to RFC 3279
		// ex:
		// 		a=fingerprint:SHA-256 \
		//     12:DF:3E:5D:49:6B:19:E5:7C:AB:4A:AD:B9:B1:3F:82:18:3B:54:02:12:DF: \
		//     3E:5D:49:6B:19:E5:7C:AB:4A:AD
		//  a=fingerprint:SHA-1 \
		//     4A:AD:B9:B1:3F:82:18:3B:54:02:12:DF:3E:5D:49:6B:19:E5:7C:AB
		"a=fingerprint:SHA-256 " + fingerprint,

		// 	BUNDLE makes tls-id redundant:

		// In WebRTC, multiple media streams are multiplexed over a single DTLS session using the a=group:BUNDLE mechanism.

		// Since all media share the same DTLS session, there's no ambiguity—only one DTLS association exists.

		// tls-id is primarily useful when non-BUNDLE or multiple DTLS sessions might happen.
		// "a=tls-id",
	}

}

func dtlsSHA256Fingerprint(cert tls.Certificate) (string, error) {
	if len(cert.Certificate) == 0 {
		return "", fmt.Errorf("no certificate data found")
	}
	return dtlsSHA256CertificateFingerprint(cert.Certificate[0])
}

func dtlsSHA256CertificateFingerprint(cert []byte) (string, error) {

	// Parse the leaf certificate
	leaf, err := x509.ParseCertificate(cert)
	if err != nil {
		return "", fmt.Errorf("failed to parse certificate: %v", err)
	}

	// Calculate SHA-256 fingerprint
	hash := sha256.Sum256(leaf.Raw)

	// Format as colon-separated hex string
	hexStr := strings.ToUpper(hex.EncodeToString(hash[:]))
	var fingerprint strings.Builder
	for i := 0; i < len(hexStr); i += 2 {
		if i > 0 {
			fingerprint.WriteString(":")
		}
		fingerprint.WriteString(hexStr[i : i+2])
	}

	return fingerprint.String(), nil
}
