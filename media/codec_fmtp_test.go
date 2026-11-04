// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package media

import (
	"net"
	"strings"
	"testing"
	"time"

	"github.com/emiago/diago/media/sdp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// AMR-WB in both RFC 4867 payload formats, as a local configuration would
// offer it.
var (
	codecAMRWB      = Codec{Name: "AMR-WB", PayloadType: 118, SampleRate: 16000, SampleDur: 20 * time.Millisecond, NumChannels: 1, Fmtp: "octet-align=0"}
	codecAMRWBOctet = Codec{Name: "AMR-WB", PayloadType: 119, SampleRate: 16000, SampleDur: 20 * time.Millisecond, NumChannels: 1, Fmtp: "octet-align=1"}
)

func fmtpSession(codecs ...Codec) *MediaSession {
	return &MediaSession{
		Codecs: codecs,
		Laddr:  net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1234},
		Mode:   sdp.ModeSendrecv,
	}
}

func fmtpSDP(formats string, attrs ...string) []byte {
	lines := []string{"v=0", "o=- 1 1 IN IP4 127.0.0.1", "s=-", "c=IN IP4 127.0.0.1", "t=0 0", "m=audio 5678 RTP/AVP " + formats}
	for _, a := range attrs {
		lines = append(lines, "a="+a)
	}
	lines = append(lines, "a=sendrecv")
	return []byte(strings.Join(lines, "\r\n") + "\r\n")
}

func TestOfferWritesLocalFmtp(t *testing.T) {
	offer := string(fmtpSession(codecAMRWB, codecAMRWBOctet).LocalSDP(false))
	assert.Contains(t, offer, "a=rtpmap:118 AMR-WB/16000\r\na=fmtp:118 octet-align=0\r\n")
	assert.Contains(t, offer, "a=rtpmap:119 AMR-WB/16000\r\na=fmtp:119 octet-align=1\r\n")
}

func TestAnswerEchoesOfferedFmtp(t *testing.T) {
	previousOrder := SDPCodecPreferLocalOrder
	t.Cleanup(func() { SDPCodecPreferLocalOrder = previousOrder })
	for _, localOrder := range []int{0, 1} {
		SDPCodecPreferLocalOrder = localOrder
		sess := fmtpSession(CodecAudioUlaw, codecAMRWB, codecAMRWBOctet)
		require.NoError(t, sess.RemoteSDP(fmtpSDP("98", "rtpmap:98 AMR-WB/16000", "fmtp:98 octet-align=1; mode-set=0,1,2"), false))

		require.Len(t, sess.CommonCodecs(), 1, "local order %d: one remote AMR-WB must give one negotiated codec", localOrder)
		negotiated := CodecAudioFromSession(sess)
		assert.Equal(t, uint8(98), negotiated.PayloadType)
		assert.Equal(t, uint8(98), negotiated.PayloadTypeRecv())
		assert.Equal(t, "octet-align=1; mode-set=0,1,2", negotiated.Fmtp)

		answer := string(sess.LocalSDP(true))
		assert.Contains(t, answer, "m=audio 1234 RTP/AVP 98\r\n")
		assert.Contains(t, answer, "a=rtpmap:98 AMR-WB/16000\r\na=fmtp:98 octet-align=1; mode-set=0,1,2\r\n")
	}
}

// TestOffererPairsAnswerWithSameLocalVariant checks that, with several local
// variants of one encoding, the answer is paired with the variant on the same
// payload type, so the receive payload type stays the offered one.
func TestOffererPairsAnswerWithSameLocalVariant(t *testing.T) {
	sess := fmtpSession(codecAMRWB, codecAMRWBOctet)
	sess.LocalSDP(false)
	require.NoError(t, sess.RemoteSDP(fmtpSDP("119", "rtpmap:119 AMR-WB/16000", "fmtp:119 octet-align=1"), true))

	negotiated := CodecAudioFromSession(sess)
	assert.Equal(t, uint8(119), negotiated.PayloadType)
	assert.Equal(t, uint8(119), negotiated.PayloadTypeRecv())
	assert.Equal(t, "octet-align=1", negotiated.Fmtp)
}

func TestDifferentEncodingOnSamePayloadTypeDoesNotMatch(t *testing.T) {
	sess := fmtpSession(codecAMRWB)
	require.Error(t, sess.RemoteSDP(fmtpSDP("118", "rtpmap:118 AMR/8000"), false))
}

func TestStaticPayloadTypesWithoutRtpmap(t *testing.T) {
	codecs := make([]Codec, 2)
	n, err := CodecsFromSDPRead([]string{"9", "18"}, []string{"fmtp:18 annexb=no"}, codecs)
	require.NoError(t, err)
	require.Equal(t, 2, n)
	assert.Equal(t, CodecAudioG722, codecs[0])
	g729 := CodecAudioG729
	g729.Fmtp = "annexb=no"
	assert.Equal(t, g729, codecs[1])
}

func TestCodecsFromSDPReadSkipsPayloadTypesOutsideRTPRange(t *testing.T) {
	codecs := make([]Codec, 3)
	n, _ := CodecsFromSDPRead([]string{"127", "128", "200"}, []string{
		"rtpmap:127 opus/48000/2", "rtpmap:128 opus/48000/2", "rtpmap:200 opus/48000/2",
	}, codecs)
	require.Equal(t, 1, n)
	assert.Equal(t, uint8(127), codecs[0].PayloadType)
}
