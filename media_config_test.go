// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"crypto/tls"
	"strings"
	"testing"

	"github.com/emiago/diago/media"
	"github.com/emiago/diago/testdata"
	"github.com/emiago/sipgo"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// newMediaConfDiago builds a Diago carrying conf over a single transport. No
// SIP listener is bound: these tests only need the media config a dialog on
// that transport would start from.
func newMediaConfDiago(t *testing.T, tran Transport, conf MediaConfig) (*Diago, *Transport) {
	t.Helper()

	ua, err := sipgo.NewUA()
	require.NoError(t, err)
	t.Cleanup(func() { ua.Close() })

	if tran.ID == "" {
		tran.ID = "udp"
	}
	if tran.Transport == "" {
		tran.Transport = "udp"
	}
	if tran.BindHost == "" {
		tran.BindHost = "127.0.0.1"
	}

	dg := NewDiago(ua, WithTransport(tran), WithMediaConfig(conf))
	resolved, ok := dg.getTransport(tran.Transport)
	require.True(t, ok, "transport %q must be loaded", tran.Transport)
	return dg, resolved
}

// initSessionFor builds the media session a dialog on tran would get, going
// through the same config path the server and client dialogs use.
func initSessionFor(t *testing.T, dg *Diago, tran *Transport) *media.MediaSession {
	t.Helper()

	d := &DialogMedia{}
	require.NoError(t, d.initMediaSessionFromConf(dg.mediaConfForTransport(tran)))
	t.Cleanup(func() { _ = d.Close() })
	return d.mediaSession
}

// TestMediaConfigICEWiring covers the path from WithMediaConfig down to the
// media session. The media package has its own ICE tests, but they build a
// MediaSession directly, so nothing there notices when the config never
// reaches one.
func TestMediaConfigICEWiring(t *testing.T) {
	codecs := []media.Codec{media.CodecAudioUlaw}

	// An ICEConfig on the Diago wide media config must reach the session and
	// switch ICE on. ice-ufrag is only written once an agent exists, so it says
	// the session really is negotiating ICE rather than just holding the config.
	t.Run("ICEConfigEnablesICE", func(t *testing.T) {
		dg, tran := newMediaConfDiago(t,
			Transport{MediaSRTP: media.SecureRTPModeDTLS},
			MediaConfig{Codecs: codecs, ICEConfig: &media.ICEConfig{}},
		)

		sess := initSessionFor(t, dg, tran)
		require.NotNil(t, sess.ICEConf, "ICEConfig must reach the media session")

		sdp := string(sess.LocalSDP())
		assert.Contains(t, sdp, "a=ice-ufrag:", "an ICE session offers its credentials")
		assert.Contains(t, sdp, "a=ice-pwd:")
		assert.Contains(t, sdp, "a=rtcp-mux", "ICE nominates one pair, so RTCP is muxed")
	})

	// The default path must stay exactly as it was: no agent, no ICE in the
	// offer and the two socket RTP/RTCP layout.
	t.Run("NilICEConfigKeepsNonICEPath", func(t *testing.T) {
		dg, tran := newMediaConfDiago(t,
			Transport{MediaSRTP: media.SecureRTPModeDTLS},
			MediaConfig{Codecs: codecs},
		)

		sess := initSessionFor(t, dg, tran)
		require.Nil(t, sess.ICEConf, "nothing may invent an ICE config")

		sdp := string(sess.LocalSDP())
		assert.NotContains(t, sdp, "a=ice-ufrag:", "a non ICE session offers no ICE")
		assert.NotContains(t, sdp, "a=candidate:")
		assert.NotContains(t, sdp, "a=rtcp-mux")
	})

	// ICE is signalled only on the DTLS profile. Carrying an ICEConfig on a
	// plain RTP transport must not quietly change that transport's media.
	t.Run("ICEConfigIgnoredWithoutDTLSTransport", func(t *testing.T) {
		dg, tran := newMediaConfDiago(t,
			Transport{MediaSRTP: media.SecureRTPModeNone},
			MediaConfig{Codecs: codecs, ICEConfig: &media.ICEConfig{}},
		)

		sess := initSessionFor(t, dg, tran)
		sdp := string(sess.LocalSDP())
		assert.NotContains(t, sdp, "a=ice-ufrag:", "ICE needs the DTLS profile to be signalled")
	})
}

