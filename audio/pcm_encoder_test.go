// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package audio

import (
	"bytes"
	"encoding/binary"
	"io/ioutil"
	"math"
	"testing"

	"github.com/emiago/diago/media"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zaf/g711"
)

// testGeneratePCM16 generates a 20ms PCM16 sine wave as a byte slice.
// Frequency: 1kHz, Amplitude: max for PCM16.
func testGeneratePCM16(sampleRate int) []byte {
	const (
		durationMs = 20    // 20 ms
		frequency  = 1000  // 1 kHz sine wave
		amplitude  = 32767 // Max amplitude for PCM16
	)

	numSamples := sampleRate * durationMs / 1000
	buf := new(bytes.Buffer)

	for i := 0; i < numSamples; i++ {
		sample := int16(amplitude * math.Sin(2*math.Pi*float64(frequency)*float64(i)/float64(sampleRate)))
		binary.Write(buf, binary.LittleEndian, sample)
		binary.Write(buf, binary.LittleEndian, sample)
	}
	return buf.Bytes()
}

func TestPCMEncoderWrite(t *testing.T) {
	lpcm := []byte{
		0x00, 0x01, // Sample 1
		0x02, 0x03, // Sample 2
		0x04, 0x05, // Sample 3
		0x06, 0x07, // Sample 4
	}

	// Expected μ-law and A-law outputs (replace with actual expected encoded bytes)
	expectedULaw := g711.EncodeUlaw(lpcm)
	expectedALaw := g711.EncodeAlaw(lpcm)

	// Test cases for both μ-law and A-law
	tests := []struct {
		name     string
		codec    uint8
		expected []byte
	}{
		{"UlawEncoding", FORMAT_TYPE_ULAW, expectedULaw},
		{"AlawEncoding", FORMAT_TYPE_ALAW, expectedALaw},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var outputBuffer bytes.Buffer

			// Create the PCM encoder
			encoder, err := NewPCMEncoderWriter(tt.codec, &outputBuffer)
			require.NoError(t, err)

			// Write the PCM data
			n, err := encoder.Write(lpcm)
			if err != nil {
				t.Fatalf("Write failed: %v", err)
			}
			if n != len(lpcm) {
				t.Fatalf("expected to write %d bytes, but wrote %d", len(lpcm), n)
			}

			assert.Equal(t, tt.expected, outputBuffer.Bytes())
		})
	}
}

func TestPCMDecoderRead(t *testing.T) {
	// Expected decoded output
	// pcm := []byte{
	// 	0x00, 0x01, 0x02, 0x03, // This should match original PCM data after decoding
	// 	0x04, 0x05, 0x06, 0x07,
	// }
	pcm := testGeneratePCM16(8000)

	encodedUlaw := g711.EncodeUlaw(pcm)
	encodedAlaw := g711.EncodeAlaw(pcm)

	// Test cases for both μ-law and A-law
	tests := []struct {
		name     string
		codec    uint8
		input    []byte
		expected []byte
	}{
		{"UlawDecoding", FORMAT_TYPE_ULAW, encodedUlaw, g711.DecodeUlaw(encodedUlaw)},
		{"AlawDecoding", FORMAT_TYPE_ALAW, encodedAlaw, g711.DecodeAlaw(encodedAlaw)},
		{"AlawDecodingCut", FORMAT_TYPE_ALAW, encodedAlaw[:len(encodedAlaw)-48], g711.DecodeAlaw(encodedAlaw[:len(encodedAlaw)-48])},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a buffer to simulate the encoded input
			inputBuffer := bytes.NewReader(tt.input)

			// Create the PCM decoder
			decoder, err := NewPCMDecoderReader(tt.codec, inputBuffer)
			decoder.BufSize = 160
			require.NoError(t, err)

			// Prepare a buffer to read the decoded PCM data into
			// decodedPCM := make([]byte, 320)

			// Read the data
			decodedPCM, err := media.ReadAll(decoder, 320)
			n := len(decodedPCM)
			// n, err := decoder.Read(decodedPCM)
			require.NoError(t, err)
			if n != len(tt.expected) {
				t.Fatalf("expected to read %d bytes, but read %d", len(tt.expected), n)
			}

			// Verify the decoded output matches the expected PCM data
			decodedPCM = decodedPCM[:n]
			assert.Equal(t, tt.expected, decodedPCM)
		})
	}
}

// Extract raw pcm data from .wav file
func extractWavPcm(t *testing.T, fname string) []int16 {
	bytes, err := ioutil.ReadFile(fname)
	if err != nil {
		t.Fatalf("Error reading file data from %s: %v", fname, err)
	}
	const wavHeaderSize = 44
	if (len(bytes)-wavHeaderSize)%2 == 1 {
		t.Fatalf("Illegal wav data: payload must be encoded in byte pairs")
	}
	numSamples := (len(bytes) - wavHeaderSize) / 2
	samples := make([]int16, numSamples)
	for i := 0; i < numSamples; i++ {
		samples[i] += int16(bytes[wavHeaderSize+i*2])
		samples[i] += int16(bytes[wavHeaderSize+i*2+1]) << 8
	}
	return samples
}

func TestPCM16ToByte(t *testing.T) {
	pcm := []int16{-32768, -12345, -1, 0, 1, 12345, 32767, 32767}
	bytearr := []byte{0, 128, 199, 207, 255, 255, 0, 0, 1, 0, 57, 48, 255, 127, 255, 127}

	output := make([]byte, len(pcm)*2)
	samplesInt16ToBytes(pcm, output)
	assert.Equal(t, bytearr, output)

	outputPcm := make([]int16, len(bytearr)/2)
	samplesByteToInt16(bytearr, outputPcm)
	assert.Equal(t, pcm, outputPcm)
}
