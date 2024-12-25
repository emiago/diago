// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sync"

	"github.com/emiago/diago/audio"
	"github.com/emiago/diago/media"
)

var (
	PlaybackBufferSize = 3840 // For now largest we support. 48000 sample rate with 2 channels
)

var playBufPool = sync.Pool{
	New: func() any {
		// Increase this size if there will be support for larger pools
		return make([]byte, PlaybackBufferSize)
	},
}

type AudioPlayback struct {
	writer io.Writer
	codec  media.Codec

	// Read only values
	// This will influence playout sampling buffer
	BitDepth    int
	NumChannels int

	totalWritten int64
}

// NewAudioPlayback creates a playback where writer is encoder/streamer to media codec
// Use dialog.PlaybackCreate() instead creating manually playback
func NewAudioPlayback(writer io.Writer, codec media.Codec) AudioPlayback {
	return AudioPlayback{
		writer:      writer,
		codec:       codec,
		BitDepth:    16,
		NumChannels: codec.NumChannels,
	}
}

// Play is generic approach to play supported audio contents
// Empty mimeType will stream reader as buffer. Make sure that bitdepth and numchannels is set correctly
func (p *AudioPlayback) Play(reader io.Reader, mimeType string) (int64, error) {
	var written int64
	var err error
	switch mimeType {
	case "":
		written, err = p.stream(reader, p.writer)
	case "audio/wav", "audio/x-wav", "audio/wav-x", "audio/vnd.wave":
		written, err = p.streamWav(reader, p.writer)
	default:
		return 0, fmt.Errorf("unsuported content type %q", mimeType)
	}

	p.totalWritten += written
	if errors.Is(err, io.EOF) {
		return written, nil
	}
	return written, err
}

// PlayFile will play file and close file when finished playing
// If you need to play same file multiple times, that use generic Play function
func (p *AudioPlayback) PlayFile(filename string) (int64, error) {
	file, err := os.Open(filename)
	if err != nil {
		return 0, err
	}
	defer file.Close()

	if ext := path.Ext(file.Name()); ext != ".wav" {
		return 0, fmt.Errorf("only playing wav file is now supported, but detected=%s", ext)
	}

	return p.Play(file, "audio/wav")
}

func (p *AudioPlayback) stream(body io.Reader, playWriter io.Writer) (int64, error) {
	payloadSize := p.calcPlayoutSize()
	buf := playBufPool.Get()
	defer playBufPool.Put(buf)
	payloadBuf := buf.([]byte)[:payloadSize] // 20 ms

	written, err := copyWithBuf(body, playWriter, payloadBuf)
	return written, err
}

func (p *AudioPlayback) streamWav(body io.Reader, playWriter io.Writer) (int64, error) {
	codec := &p.codec
	dec := audio.NewWavReader(body)
	if err := dec.ReadHeaders(); err != nil {
		return 0, err
	}
	if dec.BitsPerSample != uint16(p.BitDepth) {
		return 0, fmt.Errorf("wav file bitdepth=%d does not match expected=%d", dec.BitsPerSample, p.BitDepth)
	}
	if dec.SampleRate != codec.SampleRate {
		return 0, fmt.Errorf("wav file samplerate=%d does not match expected=%d", dec.SampleRate, codec.SampleRate)
	}
	if dec.NumChannels != uint16(codec.NumChannels) {
		return 0, fmt.Errorf("wav file numchannels=%d does not match expected=%d", dec.NumChannels, codec.NumChannels)
	}

	// We need to read and packetize to 20 ms
	// sampleDurMS := int(codec.SampleDur.Milliseconds())
	// payloadSize := int(dec.BitsPerSample) / 8 * int(dec.NumChannels) * int(dec.SampleRate) / 1000 * sampleDurMS
	payloadSize := p.codec.SamplesPCM(int(dec.BitsPerSample))

	buf := playBufPool.Get()
	defer playBufPool.Put(buf)
	payloadBuf := buf.([]byte)[:payloadSize] // 20 ms

	enc, err := audio.NewPCMEncoderWriter(codec.PayloadType, playWriter)
	if err != nil {
		return 0, fmt.Errorf("failed to create PCM encoder: %w", err)
	}

	written, err := media.CopyWithBuf(dec, enc, payloadBuf)
	// written, err := wavCopy(dec, enc, payloadBuf)
	return written, err
}

func (p *AudioPlayback) calcPlayoutSize() int {
	codec := &p.codec
	sampleDurMS := int(codec.SampleDur.Milliseconds())

	bitsPerSample := p.BitDepth
	numChannels := p.NumChannels
	sampleRate := codec.SampleRate
	return int(bitsPerSample) / 8 * int(numChannels) * int(sampleRate) / 1000 * sampleDurMS
}

// func wavCopy(dec *audio.WavReader, playWriter io.Writer, payloadBuf []byte) (int64, error) {
// 	var totalWritten int64
// 	for {
// 		ch, err := dec.NextChunk()
// 		if err != nil {
// 			return totalWritten, err
// 		}
// 		fmt.Println("Chunk wav", ch)
// 		if ch.ID != riff.DataFormatID && ch.ID != [4]byte{} {
// 			// Until we reach data chunk we will draining
// 			ch.Drain()
// 			continue
// 		}

// 		fmt.Println("copy buf", len(payloadBuf))
// 		n, err := copyWithBuf(ch, playWriter, payloadBuf)
// 		totalWritten += n
// 		if err != nil {
// 			return totalWritten, err
// 		}
// 	}
// }
