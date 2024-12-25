//go:build with_opus_c

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

func (enc *OpusEncoder) Init(sampleRate int, numChannels int, samplesSize int) error {
	enc.numChannels = numChannels
	// TODO use sync pool for creating this. Needs to be closer
	enc.pcmInt16 = make([]int16, samplesSize)

	if err := enc.Encoder.Init(sampleRate, numChannels, opus.AppVoIP); err != nil {
		return fmt.Errorf("failed to create opus decoder: %w", err)
	}

	return nil
}

func (enc *OpusEncoder) EncodeTo(data []byte, lpcm []byte) (int, error) {
	n, err := samplesByteToInt16(lpcm, enc.pcmInt16)
	if err != nil {
		return 0, err
	}

	// NOTE: opus has fixed frame sizes 2.5, 5, 10, 20, 40 or 60 ms
	pcmInt16 := enc.pcmInt16[:n]

	n, err = enc.Encode(pcmInt16, data)
	return n, err

}

type OpusDecoder struct {
	opus.Decoder
	pcmInt16    []int16
	numChannels int
}

func (enc *OpusDecoder) Init(sampleRate int, numChannels int, samplesSize int) error {
	enc.numChannels = numChannels
	enc.pcmInt16 = make([]int16, samplesSize)

	if err := enc.Decoder.Init(sampleRate, numChannels); err != nil {
		return fmt.Errorf("failed to create opus decoder: %w", err)
	}

	return nil
}

func (dec *OpusDecoder) DecodeTo(lpcm []byte, data []byte) (int, error) {
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

	pcm := dec.pcmInt16[:pcmN]
	n, err := samplesInt16ToBytes(pcm, lpcm)
	return n, err
}
