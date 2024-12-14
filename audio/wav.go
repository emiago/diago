// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package audio

import (
	"encoding/binary"
	"io"
)

// WavWriteVoipPCM is normally 16 bit mono 8000 PCM
func WavWriteVoipPCM(w io.Writer, audio []byte) (int, error) {
	return WavWrite(w, audio, WavWriteOpts{
		AudioFormat: 1,
		BitDepth:    16, // 16 bit
		NumChans:    1,
		SampleRate:  8000,
	})
}

type WavWriteOpts struct {
	SampleRate  int
	BitDepth    int
	NumChans    int
	AudioFormat int
}

// WavWrite wrates WAV encoded to writter with the given audio payload, sample rate, and bit rate
func WavWrite(w io.Writer, audio []byte, opts WavWriteOpts) (int, error) {
	// WAV header constants
	const (
		headerSize   = 44
		fmtChunkSize = 16
		// audioFormat    = 1 // PCM
		// numChannels    = 1 // mono
		// bitsPerSample  = 16
	)

	audioFormat := opts.AudioFormat
	numChannels := opts.NumChans
	bitsPerSample := opts.BitDepth
	sampleRate := opts.SampleRate
	// Calculate file size
	fileSize := len(audio) + headerSize - 8

	// Create the header
	header := make([]byte, headerSize)
	copy(header[0:4], []byte("RIFF"))
	binary.LittleEndian.PutUint32(header[4:8], uint32(fileSize))
	copy(header[8:12], []byte("WAVE"))

	// fmt subchunk
	copy(header[12:16], []byte("fmt "))
	binary.LittleEndian.PutUint32(header[16:20], fmtChunkSize)
	binary.LittleEndian.PutUint16(header[20:22], uint16(audioFormat))
	binary.LittleEndian.PutUint16(header[22:24], uint16(numChannels))
	binary.LittleEndian.PutUint32(header[24:28], uint32(sampleRate))
	binary.LittleEndian.PutUint32(header[28:32], uint32(sampleRate*bitsPerSample*numChannels/8)) // Byte rate calculation
	binary.LittleEndian.PutUint16(header[32:34], uint16(bitsPerSample*numChannels/8))            // Block align
	binary.LittleEndian.PutUint16(header[34:36], uint16(bitsPerSample))

	// data chunk
	copy(header[36:40], []byte("data"))
	binary.LittleEndian.PutUint32(header[40:44], uint32(len(audio)))

	// Combine header and audio payload
	wavFile := append(header, audio...)
	for N := 0; N < len(wavFile); {
		n, err := w.Write(wavFile)
		if err != nil {
			return 0, err
		}
		N += n
	}
	return len(wavFile), nil
}
