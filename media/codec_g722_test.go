// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package media

import (
	"net"
	"testing"

	"github.com/emiago/diago/media/sdp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestCodecG722Clock(t *testing.T) {
	// RFC 3551 froze the G.722 RTP clock at 8000 even though the codec samples at
	// 16 kHz. 8000 * 20ms = 160 ticks per packet.
	assert.EqualValues(t, 8000, CodecAudioG722.SampleRate)
	assert.EqualValues(t, 160, CodecAudioG722.SampleTimestamp())

	// DTMF writer rejects any codec that is not on the 8000 clock, so
	// telephone-event must share the G.722 clock.
	assert.Equal(t, CodecTelephoneEvent8000.SampleRate, CodecAudioG722.SampleRate)
}

func TestCodecG722FromPayloadType(t *testing.T) {
	codec, err := CodecAudioFromPayloadType(9)
	require.NoError(t, err)
	assert.Equal(t, CodecAudioG722, codec)

	assert.Equal(t, CodecAudioG722, mapSupportedCodec(sdp.FORMAT_TYPE_G722))
}

func TestCodecG722SDPRtpmap(t *testing.T) {
	codecs := []Codec{CodecAudioG722, CodecTelephoneEvent8000}
	ip := net.IPv4(127, 0, 0, 1)
	out := string(generateSDPForAudio(1, 1, "RTP/AVP", ip, ip, 10000, sdp.ModeSendrecv, codecs, sdesInline{}, nil))

	assert.Contains(t, out, "m=audio 10000 RTP/AVP 9 101")
	assert.Contains(t, out, "a=rtpmap:9 G722/8000")
	// Clock must never be the 16 kHz sampling rate and the line must not carry the
	// encoding parameters suffix the generic default would append.
	assert.NotContains(t, out, "G722/16000")
	assert.NotContains(t, out, "G722/8000/1")
	assert.Contains(t, out, "a=rtpmap:101 telephone-event/8000")
}

func TestCodecsFromSDPReadG722Static(t *testing.T) {
	// G.722 is a static payload type, so a peer may offer bare RTP/AVP 9 with no
	// a=rtpmap line at all.
	codecs := make([]Codec, 4)
	n, err := CodecsFromSDPRead([]string{"9"}, nil, codecs)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	assert.Equal(t, CodecAudioG722, codecs[0])

	// Mixed static only offer, still no rtpmap.
	codecs = make([]Codec, 4)
	n, err = CodecsFromSDPRead([]string{"0", "8", "9"}, nil, codecs)
	require.NoError(t, err)
	require.Equal(t, 3, n)
	assert.Equal(t, []Codec{CodecAudioUlaw, CodecAudioAlaw, CodecAudioG722}, codecs[:n])

	// A peer that mislabels the rtpmap clock must not desynchronize us: the static
	// payload type wins and keeps the RFC 3551 clock.
	codecs = make([]Codec, 4)
	n, err = CodecsFromSDPRead([]string{"9"}, []string{"rtpmap:9 G722/16000"}, codecs)
	require.NoError(t, err)
	require.Equal(t, 1, n)
	assert.EqualValues(t, 8000, codecs[0].SampleRate)
}
