// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package media

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"strings"
	"sync/atomic"

	"github.com/pion/dtls/v3"
	"github.com/pion/dtls/v3/pkg/crypto/elliptic"
	"github.com/pion/logging"
)

var (
	DTLSDebug bool
)

// Secure RTP modes for MediaSession.SecureRTP.
const (
	SecureRTPModeNone = 0
	SecureRTPModeSDES = 1
	SecureRTPModeDTLS = 2
)

// DTLSEndpointRole is the offer/answer role of this endpoint.
//
// It decides two things that RFC 5763 and RFC 8445 tie to the same signalling
// role: the default a=setup value, and which side is the controlling ICE agent.
// DTLSEndpointRoleUnknown lets MediaSession infer the role from whether a
// remote address is already known.
type DTLSEndpointRole int

const (
	DTLSEndpointRoleUnknown DTLSEndpointRole = iota
	DTLSEndpointRoleOfferer
	DTLSEndpointRoleAnswerer
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
	SDPSetupRole func(offer bool) string `json:"-"`

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

// dtlsKeyExchangeConn lends the media socket to the DTLS stack.
//
// RFC 5764 section 5.1.2 multiplexes the handshake onto the transport that
// carries media, so the socket belongs to MediaSession and merely serves DTLS
// for the length of the exchange. The DTLS stack does not know that: every one
// of its teardown paths, whether a fatal alert, a handshake timeout or a peer
// close_notify, ends in a Close of its underlying conn. Under ICE that conn is
// one stream of a mux whose Close releases the nominated pair, so an alert on
// the handshake would silence RTP and RTCP for the rest of the call.
//
// DTLS here is only a key exchange: the association carries no application data,
// and nothing reads or writes it once the keying material is exported. Retiring
// it is therefore a local act, and detach makes that explicit.
type dtlsKeyExchangeConn struct {
	net.PacketConn
	detached atomic.Bool
}

func newDTLSKeyExchangeConn(conn net.PacketConn) *dtlsKeyExchangeConn {
	return &dtlsKeyExchangeConn{PacketConn: conn}
}

// detach retires the transport. Reads report EOF, which the DTLS read loop
// treats as a clean end, and writes are dropped instead of reaching the peer.
// It must be called before the conn is closed, so that the close_notify that
// Close emits is dropped with them: the peer's DTLS association is still live
// and is what its SRTP keys hang off, so telling it we are gone would fail the
// call. Retiring is invisible on the wire by design.
func (c *dtlsKeyExchangeConn) detach() {
	c.detached.Store(true)
}

func (c *dtlsKeyExchangeConn) ReadFrom(p []byte) (int, net.Addr, error) {
	if c.detached.Load() {
		return 0, nil, io.EOF
	}
	return c.PacketConn.ReadFrom(p)
}

func (c *dtlsKeyExchangeConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	if c.detached.Load() {
		return len(p), nil
	}
	return c.PacketConn.WriteTo(p, addr)
}

// Close is deliberately a no-op. The socket outlives the DTLS association and
// is closed by MediaSession.Close, which is what owns it.
func (c *dtlsKeyExchangeConn) Close() error { return nil }

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
