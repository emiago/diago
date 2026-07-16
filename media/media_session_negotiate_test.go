// SPDX-License-Identifier: MPL-2.0

package media

import (
	"net"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func negotiateBody(lines ...string) []byte {
	return []byte(strings.Join(lines, "\r\n") + "\r\n")
}

func negotiateSession(codecs ...Codec) *MediaSession {
	return &MediaSession{
		Codecs: codecs,
		Laddr:  net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5004},
	}
}

// Payload type numbers for dynamic formats are assigned by each side
// independently (RFC 3551 section 3), so matching a remote codec against a local
// one by number drops every format the peer happens to number differently.
func TestUpdateRemoteCodecsMatchesByFormatNotNumber(t *testing.T) {
	m := negotiateSession(CodecAudioAlaw, CodecAudioOpus, CodecTelephoneEvent8000)

	// Peer numbers opus 111 and telephone-event 100. We number them 96 and 101.
	remote := []Codec{
		{Name: "opus", PayloadType: 111, SampleRate: 48000, NumChannels: 2, SampleDur: CodecAudioOpus.SampleDur},
		{Name: "telephone-event", PayloadType: 100, SampleRate: 8000, NumChannels: 1, SampleDur: CodecTelephoneEvent8000.SampleDur},
		CodecAudioAlaw,
	}
	n := m.updateRemoteCodecs(remote, true)
	require.Equal(t, 3, n, "peer offered opus, telephone-event and PCMA and we support all three")

	names := make([]string, 0, len(m.filterCodecs))
	for _, c := range m.filterCodecs {
		names = append(names, c.Name)
	}
	assert.Equal(t, []string{"opus", "telephone-event", "PCMA"}, names)
}

// An answerer echoes the offerer's format numbers (RFC 3264 section 6.1), and the
// RTP read path gates on payload type, so the negotiated codec must carry the
// number the peer actually puts on the wire.
func TestUpdateRemoteCodecsEchoesRemotePayloadType(t *testing.T) {
	m := negotiateSession(CodecAudioAlaw, CodecTelephoneEvent8000)

	remote := []Codec{
		CodecAudioAlaw,
		{Name: "telephone-event", PayloadType: 100, SampleRate: 8000, NumChannels: 1, SampleDur: CodecTelephoneEvent8000.SampleDur},
	}
	require.Equal(t, 2, m.updateRemoteCodecs(remote, true))

	te := m.filterCodecs[1]
	assert.Equal(t, uint8(100), te.PayloadType, "we must answer with the offerer's telephone-event number")
	assert.Equal(t, CodecTelephoneEvent8000.SampleDur, te.SampleDur, "local framing is kept")
}

// A remote codec that shares a payload type with a local one but is a different
// format must not be treated as a match.
func TestUpdateRemoteCodecsRejectsUnknownFormatOnKnownPayloadType(t *testing.T) {
	m := negotiateSession(CodecAudioAlaw, CodecAudioOpus)

	remote := []Codec{
		{Name: "speex", PayloadType: 96, SampleRate: 8000, NumChannels: 1},
	}
	assert.Equal(t, 0, m.updateRemoteCodecs(remote, true))
}

// A sample rate difference is a different format, even under the same name.
func TestUpdateRemoteCodecsRequiresEqualSampleRate(t *testing.T) {
	m := negotiateSession(CodecAudioAlaw, CodecTelephoneEvent8000)

	remote := []Codec{
		CodecAudioAlaw,
		{Name: "telephone-event", PayloadType: 96, SampleRate: 16000, NumChannels: 1},
	}
	require.Equal(t, 1, m.updateRemoteCodecs(remote, true))
	assert.Equal(t, "PCMA", m.filterCodecs[0].Name)
}

