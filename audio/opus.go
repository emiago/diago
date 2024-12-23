// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package audio

import (
	"encoding/binary"
	"fmt"

	"github.com/rs/zerolog/log"
	"gopkg.in/hraban/opus.v2"
)

func samplesByteToInt16(input []byte, output []int16) int {
	if len(output) < len(input)/2 {
		panic("samplesByteToInt16 output is too small buffer")
	}

	j := 0
	for i := 0; i < len(input); i, j = i+2, j+1 {
		output[j] = int16(binary.LittleEndian.Uint16(input[i : i+2]))
	}
	return len(input) / 2
}

func samplesInt16ToBytes(input []int16, output []byte) int {
	if len(output) < len(input)*2 {
		panic(fmt.Sprintf("samplesInt16ToBytes output is too small buffer. expected=%d, received=%d", len(input)*2, len(output)))
	}

	j := 0
	for _, sample := range input {
		binary.LittleEndian.PutUint16(output[j:j+2], uint16(sample))
		j += 2
	}
	return len(input) * 2
}

type OpusEncoder struct {
	*opus.Encoder
	pcmInt16    []int16
	numChannels int
}

func (enc *OpusEncoder) EncodeTo(data []byte, lpcm []byte) (int, error) {
	n := samplesByteToInt16(lpcm, enc.pcmInt16)

	// for

	// frameSize := 960
	// maxN := min(frameSize, n)
	pcmInt16 := enc.pcmInt16[:n]

	// fmt.Println("I am here", len(data), len(lpcm), len(pcmInt16))
	// Make sure that the raw PCM data you want to encode has a legal Opus frame size. This means it must be exactly 2.5, 5, 10, 20, 40 or 60 ms long.
	// The number of bytes this corresponds to depends on the sample rate
	n, err := enc.Encode(pcmInt16, data)
	return n, err

}

type OpusDecoder struct {
	*opus.Decoder
	pcmInt16    []int16
	unread      int
	off         int
	numChannels int
}

func (dec *OpusDecoder) Decode(data []byte) []byte {
	n, err := dec.Decoder.Decode(data, dec.pcmInt16)
	if err != nil {
		return []byte{}
	}

	pcm := dec.pcmInt16[:n*2]
	lpcm := make([]byte, len(pcm)*2)
	n = samplesInt16ToBytes(pcm, lpcm)
	return lpcm[:n]
}

func (dec *OpusDecoder) DecodeTo(lpcm []byte, data []byte) (int, error) {

	// Temporarly fix
	if data == nil {
		if dec.unread > 0 {
			n := dec.unread
			off := dec.off

			maxRead := min(len(lpcm)/2, n)
			dec.unread = n - maxRead
			dec.off += maxRead

			pcm := dec.pcmInt16[off : off+maxRead]
			n = samplesInt16ToBytes(pcm, lpcm)

			if dec.unread > 0 {
				// panic("buffer not cleaned")
				log.Error().Msg("Buffer not cleaned")
			}

			return n, nil
		}
		return 0, nil
	}

	if dec.unread > 0 {
		log.Error().Msg("Buffer not cleaned")
	}

	pcmN, err := dec.Decoder.Decode(data, dec.pcmInt16)
	if err != nil {
		return 0, err
	}
	// If there are more channels lib will not return, so it needs to be multiplied
	pcmN = pcmN * dec.numChannels

	maxRead := min(len(lpcm)/2, pcmN)

	dec.unread = pcmN - maxRead
	dec.off = maxRead
	pcm := dec.pcmInt16[:maxRead]
	n := samplesInt16ToBytes(pcm, lpcm)
	return n, nil
}
