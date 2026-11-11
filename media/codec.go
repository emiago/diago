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
	CodecAudioUlaw = Codec{PayloadType: 0, SampleRate: 8000, SampleDur: 20 * time.Millisecond, NumChannels: 1, Name: "pcmu"}
	CodecAudioAlaw = Codec{PayloadType: 8, SampleRate: 8000, SampleDur: 20 * time.Millisecond, NumChannels: 1, Name: "pcma"}
	// G.722 uses an 8000 Hz RTP clock despite its 16000 Hz audio sample rate.
	CodecAudioG722          = Codec{PayloadType: 9, SampleRate: 8000, SampleDur: 20 * time.Millisecond, NumChannels: 1, Name: "g722"}
	CodecAudioG729          = Codec{PayloadType: 18, SampleRate: 8000, SampleDur: 20 * time.Millisecond, NumChannels: 1, Name: "g729"}
	CodecAudioOpus          = Codec{PayloadType: 96, SampleRate: 48000, SampleDur: 20 * time.Millisecond, NumChannels: 2, Name: "opus"}
	CodecTelephoneEvent8000 = Codec{PayloadType: 101, SampleRate: 8000, SampleDur: 20 * time.Millisecond, NumChannels: 1, Name: "telephone-event"}
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

	// FMTP holds the format-specific SDP parameters without the payload type.
	FMTP string

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

func (c Codec) negotiatePayloadType(remote Codec, answerer bool) Codec {
	localPayloadType := c.PayloadType
	c.PayloadType = remote.PayloadType
	if c.FMTP == "" {
		c.FMTP = remote.FMTP
	}
	c.payloadTypeRecv = nil
	if !answerer && localPayloadType != remote.PayloadType {
		c.payloadTypeRecv = &localPayloadType
	}
	return c
}

// Matches compares codec identity independently of payload type and packet duration.
func (c Codec) Matches(other Codec) bool {
	return strings.EqualFold(c.Name, other.Name) && c.SampleRate == other.SampleRate && c.NumChannels == other.NumChannels
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
	case sdp.FORMAT_TYPE_G722:
		return CodecAudioG722, nil
	case sdp.FORMAT_TYPE_G729:
		return CodecAudioG729, nil
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
	case sdp.FORMAT_TYPE_G722:
		return CodecAudioG722
	case sdp.FORMAT_TYPE_G729:
		return CodecAudioG729
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

// CodecsFromSDPRead parses codecs in the order listed in formats. The caller must
// provide an output slice with at least len(formats) entries. It returns the
// codecs parsed so far along with any errors in the codec attributes.
func CodecsFromSDPRead(formats []string, attrs []string, codecsAudio []Codec) (int, error) {
	n := 0
	var rerr error
	for _, f := range formats {
		pt64, err := strconv.ParseUint(f, 10, 8)
		if err != nil {
			rerr = errors.Join(rerr, fmt.Errorf("format type failed to conv to integer, skipping f=%s: %w", f, err))
			continue
		}

		var codec Codec
		found := true
		switch f {
		case sdp.FORMAT_TYPE_ULAW:
			codec = CodecAudioUlaw
		case sdp.FORMAT_TYPE_ALAW:
			codec = CodecAudioAlaw
		case sdp.FORMAT_TYPE_G722:
			codec = CodecAudioG722
		case sdp.FORMAT_TYPE_G729:
			codec = CodecAudioG729
		default:
			found = false
		}

		if !found {
			for _, a := range attrs {
				// a=rtpmap:<payload type> <encoding name>/<clock rate> [/<encoding parameters>]
				pref := "rtpmap:" + f + " "
				if !strings.HasPrefix(a, pref) {
					continue
				}
				props := strings.Fields(a[len(pref):])
				if len(props) == 0 {
					rerr = errors.Join(rerr, fmt.Errorf("bad rtpmap property a=%s", a))
					continue
				}
				propsCodec := strings.Split(props[0], "/")
				if len(propsCodec) < 2 {
					rerr = errors.Join(rerr, fmt.Errorf("bad rtpmap property a=%s", a))
					continue
				}

				sampleRate64, err := strconv.ParseUint(propsCodec[1], 10, 32)
				if err != nil {
					rerr = errors.Join(rerr, fmt.Errorf("sample rate failed to parse a=%s: %w", a, err))
					continue
				}
				codec = Codec{
					Name:        propsCodec[0],
					PayloadType: uint8(pt64),
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
				found = true
				break
			}
		}
		if !found {
			continue
		}

		// Scan separately so fmtp may precede rtpmap and static payload types
		// can carry parameters even when rtpmap is omitted.
		for _, a := range attrs {
			value, ok := strings.CutPrefix(a, "fmtp:")
			if !ok {
				continue
			}
			sep := strings.IndexAny(value, " \t")
			if sep < 0 || value[:sep] != f {
				continue
			}
			codec.FMTP = strings.TrimSpace(value[sep:])
			break
		}
		codecsAudio[n] = codec
		n++
	}
	return n, rerr
}
