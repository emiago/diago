// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package audio

import (
	"bytes"
	"encoding/binary"
	"io"
	"io/ioutil"
	"math"
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zaf/g711"
	"gopkg.in/hraban/opus.v2"
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
	pcm := []byte{
		0x00, 0x01, 0x02, 0x03, // This should match original PCM data after decoding
		0x04, 0x05, 0x06, 0x07,
	}

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
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create a buffer to simulate the encoded input
			inputBuffer := bytes.NewReader(tt.input)

			// Create the PCM decoder
			decoder, err := NewPCMDecoderReader(tt.codec, inputBuffer)
			require.NoError(t, err)

			// Prepare a buffer to read the decoded PCM data into
			decodedPCM := make([]byte, 320)

			// Read the data
			n, err := decoder.Read(decodedPCM)
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

func TestPCMEncoderOpus(t *testing.T) {
	// Expected decoded output
	pcm := testGeneratePCM16(48000)
	pcmInt16 := make([]int16, len(pcm)/2)
	n := samplesByteToInt16(pcm, pcmInt16)
	require.Equal(t, len(pcmInt16), n)

	enc, err := opus.NewEncoder(48000, 2, opus.AppVoIP)
	require.NoError(t, err)

	encodedOpus := make([]byte, 1500)
	n, err = enc.Encode(pcmInt16, encodedOpus)
	require.NoError(t, err)
	encodedOpus = encodedOpus[:n]

	t.Log("Len of PCM ", len(pcm))
	t.Log("Len of encoded ", len(encodedOpus))

	// Test cases for both μ-law and A-law
	// tt := struct {
	// 	name     string
	// 	codec    uint8
	// 	input    []byte
	// 	expected []byte
	// }{
	// 	{"PCMDecoding", FORMAT_TYPE_OPUS, nil, encodedOpus},
	// }

	t.Run("Encode", func(t *testing.T) {
		var outputBuffer bytes.Buffer

		// Create the PCM encoder
		encoder, err := NewPCMEncoderWriter(FORMAT_TYPE_OPUS, &outputBuffer)
		require.NoError(t, err)

		// Write the PCM data
		n, err := encoder.Write(pcm)
		if err != nil {
			t.Fatalf("Write failed: %v", err)
		}
		if n != len(pcm) {
			t.Fatalf("expected to write %d bytes, but wrote %d", len(pcm), n)
		}

		assert.Equal(t, encodedOpus, outputBuffer.Bytes())
	})

	dec, err := opus.NewDecoder(48000, 2)
	require.NoError(t, err)

	// data := make([]byte, 1500)
	n, err = dec.Decode(encodedOpus, pcmInt16)
	require.NoError(t, err)

	expectedPCM := pcmInt16[:n*2]
	expectedLpcm := make([]byte, len(expectedPCM)*2)
	n = samplesInt16ToBytes(expectedPCM, expectedLpcm)
	require.Equal(t, n, len(expectedLpcm))

	t.Run("Decode", func(t *testing.T) {
		// Create a buffer to simulate the encoded input
		inputBuffer := bytes.NewReader(encodedOpus)

		// Create the PCM decoder
		decoder, err := NewPCMDecoderReader(FORMAT_TYPE_OPUS, inputBuffer)
		require.NoError(t, err)

		// Prepare a buffer to read the decoded PCM data into
		decodedPCM := make([]byte, 10000)

		// Read the data
		n, err := decoder.Read(decodedPCM)
		require.NoError(t, err)
		if n != len(expectedLpcm) {
			t.Fatalf("expected to read %d bytes, but read %d", len(expectedLpcm), n)
		}

		// Verify the decoded output matches the expected PCM data
		decodedPCM = decodedPCM[:n]
		assert.Equal(t, expectedLpcm, decodedPCM)
	})

	t.Run("DecodeWithSmallBuffer", func(t *testing.T) {
		// Create a buffer to simulate the encoded input
		inputBuffer := bytes.NewReader(encodedOpus)

		// Create the PCM decoder
		decoder, err := NewPCMDecoderReader(FORMAT_TYPE_OPUS, inputBuffer)
		require.NoError(t, err)

		// Prepare a buffer to read the decoded PCM data into
		decodedPCM := make([]byte, 10000)
		tmpPCM := make([]byte, 512)

		// Read the data
		var n int
		for {
			nn, err := decoder.Read(tmpPCM)
			if err != nil {
				require.ErrorIs(t, err, io.EOF)
				break
			}
			tmpN := copy(decodedPCM[n:], tmpPCM[:nn])

			n += tmpN
		}
		if n != len(expectedLpcm) {
			t.Fatalf("expected to read %d bytes, but read %d", len(expectedLpcm), n)
		}

		// Verify the decoded output matches the expected PCM data
		decodedPCM = decodedPCM[:n]
		assert.Equal(t, expectedLpcm, decodedPCM)
	})
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

func TestOpusPCM(t *testing.T) {
	f, err := os.Open("./testdata/output_opus.raw")
	require.NoError(t, err)

	// dec, err := NewPCMDecoderReader(96, f)
	// require.NoError(t, err)

	d, err := opus.NewDecoder(48000, 1)
	require.NoError(t, err)
	// decOpus := &OpusDecoder{Decoder: d, pcmInt16: make([]int16, 48000*0.02*2), numChannels: 1}
	// dec.DecoderTo = decOpus.DecodeTo

	data, err := io.ReadAll(f)
	require.NoError(t, err)
	t.Log("Len data", len(data))

	buf := make([]int16, 1000000)
	n, err := d.Decode(data, buf)
	require.NoError(t, err)

	opuspcm := buf[:n]

	// opuspcm, err := io.ReadAll(dec)
	// require.NoError(t, err)

	wavpcm := extractWavPcm(t, "./testdata/speech_8.wav")
	if len(opuspcm) != len(wavpcm) {
		t.Fatalf("Unexpected length of decoded opus file: %d (.wav: %d)", len(opuspcm), len(wavpcm))
	}
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
