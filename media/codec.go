// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package media

import (
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/emiago/diago/media/sdp"
)

var (
	// Here are some codec constants that can be reused
	CodecAudioUlaw          = Codec{PayloadType: 0, SampleRate: 8000, SampleDur: 20 * time.Millisecond, NumChannels: 1, Name: "PCMU"}
	CodecAudioAlaw          = Codec{PayloadType: 8, SampleRate: 8000, SampleDur: 20 * time.Millisecond, NumChannels: 1, Name: "PCMA"}
	CodecAudioOpus          = Codec{PayloadType: 96, SampleRate: 48000, SampleDur: 20 * time.Millisecond, NumChannels: 2, Name: "opus"}
	CodecTelephoneEvent8000 = Codec{PayloadType: 101, SampleRate: 8000, SampleDur: 20 * time.Millisecond, NumChannels: 1, Name: "telephone-event"}
)

type Codec struct {
	Name        string
	PayloadType uint8
	SampleRate  uint32
	SampleDur   time.Duration
	NumChannels int // 1 or 2
}

func (c *Codec) String() string {
	return fmt.Sprintf("name=%s pt=%d rate=%d dur=%s channels=%d", c.Name, c.PayloadType, c.SampleRate, c.SampleDur.String(), c.NumChannels)
}

// SampleTimestamp returns number of samples as RTP Timestamp measure
func (c *Codec) SampleTimestamp() uint32 {
	return uint32(float64(c.SampleRate) * c.SampleDur.Seconds())
}

// Samples16 returns PCM 16 bit samples size
func (c *Codec) Samples16() int {
	return c.SamplesPCM(16)
}

// Samples is samples in pcm
func (c *Codec) SamplesPCM(bitSize int) int {
	return bitSize / 8 * int(float64(c.SampleRate)*c.SampleDur.Seconds()) * c.NumChannels
}

func CodecFromSession(s *MediaSession) Codec {
	for _, codec := range s.Codecs {
		if codec.Name == "telephone-event" {
			continue
		}

		return codec
	}

	return s.Codecs[0]
}

// Deprecated: Use CodecAudioFromPayloadType
func CodecFromPayloadType(payloadType uint8) Codec {
	f := strconv.Itoa(int(payloadType))
	return mapSupportedCodec(f)
}

func CodecAudioFromPayloadType(payloadType uint8) (Codec, error) {
	f := strconv.Itoa(int(payloadType))
	switch f {
	case sdp.FORMAT_TYPE_ALAW:
		return CodecAudioAlaw, nil
	case sdp.FORMAT_TYPE_ULAW:
		return CodecAudioUlaw, nil
	case sdp.FORMAT_TYPE_OPUS:
		return CodecAudioOpus, nil
	case sdp.FORMAT_TYPE_TELEPHONE_EVENT:
		return CodecTelephoneEvent8000, nil
	}
	return Codec{}, fmt.Errorf("non supported codec: %d", payloadType)
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
		slog.Warn("Unsupported format. Using default clock rate", "format", f)
	}
	// Format as default
	pt, err := sdp.FormatNumeric(f)
	if err != nil {
		slog.Warn("Format is non numeric value", "format", f)
	}
	return Codec{
		PayloadType: pt,
		SampleRate:  8000,
		SampleDur:   20 * time.Millisecond,
	}
}

// func CodecsFromSDP(log *slog.Logger, sd sdp.SessionDescription, codecsAudio []Codec) error {
// 	md, err := sd.MediaDescription("audio")
// 	if err != nil {
// 		return err
// 	}

// 	codecs := make([]Codec, len(md.Formats))
// 	attrs := sd.Values("a")
// 	n, err := CodecsFromSDPRead(log, md, attrs, codecs)
// 	if err != nil {
// 		return err
// 	}
// 	codecs = codecs[:n]
// }

// CodecsFromSDP will try to parse as much as possible, but it will return also error in case
// some properties could not be read
// You can take what is parsed or return error
func CodecsFromSDPRead(formats []string, attrs []string, codecsAudio []Codec) (int, error) {
	n := 0
	var rerr error
	for _, f := range formats {
		if f == "0" {
			codecsAudio[n] = CodecAudioUlaw
			n++
			continue
		}

		if f == "8" {
			codecsAudio[n] = CodecAudioAlaw
			n++
			continue
		}

		pt64, err := strconv.ParseUint(f, 10, 8)
		if err != nil {
			rerr = errors.Join(rerr, fmt.Errorf("format type failed to conv to integer, skipping f=%s: %w", f, err))
			continue
		}
		pt := uint8(pt64)

		for _, a := range attrs {
			// a=rtpmap:<payload type> <encoding name>/<clock rate> [/<encoding parameters>]
			pref := "rtpmap:" + f + " "
			if strings.HasPrefix(a, pref) {
				// Check properties of this codec
				str := a[len(pref):]
				// TODO use more efficient reading props
				props := strings.Split(str, " ")
				firstProp := props[0]
				propsCodec := strings.Split(firstProp, "/")
				if len(propsCodec) < 2 {
					rerr = errors.Join(rerr, fmt.Errorf("bad rtmap property a=%s", a))
					continue
				}

				encodingName := propsCodec[0]
				sampleRateStr := propsCodec[1]
				sampleRate64, err := strconv.ParseUint(sampleRateStr, 10, 32)
				if err != nil {
					rerr = errors.Join(rerr, fmt.Errorf("sample rate failed to parse a=%s: %w", a, err))
					continue
				}

				codec := Codec{
					Name:        encodingName,
					PayloadType: pt,
					SampleRate:  uint32(sampleRate64),
					// TODO check ptime ?
					SampleDur:   20 * time.Millisecond,
					NumChannels: 1,
				}

				if len(propsCodec) == 3 {
					numChannels, err := strconv.ParseUint(propsCodec[2], 10, 32)
					if err == nil {
						codec.NumChannels = int(numChannels)
					}
				}
				codecsAudio[n] = codec
				n++
			}
		}
	}
	return n, nil
}
