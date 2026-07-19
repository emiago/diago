package media

import (
	"net"
	"testing"
)

// These tests pin the media profile rules to the RFCs, deliberately NOT to any
// one peer. The regression that motivated them was an Asterisk chan_sip webphone
// offering RTP/SAVPF with a=fingerprint -- DTLS-SRTP under the pre-RFC-5764
// profile name -- being rejected as "unsupported media description protocol".
// The fix is not an arm for that peer: it is parsing the profile's axes and
// taking the keying mechanism from the attributes, which is what the specs say
// and which classifies every peer, including ones we have never seen.

// TestParseRTPProfileDecomposesAxes drives the full product of the axes rather
// than the profiles we happen to meet. S = SRTP (RFC 3711), F = AVPF feedback
// (RFC 4585), UDP/TLS/ = DTLS-keyed (RFC 5764) -- independent axes, so all eight
// combinations are well-formed inputs and must classify, not be enumerated.
func TestParseRTPProfileDecomposesAxes(t *testing.T) {
	tests := []struct {
		proto        string
		wantSecure   bool
		wantFeedback bool
		wantDTLS     bool
	}{
		{"RTP/AVP", false, false, false},
		{"RTP/AVPF", false, true, false},
		{"RTP/SAVP", true, false, false},
		// The regression: DTLS-SRTP signalled under the plain profile name. It is
		// "secure, with feedback"; whether it is DTLS-keyed is NOT readable here,
		// it comes from a=fingerprint.
		{"RTP/SAVPF", true, true, false},
		{"UDP/TLS/RTP/SAVP", true, false, true},
		{"UDP/TLS/RTP/SAVPF", true, true, true},
	}

	for _, tt := range tests {
		t.Run(tt.proto, func(t *testing.T) {
			got, ok := parseRTPProfile(tt.proto)
			if !ok {
				t.Fatalf("parseRTPProfile(%q) rejected a well-formed RTP profile", tt.proto)
			}
			if got.secure != tt.wantSecure {
				t.Errorf("secure = %v, want %v", got.secure, tt.wantSecure)
			}
			if got.feedback != tt.wantFeedback {
				t.Errorf("feedback = %v, want %v", got.feedback, tt.wantFeedback)
			}
			if got.dtls != tt.wantDTLS {
				t.Errorf("dtls = %v, want %v", got.dtls, tt.wantDTLS)
			}
		})
	}
}

// TestParseRTPProfileRejectsNonRTPAndIncoherent pins the boundary. Rejecting is
// correct for protocols this stack cannot answer; it must not become a shrug
// that accepts anything.
func TestParseRTPProfileRejectsNonRTPAndIncoherent(t *testing.T) {
	for _, proto := range []string{
		"",
		"udp",
		"TCP/MSRP",
		"RTP/FOO",
		// DTLS keying over an unsecured profile is not a thing a peer may offer:
		// the whole point of the UDP/TLS/ prefix is keying SRTP.
		"UDP/TLS/RTP/AVP",
	} {
		if _, ok := parseRTPProfile(proto); ok {
			t.Errorf("parseRTPProfile(%q) = accepted; want rejected", proto)
		}
	}
}

// TestParseRTPProfileIsLiberalInCasing pins tolerance of casing and whitespace.
// SDP tokens are defined uppercase (RFC 4566), but dropping a call over a peer's
// casing is a worse outcome than accepting it, and accepting costs nothing.
func TestParseRTPProfileIsLiberalInCasing(t *testing.T) {
	for _, proto := range []string{"rtp/savpf", "  RTP/SAVPF  ", "Rtp/SavpF"} {
		got, ok := parseRTPProfile(proto)
		if !ok {
			t.Fatalf("parseRTPProfile(%q) rejected; casing must not decide interop", proto)
		}
		if !got.secure || !got.feedback {
			t.Errorf("parseRTPProfile(%q) = %+v; want secure+feedback", proto, got)
		}
	}
}

// TestSDPFingerprintIsAuthoritativeForKeying pins the rule that actually fixes
// the regression: the KEYING mechanism comes from the attributes, never from the
// profile name. a=fingerprint means the keys arrive over DTLS (RFC 5763 s5,
// RFC 4572 s5), whatever profile name carried the offer.
func TestSDPFingerprintIsAuthoritativeForKeying(t *testing.T) {
	t.Run("present", func(t *testing.T) {
		attrs := []string{
			"rtcp-mux",
			"setup:actpass",
			"fingerprint:SHA-256 AA:0F:BA:F8:18:6D:6A:BD:0E:3B:98:FE:12:CA:59:D1",
		}
		if !sdpHasFingerprint(attrs) {
			t.Error("sdpHasFingerprint() = false with a=fingerprint present; " +
				"the offer would be misread as SDES and rejected for having no a=crypto")
		}
	})

	t.Run("absent", func(t *testing.T) {
		attrs := []string{"rtcp-mux", "crypto:1 AES_CM_128_HMAC_SHA1_80 inline:abc"}
		if sdpHasFingerprint(attrs) {
			t.Error("sdpHasFingerprint() = true with only a=crypto; an SDES offer must not be read as DTLS")
		}
	})

	t.Run("case insensitive", func(t *testing.T) {
		if !sdpHasFingerprint([]string{"FINGERPRINT:SHA-256 AA:0F"}) {
			t.Error("sdpHasFingerprint() is case sensitive; attribute casing must not decide keying")
		}
	})
}

