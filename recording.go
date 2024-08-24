// SPDX-License-Identifier: MPL-2.0
// Copyright (C) 2024 Emir Aganovic

package diago

import (
	"bytes"
	"errors"
	"io"
	"sync"
	"sync/atomic"

	"github.com/emiago/diago/audio"
	"github.com/emiago/diago/media"
	"github.com/rs/zerolog/log"
)

var (
	RecordingFlushSize = 1024 * 4
)

type Recording struct {
	// Is id of recording
	ID string

	// MonitorWriter is monitoring writer stream
	MonitorWriter io.Writer
	pcmDecWriter  *audio.PCMDecoder

	// MonitorReader is monitoring reader strea
	MonitorReader io.Reader
	pcmDecReader  *audio.PCMDecoder

	// Writer is our store
	// It will be closed toggether when recording is closed. Should be rare case
	// to keep writer open after recording
	// In some cases this is needed where for example WavWriter will update headers on close
	Writer io.WriteCloser

	mu          sync.Mutex
	paused      atomic.Bool
	bufPCM      []byte
	leftN       int
	rightN      int
	BitSize     int
	numChannels int
}

func NewRecordingWav(readCodec media.Codec, writeCodec media.Codec,
	mreader io.Reader, mwriter io.Writer, w io.WriteSeeker) (*Recording, error) {

	decR, eR := audio.NewPCMDecoderReader(readCodec.PayloadType, nil)
	decW, eW := audio.NewPCMDecoderWriter(writeCodec.PayloadType, nil)
	if err := errors.Join(eR, eW); err != nil {
		return nil, err
	}

	rec := &Recording{
		MonitorReader: mreader,
		pcmDecReader:  decR,
		MonitorWriter: bytes.NewBuffer(make([]byte, 0, 50)),
		pcmDecWriter:  decW,
		Writer:        audio.NewWavWriter(w),
		BitSize:       16,
	}
	rec.Init()
	return rec, nil
}

func (r *Recording) Init() {
	r.bufPCM = make([]byte, RecordingFlushSize)
	if r.BitSize == 0 {
		r.BitSize = 16
	}

	r.rightN = r.BitSize / 8
	r.numChannels = 2
}

func (r *Recording) Close() error {
	// do flush
	off := min(r.leftN, r.rightN)

	if _, err := r.Writer.Write(r.bufPCM[:off]); err != nil {
		return err
	}

	return r.Writer.Close()
}

func (r *Recording) Pause(toggle bool) {
	r.paused.Store(toggle)
}

func (r *Recording) Read(b []byte) (int, error) {
	n, err := r.MonitorReader.Read(b)
	if err != nil {
		return 0, err
	}
	if r.paused.Load() {
		return n, nil
	}

	// Decode to PCM
	lpcm := r.pcmDecReader.Decoder(b[:n])
	if err := r.writePCM(lpcm, 1); err != nil {
		log.Error().Err(err).Msg("Monitor Read failed to write")
	}
	return n, nil
}

func (r *Recording) Write(b []byte) (int, error) {
	n, err := r.MonitorWriter.Write(b)
	if err != nil {
		return 0, err
	}

	if r.paused.Load() {
		return n, nil
	}

	// Decode to PCM
	lpcm := r.pcmDecWriter.Decoder(b[:n])
	if err := r.writePCM(lpcm, 2); err != nil {
		log.Error().Err(err).Msg("Monitor Read failed to write")
	}
	return n, nil
}

func (r *Recording) writePCM(lpcm []byte, channel int) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	size := r.BitSize / 8
	numChannels := r.numChannels

	off := &r.leftN
	if channel == 2 {
		off = &r.rightN
	}

	buf := r.bufPCM[:*off]
	available := cap(r.bufPCM) - len(buf)

	// Flush if needed
	if available < len(lpcm)*numChannels {
		if _, err := r.Writer.Write(buf); err != nil {
			return err
		}
		r.leftN = 0
		r.rightN = size
	}
	// }
	available = cap(r.bufPCM) - *off
	if available < len(lpcm)*numChannels {
		log.Error().Msg("Recording buffer is too small for this stream. Please increase")
		return io.ErrShortBuffer
	}

	for i := 0; i < len(lpcm); i += size {
		*off += copy(r.bufPCM[*off:], lpcm[i:i+size])
		*off += size * (numChannels - 1) // shift for multi channels
	}

	return nil
}
