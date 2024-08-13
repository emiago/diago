// SPDX-License-Identifier: BSD-2-Clause
// Copyright (C) 2024 Emir Aganovic

package diago

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"github.com/emiago/diago/audio"
	"github.com/emiago/diago/media"
	"github.com/go-audio/riff"
)

type Playback struct {
	// reader io.Reader
	// TODO we could avoid mute controller
	writer io.Writer
	// codec  media.Codec
	SampleRate uint32
	SampleDur  time.Duration

	totalWritten int
}

// Use dialog.PlaybackCreate() instead creating manually playback
func NewPlayback(writer io.Writer, sampleRate uint32, sampleDur time.Duration) Playback {
	return Playback{
		writer:     writer,
		SampleRate: sampleRate,
		SampleDur:  sampleDur,
	}
}

// Play is generic approach to play supported audio contents
func (p *Playback) Play(reader io.Reader, mimeType string) error {
	switch mimeType {
	case "audio/wav", "audio/x-wav", "audio/wav-x", "audio/vnd.wave":
	default:
		return fmt.Errorf("unsuported content type %q", mimeType)
	}

	written, err := p.streamWav(reader, p.writer)
	if err != nil {
		return err
	}

	if written == 0 {
		return fmt.Errorf("nothing written")
	}
	p.totalWritten += written
	return nil
}

func (p *Playback) PlayFile(ctx context.Context, filename string) (err error) {
	file, err := os.Open(filename)
	if err != nil {
		return err
	}
	defer file.Close()
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

var playBufPool = sync.Pool{
	New: func() any {
		// Increase this size if there will be support for larger pools
		return make([]byte, 320)
	},
}

func (p *Playback) streamWav(body io.Reader, playWriter io.Writer) (int, error) {
	// dec := audio.NewWavDecoderStreamer(body)
	dec := audio.NewWavReader(body)
	if err := dec.ReadHeaders(); err != nil {
		return 0, err
	}
	if dec.BitsPerSample != 16 {
		return 0, fmt.Errorf("received bitdepth=%d, but only 16 bit PCM supported", dec.BitsPerSample)
	}
	if dec.SampleRate != p.SampleRate {
		return 0, fmt.Errorf("only 8000 sample rate supported")
	}

	// We need to read and packetize to 20 ms
	sampleDurMS := int(p.SampleDur.Milliseconds())
	payloadSize := int(dec.BitsPerSample) / 8 * int(dec.NumChannels) * int(dec.SampleRate) / 1000 * sampleDurMS

	buf := playBufPool.Get()
	payloadBuf := buf.([]byte)[:payloadSize] // 20 ms

	return wavCopy(dec, playWriter, payloadBuf)
}

func streamWavRTP(body io.Reader, rtpWriter *media.RTPPacketWriter) (int, error) {
	pt := rtpWriter.PayloadType
	enc, err := audio.NewPCMEncoder(pt, rtpWriter)
	if err != nil {
		return 0, err
	}

	p := Playback{
		writer:     enc,
		SampleRate: rtpWriter.SampleRate,
		SampleDur:  20 * time.Millisecond,
	}
	return p.streamWav(body, enc)
}

func wavCopy(dec *audio.WavReader, playWriter io.Writer, payloadBuf []byte) (int, error) {
	totalWritten := 0
outloop:
	for {
		ch, err := dec.NextChunk()
		if err != nil {
			if errors.Is(err, io.EOF) {
				break
			}
			return 0, err
		}

		if ch.ID != riff.DataFormatID && ch.ID != [4]byte{} {
			// Until we reach data chunk we will draining
			ch.Drain()
			continue
		}

		for {
			n, err := ch.Read(payloadBuf)
			if err != nil {
				if errors.Is(err, io.EOF) {
					break outloop
				}
				return 0, err
			}

			// Ticker has already correction for slow operation so this is enough
			n, err = playWriter.Write(payloadBuf[:n])
			if err != nil {
				return 0, err
			}
			totalWritten += n
		}
	}
	return totalWritten, nil
}