// TestMediaConfigDTLSWiring pins how the Diago wide DTLSConfig and the per
// transport MediaDTLSConf combine, now that both feed the one config field.
func TestMediaConfigDTLSWiring(t *testing.T) {
	codecs := []media.Codec{media.CodecAudioUlaw}
	cert := testdata.ServerCertificate()

	// Nothing set anywhere keeps the transport's own zero config, which is what
	// a caller that never mentioned DTLS had before.
	t.Run("FallsBackToTransportConf", func(t *testing.T) {
		dg, tran := newMediaConfDiago(t,
			Transport{
				MediaSRTP:     media.SecureRTPModeDTLS,
				MediaDTLSConf: media.DTLSConfig{Certificates: []tls.Certificate{cert}},
			},
			MediaConfig{Codecs: codecs},
		)

		conf := dg.mediaConfForTransport(tran)
		require.NotNil(t, conf.DTLSConfig)
		assert.Len(t, conf.DTLSConfig.Certificates, 1, "transport DTLS config still applies")
	})

	// A Diago wide config is opt in, so it wins over the transport value, which
	// cannot say "unset".
	t.Run("DiagoConfigWinsOverTransport", func(t *testing.T) {
		wide := &media.DTLSConfig{ServerName: "wide.example.com"}
		dg, tran := newMediaConfDiago(t,
			Transport{
				MediaSRTP:     media.SecureRTPModeDTLS,
				MediaDTLSConf: media.DTLSConfig{ServerName: "transport.example.com"},
			},
			MediaConfig{Codecs: codecs, DTLSConfig: wide},
		)

		conf := dg.mediaConfForTransport(tran)
		require.NotNil(t, conf.DTLSConfig)
		assert.Equal(t, "wide.example.com", conf.DTLSConfig.ServerName)
	})

	// The transport config is copied per dialog, so a dialog cannot write back
	// into the transport every later dialog reads.
	t.Run("TransportConfIsCopied", func(t *testing.T) {
		dg, tran := newMediaConfDiago(t,
			Transport{
				MediaSRTP:     media.SecureRTPModeDTLS,
				MediaDTLSConf: media.DTLSConfig{ServerName: "transport.example.com"},
			},
			MediaConfig{Codecs: codecs},
		)

		conf := dg.mediaConfForTransport(tran)
		require.NotNil(t, conf.DTLSConfig)
		conf.DTLSConfig.ServerName = "mutated.example.com"

		assert.Equal(t, "transport.example.com", tran.MediaDTLSConf.ServerName)
		assert.Equal(t, "transport.example.com", dg.mediaConfForTransport(tran).DTLSConfig.ServerName)
	})

	// The DTLS certificate must survive the trip into the session, or the
	// handshake would run on a config the caller never asked for.
	t.Run("ReachesMediaSession", func(t *testing.T) {
		dg, tran := newMediaConfDiago(t,
			Transport{MediaSRTP: media.SecureRTPModeDTLS},
			MediaConfig{
				Codecs:     codecs,
				DTLSConfig: &media.DTLSConfig{Certificates: []tls.Certificate{cert}},
			},
		)

		sess := initSessionFor(t, dg, tran)
		require.Len(t, sess.DTLSConf.Certificates, 1, "DTLSConfig must reach the media session")

		// The fingerprint is derived from the certificate we passed in, so it is
		// the observable that the session is really using it.
		assert.True(t, strings.Contains(string(sess.LocalSDP()), "a=fingerprint:"),
			"a DTLS session offers its certificate fingerprint")
	})
}
