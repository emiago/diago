// SPDX-License-Identifier: MPL-2.0
// Copyright (C) 2024 Emir Aganovic

package diago

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"sync"
	"time"

	"github.com/emiago/diago/audio"
	"github.com/emiago/diago/media"
	"github.com/go-audio/riff"
)

var (
	PlaybackBufferSize = 320
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
		NumChannels: 2,
	}
}

// Play is generic approach to play supported audio contents
// Empty mimeType will stream reader as buffer. Make sure that bitdepth and numchannels is set correctly
func (p *AudioPlayback) Play(reader io.Reader, mimeType string) error {
	var written int64
	var err error
	switch mimeType {
	case "":
		written, err = p.stream(reader, p.writer)
	case "audio/wav", "audio/x-wav", "audio/wav-x", "audio/vnd.wave":
		written, err = p.streamWav(reader, p.writer)
	default:
		return fmt.Errorf("unsuported content type %q", mimeType)
	}
	if err != nil {
		return err
	}

	if written == 0 {
		return fmt.Errorf("nothing written")
	}
	p.totalWritten += written
	return nil
}

func (p *AudioPlayback) PlayFile(ctx context.Context, filename string) (err error) {
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer file.Close()

	if ext := path.Ext(file.Name()); ext != ".wav" {
		return fmt.Errorf("only playing wav file is now supported, but detected=%s", ext)
	}

	errCh := make(chan error, 1)
	go func() {
		errCh <- p.Play(file, "audio/wav")
	}()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case e := <-errCh:
		return e
	}
}

func (p *AudioPlayback) stream(body io.Reader, playWriter io.Writer) (int64, error) {
	payloadSize := p.calcPlayoutSize()
	buf := playBufPool.Get()
	defer playBufPool.Put(buf)
	payloadBuf := buf.([]byte)[:payloadSize] // 20 ms

	written, err := copyWithBuf(body, playWriter, payloadBuf)
	if !errors.Is(err, io.EOF) {
		return written, err
	}
	return written, nil
}

func (p *AudioPlayback) streamWav(body io.Reader, playWriter io.Writer) (int64, error) {
	// dec := audio.NewWavDecoderStreamer(body)
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

	// We need to read and packetize to 20 ms
	sampleDurMS := int(codec.SampleDur.Milliseconds())
	payloadSize := int(dec.BitsPerSample) / 8 * int(dec.NumChannels) * int(dec.SampleRate) / 1000 * sampleDurMS

	buf := playBufPool.Get()
	defer playBufPool.Put(buf)
	payloadBuf := buf.([]byte)[:payloadSize] // 20 ms

	enc, err := audio.NewPCMEncoder(codec.PayloadType, playWriter)
	if err != nil {
		return 0, fmt.Errorf("failed to create PCM encoder: %w", err)
	}

	written, err := wavCopy(dec, enc, payloadBuf)
	if !errors.Is(err, io.EOF) {
		return written, err
	}
	return written, nil
}

func (p *AudioPlayback) calcPlayoutSize() int {
	codec := &p.codec
	sampleDurMS := int(codec.SampleDur.Milliseconds())

	bitsPerSample := p.BitDepth
	numChannels := p.NumChannels
	sampleRate := codec.SampleRate
	return int(bitsPerSample) / 8 * int(numChannels) * int(sampleRate) / 1000 * sampleDurMS
}

func streamWavRTP(body io.Reader, rtpWriter *media.RTPPacketWriter, codec media.Codec) (int64, error) {
	pt := codec.PayloadType
	enc, err := audio.NewPCMEncoder(pt, rtpWriter)
	if err != nil {
		return 0, err
	}

	p := NewAudioPlayback(enc, media.Codec{
		SampleRate: codec.SampleRate,
		SampleDur:  20 * time.Millisecond,
	})
	return p.streamWav(body, enc)
}

func copyWithBuf(body io.Reader, playWriter io.Writer, payloadBuf []byte) (int64, error) {
	var totalWritten int64
	for {
		n, err := body.Read(payloadBuf)
		if err != nil {
			return totalWritten, err
		}
		n, err = playWriter.Write(payloadBuf[:n])
		if err != nil {
			return totalWritten, err
		}
		totalWritten += int64(n)
	}
}

func wavCopy(dec *audio.WavReader, playWriter io.Writer, payloadBuf []byte) (int64, error) {
	var totalWritten int64
	for {
		ch, err := dec.NextChunk()
		if err != nil {
			return totalWritten, err
		}

		if ch.ID != riff.DataFormatID && ch.ID != [4]byte{} {
			// Until we reach data chunk we will draining
			ch.Drain()
			continue
		}

		n, err := copyWithBuf(ch, playWriter, payloadBuf)
		totalWritten += n
		if err != nil {
			return totalWritten, err
		}
	}
}
