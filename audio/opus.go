//go:build !with_opus_c

// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package audio

import (
	"fmt"
)

// This is placeholder for opus encoder. Opus is used as C binding.

type OpusEncoder struct {
}

func (enc *OpusEncoder) Init(sampleRate int, numChannels int, samplesSize int) error {
	return fmt.Errorf("Use with_opus_c tag to compile opus encoder")
}

func (enc *OpusEncoder) EncodeTo(data []byte, lpcm []byte) (int, error) {
	return 0, fmt.Errorf("not supported")
}

type OpusDecoder struct {
}

func (enc *OpusDecoder) Init(sampleRate int, numChannels int, samplesSize int) error {
	return fmt.Errorf("Use with_opus_c tag to compile opus decoder")
}

func (dec *OpusDecoder) DecodeTo(lpcm []byte, data []byte) (int, error) {
	return 0, fmt.Errorf("not supported")
}
