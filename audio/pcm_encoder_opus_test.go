//go:build with_opus_c

// SPDX-License-Identifier: MPL-2.0

package audio

import (
	"testing"

	"github.com/emiago/diago/media"
	"github.com/stretchr/testify/require"
)

// opusNegotiatedAt returns opus as negotiation leaves it once a peer numbered it
// pt. Only the payload type moves: the negotiated entry keeps our local framing.
func opusNegotiatedAt(pt uint8) media.Codec {
	c := media.CodecAudioOpus
	c.PayloadType = pt
	return c
}

// Each side numbers a dynamic format independently from 96-127 (RFC 3551 section
// 3) and we answer with the peer's number (RFC 3264 section 6.1), so the payload
// type reaching the codec dispatch is the peer's. The format is identified by its
// encoding name (RFC 4855 section 3). 111 is what WebRTC offers, 107 Asterisk.
func TestPCMDecoderInitOpusAtNegotiatedPayloadType(t *testing.T) {
	for _, pt := range []uint8{96, 107, 111} {
		dec := PCMDecoder{}
		require.NoError(t, dec.Init(opusNegotiatedAt(pt)), "opus numbered %d must decode", pt)
		require.NotNil(t, dec.DecoderTo)
	}
}

func TestPCMEncoderInitOpusAtNegotiatedPayloadType(t *testing.T) {
	for _, pt := range []uint8{96, 107, 111} {
		enc := PCMEncoder{}
		require.NoError(t, enc.Init(opusNegotiatedAt(pt)), "opus numbered %d must encode", pt)
		require.NotNil(t, enc.EncoderTo)
	}
}

// A peer numbering opus onto the telephone-event constant must still get opus.
func TestPCMCodecInitOpusAtTelephoneEventConstant(t *testing.T) {
	dec := PCMDecoder{}
	require.NoError(t, dec.Init(opusNegotiatedAt(101)))

	enc := PCMEncoder{}
	require.NoError(t, enc.Init(opusNegotiatedAt(101)))
}
