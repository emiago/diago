// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package media

import (
	"fmt"
	"strconv"
	"time"

	"github.com/emiago/diago/media/sdp"
	"github.com/rs/zerolog/log"
)

var (
	// Here are some codec constants that can be reused
	CodecAudioUlaw          = Codec{PayloadType: 0, SampleRate: 8000, SampleDur: 20 * time.Millisecond}
	CodecAudioAlaw          = Codec{PayloadType: 8, SampleRate: 8000, SampleDur: 20 * time.Millisecond}
	CodecAudioOpus          = Codec{PayloadType: 96, SampleRate: 48000, SampleDur: 20 * time.Millisecond}
	CodecTelephoneEvent8000 = Codec{PayloadType: 101, SampleRate: 8000, SampleDur: 20 * time.Millisecond}
)

type Codec struct {
	PayloadType uint8
	SampleRate  uint32
	SampleDur   time.Duration
}

func (c *Codec) String() string {
	return fmt.Sprintf("pt=%d rate=%d dur=%s", c.PayloadType, c.SampleRate, c.SampleDur.String())
}

func (c *Codec) SampleTimestamp() uint32 {
	return uint32(float64(c.SampleRate) * c.SampleDur.Seconds())
}

func CodecFromSession(s *MediaSession) Codec {
	f := s.Formats[0]

	return mapSupportedCodec(f)
}

func CodecFromPayloadType(payloadType uint8) Codec {
	f := strconv.Itoa(int(payloadType))
	return mapSupportedCodec(f)
}

func mapSupportedCodec(f string) Codec {
	// TODO: Here we need to be more explicit like matching sample rate, channels and other

	switch f {
	case sdp.FORMAT_TYPE_ALAW:
		return CodecAudioAlaw
	case sdp.FORMAT_TYPE_ULAW:
		return CodecAudioUlaw
	case sdp.FORMAT_TYPE_OPUS:
		return CodecAudioOpus
	case sdp.FORMAT_TYPE_TELEPHONE_EVENT:
		return CodecTelephoneEvent8000
	default:
		log.Warn().Str("format", f).Msg("Unsupported format. Using default clock rate")
	}
	// Format as default
	pt, err := sdp.FormatNumeric(f)
	if err != nil {
		log.Warn().Str("format", f).Msg("Format is non numeric value")
	}
	return Codec{
		PayloadType: pt,
		SampleRate:  8000,
		SampleDur:   20 * time.Millisecond,
	}
}
