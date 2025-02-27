//go:build with_opus_c

// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic
package audio

import (
	"bytes"
	"fmt"
	"io"
	"testing"

	"github.com/emiago/diago/media"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/hraban/opus.v2"
)

func TestPCMEncoderOpus(t *testing.T) {
	// Expected decoded output
	pcm := testGeneratePCM16(48000)
	pcmInt16 := make([]int16, len(pcm)/2)
	n, _ := samplesByteToInt16(pcm, pcmInt16)
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
	n, _ = samplesInt16ToBytes(expectedPCM, expectedLpcm)
	require.Equal(t, n, len(expectedLpcm))

	t.Run("Decode", func(t *testing.T) {
		// Create a buffer to simulate the encoded input
		inputBuffer := bytes.NewReader(encodedOpus)

		// Create the PCM decoder
		decoder, err := NewPCMDecoderReader(FORMAT_TYPE_OPUS, inputBuffer)
		require.NoError(t, err)

		// Prepare a buffer to read the decoded PCM data into
		decodedPCM := make([]byte, media.CodecAudioOpus.Samples16())
		fullPCM := make([]byte, 0, len(expectedLpcm))
		// Read the data
		for {
			n, err := decoder.Read(decodedPCM)
			if err != nil {
				break
			}
			fullPCM = append(fullPCM, decodedPCM[:n]...)
		}

		// Verify the decoded output matches the expected PCM data
		assert.Equal(t, expectedLpcm, fullPCM)
	})

	t.Run("DecodeWithSmallBuffer", func(t *testing.T) {
		// Create a buffer to simulate the encoded input
		inputBuffer := bytes.NewReader(encodedOpus)

		// Create the PCM decoder
		decoder, err := NewPCMDecoderReader(FORMAT_TYPE_OPUS, inputBuffer)
		require.NoError(t, err)

		decodedPCM := make([]byte, 512)
		_, err = decoder.Read(decodedPCM)
		require.Error(t, err)
		require.ErrorIs(t, err, io.ErrShortBuffer)
	})
}

// Decoding or encoding is supported regardless of sampling rate
// https://datatracker.ietf.org/doc/html/rfc6716#section-2.1.3
func TestOpusDecodingDifferentBandwith(t *testing.T) {
	pcm := testGeneratePCM16(48000)
	pcmInt16 := make([]int16, len(pcm)/2)
	n, _ := samplesByteToInt16(pcm, pcmInt16)
	require.Equal(t, len(pcmInt16), n)

	enc, err := opus.NewEncoder(48000, 2, opus.AppVoIP)
	require.NoError(t, err)

	encodedOpus := make([]byte, 1500)
	n, err = enc.Encode(pcmInt16, encodedOpus)
	require.NoError(t, err)
	encodedOpus = encodedOpus[:n]

	runDecodeTest := func(t *testing.T, sampleRate int) {
		dec, err := opus.NewDecoder(8000, 2)
		require.NoError(t, err)

		// data := make([]byte, 1500)
		n, err = dec.Decode(encodedOpus, pcmInt16)
		require.NoError(t, err)

		expectedPCM := pcmInt16[:n*2]
		expectedLpcm := make([]byte, len(expectedPCM)*2)
		n, _ = samplesInt16ToBytes(expectedPCM, expectedLpcm)
		require.Equal(t, n, len(expectedLpcm))
	}

	t.Run(fmt.Sprintf("FBDecode8000"), func(t *testing.T) {
		runDecodeTest(t, 8000)
	})

	t.Run(fmt.Sprintf("FBDecode16000"), func(t *testing.T) {
		runDecodeTest(t, 16000)
	})
}