// SDPCodecPreferLocalOrder applies to the answerer only. A UAC applying the
// answer to its own INVITE is the offerer, and must not re-rank what the peer
// already chose. The session id cannot tell the two apart: it is zero both for a
// UAS applying its first offer and for a UAC applying its first answer.
func TestRemoteSDPRoleDecidesCodecOrder(t *testing.T) {
	prev := SDPCodecPreferLocalOrder
	SDPCodecPreferLocalOrder = 1
	t.Cleanup(func() { SDPCodecPreferLocalOrder = prev })

	body := negotiateBody(
		"v=0",
		"o=- 1 1 IN IP4 198.51.100.7",
		"s=-",
		"c=IN IP4 198.51.100.7",
		"t=0 0",
		"m=audio 5004 RTP/AVP 0 8",
		"a=rtpmap:0 PCMU/8000",
		"a=rtpmap:8 PCMA/8000",
	)

	// Answerer: the body is an offer, so our local order wins.
	answerer := negotiateSession(CodecAudioAlaw, CodecAudioUlaw)
	require.NoError(t, answerer.RemoteSDP(body))
	assert.Equal(t, []Codec{CodecAudioAlaw, CodecAudioUlaw}, answerer.filterCodecs)

	// Offerer: the body answers us. It is the peer's decision and its order is
	// the negotiation result.
	offerer := negotiateSession(CodecAudioAlaw, CodecAudioUlaw)
	offerer.RemoteSDPIsAnswer = true
	require.NoError(t, offerer.RemoteSDP(body))
	assert.Equal(t, []Codec{CodecAudioUlaw, CodecAudioAlaw}, offerer.filterCodecs,
		"the answer lists PCMU first; re-ranking it discards the answer")
}

// Fork carries the role, because it is called from inside the re-negotiation
// path where the fork itself is not reachable to configure.
func TestForkCarriesRole(t *testing.T) {
	m := negotiateSession(CodecAudioAlaw)
	m.RemoteSDPIsAnswer = true
	assert.True(t, m.Fork().RemoteSDPIsAnswer)
}

// Port 0 means the stream is rejected (RFC 3264 section 6), and it is also what
// an unparseable port leaves behind. Latching it answered the call and then sat
// mute.
func TestRemoteSDPRejectsUnusableDestination(t *testing.T) {
	for _, tc := range []struct{ name, mline, cline string }{
		{"rejected stream", "m=audio 0 RTP/AVP 8", "c=IN IP4 198.51.100.7"},
		{"unparseable port", "m=audio notaport RTP/AVP 8", "c=IN IP4 198.51.100.7"},
		{"non IP address", "m=audio 5004 RTP/AVP 8", "c=IN FQDN host.example.com"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			m := negotiateSession(CodecAudioAlaw)
			err := m.RemoteSDP(negotiateBody(
				"v=0",
				"o=- 1 1 IN IP4 198.51.100.7",
				"s=-",
				tc.cline,
				"t=0 0",
				tc.mline,
				"a=rtpmap:8 PCMA/8000",
			))
			require.ErrorIs(t, err, ErrNoCommonMedia)
			assert.Zero(t, m.Raddr.Port, "an unusable destination must not be latched")
		})
	}
}

// A peer offering codecs we cannot meet is the peer's problem and should be
// classified as such (488), not counted as an internal error.
func TestRemoteSDPClassifiesNegotiationFailures(t *testing.T) {
	tests := []struct {
		name  string
		mline string
		want  error
	}{
		{"no common codec", "m=audio 5004 RTP/AVP 9", ErrNoCommonCodec},
		{"unsupported transport", "m=audio 5004 RTP/FOO 8", ErrNoCommonMedia},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			m := negotiateSession(CodecAudioAlaw)
			err := m.RemoteSDP(negotiateBody(
				"v=0",
				"o=- 1 1 IN IP4 198.51.100.7",
				"s=-",
				"c=IN IP4 198.51.100.7",
				"t=0 0",
				tc.mline,
			))
			require.ErrorIs(t, err, tc.want)
		})
	}
}

// An offer that carries only a dynamic format we support, numbered by the peer,
// must negotiate rather than be rejected as having no common codec.
func TestRemoteSDPDynamicOnlyOfferNegotiates(t *testing.T) {
	m := negotiateSession(CodecAudioOpus)

	err := m.RemoteSDP(negotiateBody(
		"v=0",
		"o=- 1 1 IN IP4 198.51.100.7",
		"s=-",
		"c=IN IP4 198.51.100.7",
		"t=0 0",
		"m=audio 5004 RTP/AVP 111",
		"a=rtpmap:111 opus/48000/2",
	))
	require.NoError(t, err)
	require.Len(t, m.filterCodecs, 1)
	assert.Equal(t, uint8(111), m.filterCodecs[0].PayloadType)
}