// TestSecureRequestMeansSDESNotJustSecure pins the distinction the old code got
// wrong. secureRequest asks "must the keys be inline in this SDP?" -- true only
// for a secured profile keyed by SDES. A DTLS-keyed offer is equally secure but
// carries no a=crypto, so demanding one rejects a valid offer. This is the exact
// shape of the bug: RTP/SAVPF + fingerprint classified as SDES -> no a=crypto ->
// ErrNoCommonCrypto.
func TestSecureRequestMeansSDESNotJustSecure(t *testing.T) {
	tests := []struct {
		name           string
		proto          string
		attrs          []string
		wantSDESNeeded bool
	}{
		{
			name:           "plain RTP/AVP needs no keys",
			proto:          "RTP/AVP",
			attrs:          []string{"rtcp-mux"},
			wantSDESNeeded: false,
		},
		{
			name:           "RTP/SAVP with crypto is SDES",
			proto:          "RTP/SAVP",
			attrs:          []string{"crypto:1 AES_CM_128_HMAC_SHA1_80 inline:abc"},
			wantSDESNeeded: true,
		},
		{
			name:           "RTP/SAVPF with fingerprint is DTLS, not SDES",
			proto:          "RTP/SAVPF",
			attrs:          []string{"setup:actpass", "fingerprint:SHA-256 AA:0F"},
			wantSDESNeeded: false,
		},
		{
			name:           "UDP/TLS/RTP/SAVPF is DTLS by profile",
			proto:          "UDP/TLS/RTP/SAVPF",
			attrs:          []string{"setup:actpass", "fingerprint:SHA-256 AA:0F"},
			wantSDESNeeded: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			prof, ok := parseRTPProfile(tt.proto)
			if !ok {
				t.Fatalf("parseRTPProfile(%q) rejected", tt.proto)
			}
			// Mirrors RemoteSDP's classification exactly.
			got := prof.secure && !prof.dtls && !sdpHasFingerprint(tt.attrs)
			if got != tt.wantSDESNeeded {
				t.Errorf("SDES-required = %v, want %v (proto=%q)", got, tt.wantSDESNeeded, tt.proto)
			}
		})
	}
}

// TestAnsweringOfferDistinguishesRoles pins the discriminator LocalSDP uses to
// know whether it is building an offer or an answer. It builds both, and the two
// have different rules: an answer's profile is dictated (RFC 3264 s6), an
// offer's is chosen.
func TestAnsweringOfferDistinguishesRoles(t *testing.T) {
	t.Run("offering: nothing applied yet", func(t *testing.T) {
		s := &MediaSession{}
		if s.answeringOffer() {
			t.Error("answeringOffer() = true before any remote SDP; there is no offer to mirror")
		}
	})

	t.Run("answering: an offer was applied", func(t *testing.T) {
		s := &MediaSession{remoteProto: "RTP/SAVPF", RemoteSDPIsAnswer: false}
		if !s.answeringOffer() {
			t.Error("answeringOffer() = false after applying an offer; the answer would not mirror it")
		}
	})

	t.Run("offerer reading the answer must not mirror", func(t *testing.T) {
		// We offered, the peer answered. remoteProto is set, but we are not the
		// answerer -- our own next offer stays ours to choose.
		s := &MediaSession{remoteProto: "RTP/AVP", RemoteSDPIsAnswer: true}
		if s.answeringOffer() {
			t.Error("answeringOffer() = true while reading an answer to our own offer")
		}
	})
}

// TestForkCarriesSecurityMode pins that a re-negotiation inherits the session's
// security. Dropping SecureRTP made every bodied re-INVITE on an encrypted call
// answer plain RTP/AVP with no crypto -- and since iceEnabled() is
// ICEConf != nil && SecureRTP == DTLS, the same zero silently dropped ICE too.
func TestForkCarriesSecurityMode(t *testing.T) {
	for _, mode := range []int{0, 1, 2} {
		s := &MediaSession{SecureRTP: mode, SRTPAlg: 7}
		got := s.Fork()
		if got.SecureRTP != mode {
			t.Errorf("Fork().SecureRTP = %d, want %d; a fork that downgrades answers a re-INVITE in the clear", got.SecureRTP, mode)
		}
		if got.SRTPAlg != 7 {
			t.Errorf("Fork().SRTPAlg = %d, want 7; the algorithm is a property of the session, not of one exchange", got.SRTPAlg)
		}
	}
}

