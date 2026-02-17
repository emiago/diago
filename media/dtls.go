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
	"github.com/emiago/dtls/v3/pkg/crypto/elliptic"
	"github.com/pion/logging"
)

var (
	DTLSDebug bool
)

const (
	ServerClientAuthNoCert      = int(dtls.NoClientCert)
	ServerClientAuthRequireCert = int(dtls.RequestClientCert)

	EllipticCurveP256   uint16 = uint16(elliptic.P256)
	EllipticCurveP384   uint16 = uint16(elliptic.P384)
	EllipticCurveX25519 uint16 = uint16(elliptic.X25519)
)

type DTLSConfig struct {
	Certificates []tls.Certificate
	// If used as client this would verify server certificate
	ServerName string

	// ServerClientAuth determines the server's policy for
	// TLS Client Authentication. The default is ServerClientAuthNoCert.
	// Check ServerClientAuth
	ServerClientAuth int

	// SRTPProfiles to use in exchange. Check constant vars with media.SRTPProfile...
	SRTPProfiles []uint16

	// SDP Setup Role force value.
	// Values: active,passive,actpass
	// Default: offer->active answer->passive
	SDPSetupRole func(offer bool) string

	// List of Elliptic Curves to use
	//
	// If an ECC ciphersuite is configured and EllipticCurves is empty
	// it will default to X25519, P-256, P-384 in this specific order.
	// Check values with media.Eliptic<name>
	EllipticCurves []uint16
}

func (conf *DTLSConfig) ToLibConf(fingerprints []sdpFingerprints) *dtls.Config {

	config := &dtls.Config{
		// Use appropriate certificate or generate self-signed
		Certificates:     conf.Certificates,
		SignatureSchemes: []tls.SignatureScheme{},

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

		InsecureSkipVerify: conf.ServerName == "", // Accept self-signed certs (for dev)
		ServerName:         conf.ServerName,       // If insecure is false

		// IT IS STILL UNCLEAR WHY WE CAN NOT READ CERTIFICATE HERE
		VerifyConnection: func(state *dtls.State) error {
			if len(fingerprints) == 0 {
				return nil
			}
			return dtlsVerifyConnection(state, fingerprints)
		},
		StopReaderAfterHandshake: true,
	}

	if conf.SRTPProfiles != nil {
		srtpProfs := make([]dtls.SRTPProtectionProfile, len(conf.SRTPProfiles))
		for i, p := range conf.SRTPProfiles {
			srtpProfs[i] = dtls.SRTPProtectionProfile(p)
		}
		config.SRTPProtectionProfiles = srtpProfs
	}

	if conf.EllipticCurves != nil {
		config.EllipticCurves = make([]elliptic.Curve, len(conf.EllipticCurves))
		for i, c := range conf.EllipticCurves {
			config.EllipticCurves[i] = elliptic.Curve(c)
		}
	}

	if DTLSDebug {
		loggerFactory := logging.NewDefaultLoggerFactory()
		loggerFactory.DefaultLogLevel = logging.LogLevelTrace
		config.LoggerFactory = loggerFactory
	}
	return config
}

func dtlsServer(conn net.PacketConn, raddr net.Addr, certificates []tls.Certificate) (*dtls.Conn, error) {
	conf := DTLSConfig{
		Certificates: certificates,
	}
	return dtls.Server(conn, raddr, conf.ToLibConf([]sdpFingerprints{}))
}

func dtlsClient(conn net.PacketConn, raddr net.Addr, certificates []tls.Certificate, serverName string) (*dtls.Conn, error) {
	// Client DTLS config
	conf := DTLSConfig{
		Certificates: certificates,
		ServerName:   serverName,
	}
	return dtls.Client(conn, raddr, conf.ToLibConf([]sdpFingerprints{}))
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
