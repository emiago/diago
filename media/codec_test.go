// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2026, Emir Aganovic

package media

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestCodecsFromSDPReadFormatParameters(t *testing.T) {
	formats := []string{"99", "9", "18", "0", "8", "101", "110"}
	attrs := []string{
		"fmtp:199 wrong-payload=1",
		"fmtp:99  octet-align=1; mode-set=0,1,2  ", // Before rtpmap.
		"rtpmap:99 AMR-WB/16000",
		"fmtp:18 annexb=no", // Static payload without rtpmap.
		"fmtp:0 test=pcmu",
		"fmtp:8 test=pcma",
		"rtpmap:101 telephone-event/8000",
		"fmtp:101\t0-15",
		"fmtp:110 orphan=1",      // No mapping for this dynamic payload.
		"rtpmap:99 AMR-WB/16000", // Duplicate mapping must not duplicate codec.
	}
	codecs := make([]Codec, len(formats))
	n, err := CodecsFromSDPRead(formats, attrs, codecs)
	require.NoError(t, err)
	require.Equal(t, 6, n)
	want := []Codec{
		{Name: "AMR-WB", PayloadType: 99, SampleRate: 16000, SampleDur: 20 * time.Millisecond, NumChannels: 1, FMTP: "octet-align=1; mode-set=0,1,2"},
		CodecAudioG722, CodecAudioG729, CodecAudioUlaw, CodecAudioAlaw, CodecTelephoneEvent8000,
	}
	want[2].FMTP = "annexb=no"
	want[3].FMTP = "test=pcmu"
	want[4].FMTP = "test=pcma"
	want[5].FMTP = "0-15"
	require.Equal(t, want, codecs[:n])
}

func TestCodecsFromSDPReadErrors(t *testing.T) {
	formats := []string{"invalid", "100", "101", "102", "18"}
	codecs := make([]Codec, len(formats))
	n, err := CodecsFromSDPRead(formats, []string{
		"rtpmap:100 ", "rtpmap:101 broken", "rtpmap:102 opus/bad/2",
	}, codecs)
	require.Error(t, err)
	require.Equal(t, 1, n)
	require.Equal(t, CodecAudioG729, codecs[0])
}

func TestCodecStaticPayloadTypes(t *testing.T) {
	for _, want := range []Codec{CodecAudioG722, CodecAudioG729} {
		codec, err := CodecAudioFromPayloadType(want.PayloadType)
		require.NoError(t, err)
		require.Equal(t, want, codec)
		require.Equal(t, want, CodecFromPayloadType(want.PayloadType))
	}
}

func TestCodecFmtpGet(t *testing.T) {
	f := CodecFmtp("Octet-Align=1; mode-set=0,1,2;0-15")
	v, ok := f.Get("octet-align")
	require.True(t, ok)
	require.Equal(t, "1", v)
	v, ok = f.Get("0-15")
	require.True(t, ok)
	require.Equal(t, "", v)
	_, ok = f.Get("mode")
	require.False(t, ok)
	require.Equal(t, "0,1,2", f.GetDefault("mode-set", "all"))
	require.Equal(t, "0", f.GetDefault("crc", "0"))
}

func TestCodecFmtpWith(t *testing.T) {
	for _, tt := range []struct {
		name       string
		fmtp       CodecFmtp
		key, value string
		want       CodecFmtp
	}{
		{name: "empty", key: "octet-align", value: "1", want: "octet-align=1"},
		{name: "append", fmtp: "mode-set=0,1,2", key: "octet-align", value: "1", want: "mode-set=0,1,2; octet-align=1"},
		{name: "replace case-insensitive keeps order", fmtp: "Octet-Align=0;mode-set=0,1,2", key: "octet-align", value: "1", want: "octet-align=1; mode-set=0,1,2"},
		{name: "replace only first", fmtp: "a=1; a=2", key: "a", value: "3", want: "a=3; a=2"},
		{name: "empty value writes bare key", fmtp: "a=1;", key: "b", want: "a=1; b"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			f := tt.fmtp.With(tt.key, tt.value)
			require.Equal(t, tt.want, f)
			v, ok := f.Get(tt.key)
			require.True(t, ok)
			require.Equal(t, tt.value, v)
		})
	}
}
