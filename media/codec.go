// SPDX-License-Identifier: BSD-2-Clause
// Copyright (C) 2024 Emir Aganovic

package media

import (
	"strconv"
	"time"

	"github.com/emiago/diago/media/sdp"
	"github.com/rs/zerolog/log"
)

var (
	CodecAudioUlaw = Codec{PayloadType: 0, SampleRate: 8000, SampleDur: 20 * time.Millisecond}
	CodecAudioAlaw = Codec{PayloadType: 8, SampleRate: 8000, SampleDur: 20 * time.Millisecond}
)

type Codec struct {
	PayloadType uint8
	SampleRate  uint32
	SampleDur   time.Duration
}

func (c *Codec) SampleTimestamp() uint32 {
	return uint32(float64(c.SampleRate) * c.SampleDur.Seconds())
}

func CodecFromSession(s *MediaSession) Codec {
	f := s.Formats[0]
	c := Codec{
		PayloadType: sdp.FormatNumeric(f),
		SampleRate:  8000,
		SampleDur:   20 * time.Millisecond,
	}
	switch f {
	case sdp.FORMAT_TYPE_ALAW:
	case sdp.FORMAT_TYPE_ULAW:
	default:
		s.log.Warn().Str("format", f).Msg("Unsupported format. Using default clock rate")
	}
	return c
}

func CodecFromPayloadType(payloadType uint8) Codec {
	c := Codec{
		PayloadType: uint8(payloadType),
		SampleRate:  8000,
		SampleDur:   20 * time.Millisecond,
	}

	f := strconv.Itoa(int(payloadType))
	switch f {
	case sdp.FORMAT_TYPE_ALAW:
	case sdp.FORMAT_TYPE_ULAW:
	default:
		// For now
		log.Warn().Str("format", f).Msg("Unsupported format. Using default clock rate")

	}
	return c
}
