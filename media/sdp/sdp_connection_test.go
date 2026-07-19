// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package sdp

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// offer builds an SDP body from lines, CRLF terminated the way a peer sends one.
func offer(lines ...string) []byte {
	return []byte(strings.Join(lines, "\r\n") + "\r\n")
}

// TestConnectionInformationMediaLevel checks RFC 4566 section 5.7 precedence: a
// c= inside the media section overrides the session level one. A gateway that
// publishes its signalling address at session level and a separate media address
// on the audio section is ordinary, and reading the session level c= sends every
// RTP packet to a host that is not expecting it.
func TestConnectionInformationMediaLevel(t *testing.T) {
	body := offer(
		"v=0",
		"o=- 1 1 IN IP4 203.0.113.1",
		"s=-",
		"c=IN IP4 203.0.113.1", // session level: signalling host
		"t=0 0",
		"m=audio 5004 RTP/AVP 8",
		"c=IN IP4 198.51.100.7", // media level: governs the audio stream
		"a=rtpmap:8 PCMA/8000",
	)

	sd := SessionDescription{}
	require.NoError(t, Unmarshal(body, &sd))

	ci, err := sd.ConnectionInformation()
	require.NoError(t, err)
	require.Equal(t, "198.51.100.7", ci.IP.String())
}

// TestConnectionInformationSessionLevel pins the fallback, which is the common
// case: most peers publish exactly one session level c=.
func TestConnectionInformationSessionLevel(t *testing.T) {
	sd := SessionDescription{}
	require.NoError(t, Unmarshal(offer(
		"v=0",
		"c=IN IP4 203.0.113.9",
		"t=0 0",
		"m=audio 5004 RTP/AVP 8",
		"a=rtpmap:8 PCMA/8000",
	), &sd))

	ci, err := sd.ConnectionInformation()
	require.NoError(t, err)
	require.Equal(t, "203.0.113.9", ci.IP.String())
}

// TestConnectionInformationMultipleMediaLines documents why taking the last c=
// is sound. Line order alone cannot bind a c= to its section, so with several m=
// lines the last c= may belong to some other stream. MediaDescription already
// rejects that body per RFC 3264, and ConnectionInformation rejects it for the
// same reason rather than quietly answering with the video section's address.
func TestConnectionInformationMultipleMediaLines(t *testing.T) {
	sd := SessionDescription{}
	require.NoError(t, Unmarshal(offer(
		"v=0",
		"c=IN IP4 203.0.113.9",
		"t=0 0",
		"m=audio 5004 RTP/AVP 8",
		"m=video 5006 RTP/AVP 96",
		"c=IN IP4 192.0.2.44", // video's address, must never govern audio
	), &sd))

	_, err := sd.ConnectionInformation()
	require.Error(t, err)

	_, err = sd.MediaDescription("audio")
	require.Error(t, err)
}

// TestConnectionInformationMalformed covers a c= line that is truncated before
// the address. fields[1] and fields[2] were read with no field count guard, so a
// remote peer sending "c=IN" panicked the process.
func TestConnectionInformationMalformed(t *testing.T) {
	for _, v := range []string{"IN", "IN IP4", ""} {
		sd := SessionDescription{"c": []string{v}}
		_, err := sd.ConnectionInformation()
		require.Error(t, err, "c=%s must error, not panic", v)
	}
}

// TestUnmarshalMalformedNoPanic pins the parser against bodies a peer can simply
// send. A bare LF line reached nextLine as a 1 byte string and indexed the CRLF
// check at -1. There is no recover on the receive path, so either panic takes the
// process down along with every concurrent call.
func TestUnmarshalMalformedNoPanic(t *testing.T) {
	bodies := map[string][]byte{
		"bare LF line":      []byte("v=0\r\n\nm=audio 5004 RTP/AVP 8\r\n"),
		"blank line at end": []byte("v=0\r\nm=audio 5004 RTP/AVP 8\r\n\n"),
		"truncated c=":      []byte("v=0\r\nc=IN\r\nm=audio 5004 RTP/AVP 8\r\n"),
		"empty c=":          []byte("v=0\r\nc=\r\nm=audio 5004 RTP/AVP 8\r\n"),
		"media truncated c=": []byte("v=0\r\nc=IN IP4 203.0.113.1\r\n" +
			"m=audio 5004 RTP/AVP 8\r\nc=IN IP4\r\n"),
		"empty body": {},
		"lone LF":    []byte("\n"),
		"lone CR":    []byte("\r"),
	}

	for name, body := range bodies {
		t.Run(name, func(t *testing.T) {
			// An error is an acceptable outcome for all of these. A panic is not.
			sd := SessionDescription{}
			_ = Unmarshal(body, &sd)
			_, _ = sd.ConnectionInformation()
			_, _ = sd.MediaDescription("audio")
		})
	}
}
