package audio

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/zaf/g711"
)

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
