// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package audio

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"math"
	"sync"

	"github.com/emiago/diago/media"
)

var (
	ringtones sync.Map
	beeps     sync.Map
)

// BeepLoadPCM loads pregenerated beep in PCM format
func BeepLoadPCM(codec media.Codec) ([]byte, error) {
	uuid := fmt.Sprintf("%s-%d", codec.Name, codec.SampleRate)
	ringval, exists := beeps.Load(uuid)
	if exists {
		return ringval.([]byte), nil
	}
	pcmBytes := beepPCMGenerate(int(codec.SampleRate))
	beeps.Store(uuid, pcmBytes)
	return pcmBytes, nil
}

func beepPCMGenerate(sampleRate int) []byte {
	var (
		durationSec = 0.5   // 1 second beep
		volume      = 0.2   // volume
		freq        = 700.0 // beep frequency in Hz
	)

	numSamples := int(float64(sampleRate) * durationSec)
	buf := &bytes.Buffer{}

	for i := 0; i < numSamples; i++ {
		t := float64(i) / float64(sampleRate)
		// Generate sine wave for beep
		sample := volume * math.Sin(2*math.Pi*freq*t)
		intSample := int16(sample * math.MaxInt16)
		binary.Write(buf, binary.LittleEndian, intSample)
	}

	return buf.Bytes()
}

// RingtoneLoadPCM loads pregenerated ringtone in PCM format
func RingtoneLoadPCM(codec media.Codec) ([]byte, error) {
	uuid := fmt.Sprintf("%s-%d", codec.Name, codec.SampleRate)
	ringval, exists := ringtones.Load(uuid)
	if exists {
		return ringval.([]byte), nil
	}
	pcmBytes := ringtonePCMGenerate(int(codec.SampleRate))
	ringtones.Store(uuid, pcmBytes)
	return pcmBytes, nil
}

func ringtonePCMGenerate(sampleRate int) []byte {
	var (
		durationSec = 2
		volume      = 0.3
		freq1       = 350.0
		freq2       = 440.0
	)

	numSamples := sampleRate * durationSec
	buf := &bytes.Buffer{}

	for i := 0; i < numSamples; i++ {
		t := float64(i) / float64(sampleRate)
		// Combine the two sine waves and normalize
		sample := volume * (math.Sin(2*math.Pi*freq1*t) + math.Sin(2*math.Pi*freq2*t)) / 2.0
		// Convert to 16-bit signed PCM
		intSample := int16(sample * math.MaxInt16)
		binary.Write(buf, binary.LittleEndian, intSample)
	}

	pcmBytes := buf.Bytes()

	return pcmBytes
}
