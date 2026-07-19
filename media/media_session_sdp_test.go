// SPDX-License-Identifier: MPL-2.0

package media

import (
	"net"
	"strconv"
	"strings"
	"testing"

	"github.com/emiago/diago/media/sdp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func sdpForCodecs(codecs ...Codec) string {
	ip := net.IPv4(127, 0, 0, 1)
	return string(generateSDPForAudio(1, 1, "RTP/AVP", ip, ip, 5004, sdp.ModeSendrecv, codecs, sdesInline{}, nil, nil))
}

// opusAt returns opus as it looks after negotiation with a peer that numbered it
// pt. Only the number moves: negotiatedCodec keeps our local framing.
func opusAt(pt uint8) Codec {
	c := CodecAudioOpus
	c.PayloadType = pt
	return c
}

func telephoneEventAt(pt uint8) Codec {
	c := CodecTelephoneEvent8000
	c.PayloadType = pt
	return c
}

// A dynamic format carries whatever number the peer chose (RFC 3551 section 3,
// RFC 3264 section 6.1), so the lines describing it must be rendered from the
// format's identity and stamped with that number.
func TestGenerateSDPOpusAtNegotiatedPayloadType(t *testing.T) {
	for _, pt := range []uint8{96, 107, 111} {
		out := sdpForCodecs(opusAt(pt), CodecAudioAlaw)

		assert.Contains(t, out, "a=rtpmap:"+itoa(pt)+" opus/48000/2",
			"opus rtpmap must be emitted at the negotiated number")
		// RFC 7587 section 7: providing 0 when FEC cannot be used on the receiving
		// side is RECOMMENDED. Keying on the package constant dropped this line for
		// every number but 96.
		assert.Contains(t, out, "a=fmtp:"+itoa(pt)+" useinbandfec=0",
			"opus fmtp must be emitted at the negotiated number")
		assert.Contains(t, out, "m=audio 5004 RTP/AVP "+itoa(pt)+" 8")
	}
}

// Opus at a number that collides with the telephone-event constant must still be
// described as opus.
func TestGenerateSDPOpusAtTelephoneEventConstant(t *testing.T) {
	out := sdpForCodecs(opusAt(101), telephoneEventAt(100))

	assert.Contains(t, out, "a=rtpmap:101 opus/48000/2")
	assert.Contains(t, out, "a=fmtp:101 useinbandfec=0")
	assert.NotContains(t, out, "a=rtpmap:101 telephone-event/8000",
		"101 is opus here, the package constant must not decide this")
	assert.NotContains(t, out, "a=fmtp:101 0-16")

	assert.Contains(t, out, "a=rtpmap:100 telephone-event/8000")
	assert.Contains(t, out, "a=fmtp:100 0-16")
}

// Telephone-event at a number that collides with the opus constant must not
// inherit opus' rtpmap or its fmtp.
func TestGenerateSDPTelephoneEventAtOpusConstant(t *testing.T) {
	out := sdpForCodecs(opusAt(111), telephoneEventAt(96))

	assert.Contains(t, out, "a=rtpmap:96 telephone-event/8000")
	assert.Contains(t, out, "a=fmtp:96 0-16")
	assert.NotContains(t, out, "a=rtpmap:96 opus/48000/2",
		"96 is telephone-event here, advertising opus on it misdescribes the stream")
	assert.NotContains(t, out, "a=fmtp:96 useinbandfec=0",
		"opus parameters must not land on the DTMF payload type")

	// And opus must be advertised exactly once, at its negotiated number.
	assert.Contains(t, out, "a=rtpmap:111 opus/48000/2")
	assert.Equal(t, 1, strings.Count(out, "opus/48000/2"), "opus must be advertised once")
}

// Telephone-event is rendered from its identity, so it keeps the canonical
// rtpmap with no encoding parameters suffix at any number.
func TestGenerateSDPTelephoneEventCanonicalRtpmap(t *testing.T) {
	out := sdpForCodecs(CodecAudioAlaw, telephoneEventAt(100))

	assert.Contains(t, out, "a=rtpmap:100 telephone-event/8000")
	assert.NotContains(t, out, "telephone-event/8000/1",
		"the generic default appends the channel count, which is not the canonical form")
}

// Static payload types are frozen by the RTP/AVP registry (RFC 3551 section 6).
// This is the control: the everyday PSTN answer must be byte for byte what it
// was before dynamic formats stopped being keyed by number.
func TestGenerateSDPStaticCodecsUnchanged(t *testing.T) {
	out := sdpForCodecs(CodecAudioUlaw, CodecAudioAlaw, CodecAudioG722, CodecTelephoneEvent8000)

	require.Contains(t, out, "m=audio 5004 RTP/AVP 0 8 9 101")
	assert.Contains(t, out, "a=rtpmap:0 PCMU/8000")
	assert.Contains(t, out, "a=rtpmap:8 PCMA/8000")
	assert.Contains(t, out, "a=rtpmap:9 G722/8000")
	assert.Contains(t, out, "a=rtpmap:101 telephone-event/8000")
	assert.Contains(t, out, "a=fmtp:101 0-16")

	// None of the static lines may gain an encoding parameters suffix.
	assert.NotContains(t, out, "PCMU/8000/1")
	assert.NotContains(t, out, "PCMA/8000/1")
	assert.NotContains(t, out, "G722/8000/1")
	assert.NotContains(t, out, "G722/16000")
}

// Encoding names are case insensitive (RFC 4566 section 6). InitWithSDP writes
// peer spelled names straight into the codec list, bypassing negotiation.
func TestGenerateSDPDynamicCodecNameCaseInsensitive(t *testing.T) {
	opus := opusAt(111)
	opus.Name = "OPUS"
	te := telephoneEventAt(96)
	te.Name = "TELEPHONE-EVENT"

	out := sdpForCodecs(opus, te)

	assert.Contains(t, out, "a=fmtp:111 useinbandfec=0")
	assert.Contains(t, out, "a=rtpmap:96 telephone-event/8000")
	assert.Contains(t, out, "a=fmtp:96 0-16")
}

// An unknown format keeps the generic rendering.
func TestGenerateSDPUnknownCodecGenericRtpmap(t *testing.T) {
	speex := Codec{Name: "speex", PayloadType: 97, SampleRate: 16000, NumChannels: 1}
	out := sdpForCodecs(speex)

	assert.Contains(t, out, "a=rtpmap:97 speex/16000/1")
}

func itoa(pt uint8) string {
	return strconv.Itoa(int(pt))
}
