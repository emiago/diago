// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package audio

import (
	"fmt"

	"gopkg.in/hraban/opus.v2"
)

type OpusEncoder struct {
	opus.Encoder
	pcmInt16    []int16
	numChannels int
}

func (enc *OpusEncoder) EncodeTo(data []byte, lpcm []byte) (int, error) {
	// What if lpcm is larger than our frame?
	if len(lpcm)*2 > len(enc.pcmInt16) {

	}

	step := min(len(enc.pcmInt16), len(lpcm)*2)
	for i := 0; i < len(lpcm); i = i + step {

	}

	n, err := samplesByteToInt16(lpcm, enc.pcmInt16)
	if err != nil {
		return 0, err
	}

	// NOTE: opus has fixed frame sizes 2.5, 5, 10, 20, 40 or 60 ms
	// frameSize := 960
	// maxN := min(frameSize, n)
	pcmInt16 := enc.pcmInt16[:n]

	n, err = enc.Encode(pcmInt16, data)
	return n, err

}

type OpusDecoder struct {
	opus.Decoder
	pcmInt16    []int16
	unread      int
	off         int
	numChannels int
}

func (dec *OpusDecoder) DecodeTo(lpcm []byte, data []byte) (int, error) {
	// For now we do not track unread. Should be caller who needs to watch out on codec sample size
	// if data == nil {
	// 	if dec.unread > 0 {
	// 		// log.Trace().Int("n", dec.unread).Msg("opus: returning unread")

	// 		n := dec.unread
	// 		off := dec.off

	// 		maxRead := min(len(lpcm)/2, n)
	// 		dec.unread = n - maxRead
	// 		dec.off += maxRead

	// 		pcm := dec.pcmInt16[off : off+maxRead]
	// 		n, err := samplesInt16ToBytes(pcm, lpcm)
	// 		return n, err
	// 	}
	// 	return 0, nil
	// }

	pcmN, err := dec.Decoder.Decode(data, dec.pcmInt16)
	if err != nil {
		return 0, err
	}
	// If there are more channels lib will not return, so it needs to be multiplied
	pcmN = pcmN * dec.numChannels
	if len(dec.pcmInt16) < pcmN {
		// Should never happen
		return 0, fmt.Errorf("opus: pcm int buffer expected=%d", pcmN)
	}

	// maxRead := min(len(lpcm)/2, pcmN)
	// dec.unread = pcmN - maxRead
	// dec.off = maxRead

	pcm := dec.pcmInt16[:pcmN]
	n, err := samplesInt16ToBytes(pcm, lpcm)
	return n, err
}
