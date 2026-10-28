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
	CodecAudioUlaw          = Codec{PayloadType: 0, SampleRate: 8000, SampleDur: 20 * time.Millisecond, NumChannels: 1, Name: "pcmu"}
	CodecAudioAlaw          = Codec{PayloadType: 8, SampleRate: 8000, SampleDur: 20 * time.Millisecond, NumChannels: 1, Name: "pcma"}
	CodecAudioOpus          = Codec{PayloadType: 96, SampleRate: 48000, SampleDur: 20 * time.Millisecond, NumChannels: 2, Name: "opus"}
	CodecTelephoneEvent8000 = Codec{PayloadType: 101, SampleRate: 8000, SampleDur: 20 * time.Millisecond, NumChannels: 1, Name: "telephone-event"}
	// CodecAudioG722 and CodecAudioG729 describe the RFC 3551 static payload
	// types 9 and 18. G.722 uses an 8 kHz RTP clock for 16 kHz audio.
	CodecAudioG722 = Codec{PayloadType: 9, SampleRate: 8000, SampleDur: 20 * time.Millisecond, NumChannels: 1, Name: "g722"}
	CodecAudioG729 = Codec{PayloadType: 18, SampleRate: 8000, SampleDur: 20 * time.Millisecond, NumChannels: 1, Name: "g729"}
	// CodecComfortNoise8000 describes the static RFC 3389 comfort-noise payload.
	// It is an auxiliary RTP format and is not included in default SDP offers.
	CodecComfortNoise8000 = Codec{PayloadType: 13, SampleRate: 8000, NumChannels: 1, Name: "cn"}
)

type Codec struct {
	Name        string
	PayloadType uint8
	SampleRate  uint32
	SampleDur   time.Duration
	NumChannels int // 1 or 2
	// Fmtp holds the format parameters (the value of a=fmtp:<payload type>),
	// e.g. "octet-align=1" for AMR-WB. It is parsed from remote SDP and
	// written to local SDP. A negotiated codec carries the remote parameters.
	Fmtp string

	payloadTypeRecv *uint8 // Only set for asymmetric negotiated payload mappings.
}

// PayloadTypeRecv returns the receive payload type of a negotiated codec.
// It equals PayloadType unless the peer answered with a different mapping.
func (c Codec) PayloadTypeRecv() uint8 {
	if c.payloadTypeRecv != nil {
		return *c.payloadTypeRecv
	}
	return c.PayloadType
}

// negotiate returns the local codec adapted to the matching remote one: it
// takes the remote payload type and format parameters.
func (c Codec) negotiate(remote Codec, answerer bool) Codec {
	localPayloadType := c.PayloadType
	c.PayloadType = remote.PayloadType
	c.Fmtp = remote.Fmtp
	c.payloadTypeRecv = nil
	if !answerer && localPayloadType != remote.PayloadType {
		c.payloadTypeRecv = &localPayloadType
	}
	return c
}

// matchingCodec returns the index of the codec in codecs with the same
// encoding as c, or -1. Among several matches (e.g. AMR-WB offered both
// octet-aligned and bandwidth-efficient) the one with the same payload type
// is preferred. Codecs marked in used are skipped; used may be nil.
func matchingCodec(codecs []Codec, c Codec, used []bool) int {
	match := -1
	for i, candidate := range codecs {
		if (used != nil && used[i]) || !candidate.Matches(c) {
			continue
		}
		if candidate.PayloadType == c.PayloadType {
			return i
		}
		if match < 0 {
			match = i
		}
	}
	return match
}

// Matches compares codec identity independently of payload type and packet duration.
func (c Codec) Matches(other Codec) bool {
	return strings.EqualFold(c.Name, other.Name) && c.SampleRate == other.SampleRate && c.NumChannels == other.NumChannels
}

func (c *Codec) String() string {
	return fmt.Sprintf("name=%s pt=%d rate=%d dur=%s channels=%d fmtp=%s", c.Name, c.PayloadType, c.SampleRate, c.SampleDur.String(), c.NumChannels, c.Fmtp)
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

func CodecAudioFromSession(s *MediaSession) Codec {
	codec, exists := CodecAudioFromList(s.filterCodecs)
	if !exists {
		return s.Codecs[0]
	}

	return codec
}

// IsAuxiliary reports whether c is an auxiliary RTP format (RFC 4733 events,
// RFC 3389 comfort noise) carried beside the audio rather than decoded as it.
func (c Codec) IsAuxiliary() bool {
	return strings.EqualFold(c.Name, "telephone-event") || strings.EqualFold(c.Name, "CN")
}

func CodecAudioFromList(codecs []Codec) (Codec, bool) {
	for _, codec := range codecs {
		if codec.IsAuxiliary() {
			continue
		}

		return codec, true
	}

	return Codec{}, false
}

// Deprecated: Use CodecAudioFromSession
func CodecFromSession(s *MediaSession) Codec {
	return CodecAudioFromSession(s)
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
		if codec, ok := staticAudioCodec(f); ok {
			codec.Fmtp = formatParameters(f, attrs)
			codecsAudio[n] = codec
			n++
			continue
		}

		pt64, err := strconv.ParseUint(f, 10, 8)
		if err != nil {
			rerr = errors.Join(rerr, fmt.Errorf("format type failed to conv to integer, skipping f=%s: %w", f, err))
			continue
		}
		// RTP payload types are 7 bits (RFC 3550).
		if pt64 > 127 {
			rerr = errors.Join(rerr, fmt.Errorf("payload type %d is out of the RTP range 0-127, skipping", pt64))
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
				codec.Fmtp = formatParameters(f, attrs)
				codecsAudio[n] = codec
				n++
			}
		}
	}
	return n, nil
}

// staticAudioCodec returns the codec of an RFC 3551 static payload type that
// SDP may list without a=rtpmap.
func staticAudioCodec(format string) (Codec, bool) {
	switch format {
	case "0":
		return CodecAudioUlaw, true
	case "8":
		return CodecAudioAlaw, true
	case "9":
		return CodecAudioG722, true
	case "18":
		return CodecAudioG729, true
	}
	return Codec{}, false
}

// formatParameters returns the value of a=fmtp:<format>, or "" if absent.
func formatParameters(format string, attrs []string) string {
	pref := "fmtp:" + format + " "
	for _, a := range attrs {
		if strings.HasPrefix(a, pref) {
			return strings.TrimSpace(a[len(pref):])
		}
	}
	return ""
}
