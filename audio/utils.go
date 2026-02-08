// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package audio

import (
	"encoding/binary"
	"errors"
	"math"
	"time"
)

// RMS Silence detection. Useful for clean audio samples
func SilenceDetectRMSframe(frame []byte, sampleRate int, threshold float64) bool {
	frameSize := sampleRate / 100 // 10 ms frames
	// Compute RMS energy for frame
	var sumSquares float64
	for i := 0; i < len(frame); i += 2 {
		sample := int16(binary.LittleEndian.Uint16(frame[i:]))
		s := float64(sample)
		sumSquares += s * s
	}
	rms := math.Sqrt(sumSquares / float64(frameSize))

	// Silence decision
	return rms < threshold
}

type PCMProps struct {
	SampleRate  int
	NumChannels int
	// BitsPerSample int
}

func FadeOut(pcmData []byte, props PCMProps, dur time.Duration) error {
	bytesPerSample := props.NumChannels * 2 // 2 bytes per int16 per channel

	if len(pcmData)%bytesPerSample != 0 {
		return errors.New("pcmData size is not aligned with frame size")
	}

	totalSamples := len(pcmData) / bytesPerSample

	// Number of frames to fade
	fadeSamples := int(float64(props.SampleRate) * dur.Seconds())

	if fadeSamples <= 0 {
		return nil
	}
	if fadeSamples > totalSamples {
		fadeSamples = totalSamples
	}

	startIndex := totalSamples - fadeSamples

	// Apply fade-out
	for i := 0; i < fadeSamples; i++ {
		gain := 1.0 - float64(i)/float64(fadeSamples)

		frameOffset := (startIndex + i) * bytesPerSample

		// Process each channel independently
		for ch := 0; ch < props.NumChannels; ch++ {
			off := frameOffset + ch*2

			s := int16(binary.LittleEndian.Uint16(pcmData[off : off+2]))
			fs := int16(float64(s) * gain)

			binary.LittleEndian.PutUint16(pcmData[off:off+2], uint16(fs))
		}
	}

	return nil
}

func PCMMix(dstBuf []byte, mixedBuf []byte, readBuf []byte) {
	n := len(readBuf)
	for i := 0; i < n; i += 2 {
		current := int16(binary.LittleEndian.Uint16(mixedBuf[i:]))
		frame := int16(binary.LittleEndian.Uint16(readBuf[i:]))

		mixed32 := int32(current) + int32(frame)
		var mixed int16
		switch {
		case mixed32 > 32767: //int16 max
			mixed = 32767
		case mixed32 < -32768:
			mixed = -32768
		default:
			mixed = int16(mixed32)
		}

		binary.LittleEndian.PutUint16(dstBuf[i:], uint16(mixed))
	}
}

func PCMUnmix(dstBuf []byte, mixedBuf []byte, readBuf []byte) {
	n := len(readBuf)
	for i := 0; i < n; i += 2 {
		current := int16(binary.LittleEndian.Uint16(mixedBuf[i:]))
		frame := int16(binary.LittleEndian.Uint16(readBuf[i:]))

		mixed32 := int32(current) - int32(frame)
		var mixed int16
		switch {
		case mixed32 > 32767: //int16 max
			mixed = 32767
		case mixed32 < -32768:
			mixed = -32768
		default:
			mixed = int16(mixed32)
		}

		binary.LittleEndian.PutUint16(dstBuf[i:], uint16(mixed))
	}
}