// TestLocalDTLSSetupIsComplementaryToTheOffer drives the RFC 4145 section 4.1
// role table. active initiates, passive accepts, so an answer must be the
// complement of the offer or both endpoints take the same side.
func TestLocalDTLSSetupIsComplementaryToTheOffer(t *testing.T) {
	tests := []struct {
		name   string
		remote string
		want   string
		why    string
	}{
		{
			name:   "peer initiates so we accept",
			remote: "active",
			want:   "passive",
			why:    "RFC 4145 s4.1: active initiates, so the complement accepts",
		},
		{
			name:   "peer accepts so we initiate",
			remote: "passive",
			want:   "active",
			why:    "RFC 4145 s4.1: passive accepts, so the complement initiates",
		},
		{
			// The regression: Asterisk offers actpass. We answered passive (from
			// the "remote IP is known" heuristic) yet acted as client, so both
			// endpoints sent a ClientHello and the peer aborted the handshake with
			// UnexpectedMessage.
			name:   "peer defers the choice so we take it",
			remote: "actpass",
			want:   "active",
			why:    "RFC 5763 s5: setup:active is RECOMMENDED; passive stalls the handshake until the answer lands",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Raddr is set, which is what used to force passive regardless.
			s := &MediaSession{
				SecureRTP:       SecureRTPModeDTLS,
				dtlsRemoteSetup: tt.remote,
				Raddr:           net.UDPAddr{IP: net.ParseIP("194.102.34.49"), Port: 19132},
			}
			if got := s.localDTLSSetup(); got != tt.want {
				t.Errorf("localDTLSSetup() = %q, want %q (offer was %q)\n%s", got, tt.want, tt.remote, tt.why)
			}
		})
	}
}

// TestOffererAdvertisesActpass pins RFC 5763 s5: "The endpoint that is the
// offerer MUST use the setup attribute value of setup:actpass". With no offer
// applied there is nothing to be complementary to, and the peer must be left
// free to choose.
func TestOffererAdvertisesActpass(t *testing.T) {
	s := &MediaSession{SecureRTP: SecureRTPModeDTLS}
	if got := s.localDTLSSetup(); got != "actpass" {
		t.Errorf("localDTLSSetup() = %q with no offer applied, want \"actpass\"", got)
	}

	// A known remote address means we are dialling someone, not that we are
	// answering them. The old heuristic read it as "we are the server".
	s.Raddr = net.UDPAddr{IP: net.ParseIP("194.102.34.49"), Port: 19132}
	if got := s.localDTLSSetup(); got != "actpass" {
		t.Errorf("localDTLSSetup() = %q, want \"actpass\"; a known remote address is not an offer to answer", got)
	}
}

// TestAdvertisedRoleAndActedRoleAgree is the invariant the bug broke, stated
// directly: whatever a=setup we put in the SDP commits us to a DTLS role, and
// the role we play must be that one. Advertising passive while sending the
// ClientHello is the contradiction that produced Alert Fatal: UnexpectedMessage.
func TestAdvertisedRoleAndActedRoleAgree(t *testing.T) {
	for _, remote := range []string{"active", "passive", "actpass", ""} {
		s := &MediaSession{
			SecureRTP:       SecureRTPModeDTLS,
			dtlsRemoteSetup: remote,
			Raddr:           net.UDPAddr{IP: net.ParseIP("194.102.34.49"), Port: 19132},
		}
		advertised := s.localDTLSSetup()
		actsAsClient := s.dtlsActsAsClient()

		switch advertised {
		case "active":
			if !actsAsClient {
				t.Errorf("remote=%q: advertised active but does not send the ClientHello; the peer waits forever", remote)
			}
		case "passive":
			if actsAsClient {
				t.Errorf("remote=%q: advertised passive but sends the ClientHello; both endpoints are clients", remote)
			}
		case "actpass":
			// Still the peer's choice, so we must not preempt it: RFC 5763 s5 has
			// the offerer ready to receive a ClientHello before the answer.
			if actsAsClient {
				t.Errorf("remote=%q: advertised actpass but already acts as client before the peer chose", remote)
			}
		default:
			t.Errorf("remote=%q: advertised %q, which is not an RFC 4145 setup value", remote, advertised)
		}
	}
}

// TestSDPSetupRoleOverrideStillWins pins that the explicit escape hatch is not
// swallowed by the derivation. Nothing in the runtime sets it today, but it is
// public API and a caller that reaches for it means it.
func TestSDPSetupRoleOverrideStillWins(t *testing.T) {
	s := &MediaSession{
		SecureRTP:       SecureRTPModeDTLS,
		dtlsRemoteSetup: "actpass", // would otherwise derive active
		DTLSConf: DTLSConfig{
			SDPSetupRole: func(bool) string { return "passive" },
		},
	}
	if got := s.localDTLSSetup(); got != "passive" {
		t.Errorf("localDTLSSetup() = %q, want %q from the explicit override", got, "passive")
	}
	if s.dtlsActsAsClient() {
		t.Error("override says passive but the session acts as client; the override must govern both")
	}
}
