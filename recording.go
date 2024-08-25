// SPDX-License-Identifier: MPL-2.0
// Copyright (C) 2024 Emir Aganovic

package diago

import (
	"errors"
	"fmt"
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

	// monitorWriter is monitoring writer stream
	monitorWriter io.Writer
	// monitorReader is monitoring reader strea
	monitorReader io.Reader

	// Writer is our store
	// It will be closed toggether when recording is closed. Should be rare case
	// to keep writer open after recording
	// In some cases this is needed where for example WavWriter will update headers on close
	Writer io.WriteSeeker

	wavWriter *audio.WavWriter

	pcmDecWriter *audio.PCMDecoder
	pcmDecReader *audio.PCMDecoder

	mu          sync.Mutex
	paused      atomic.Bool
	bufPCM      []byte
	leftN       int
	rightN      int
	BitDepth    int
	numChannels int
}

// NewRecordingWav constructs recording for storing WAV
func NewRecordingWav(id string, w io.WriteSeeker) *Recording {
	return &Recording{
		ID:     id,
		Writer: w, // Set as our default writer
	}
}

// func newRecordingWav(readCodec media.Codec, writeCodec media.Codec,
// 	mreader io.Reader, mwriter io.Writer, w io.WriteSeeker) (*Recording, error) {
// 	rec := NewRecordingWav(uuid.NewString(), w)
// 	rec.initStreams(readCodec, writeCodec, mreader, mwriter)
// 	return rec, nil
// }

func (r *Recording) initStreams(readCodec media.Codec, writeCodec media.Codec,
	mreader io.Reader, mwriter io.Writer) error {

	if mreader == nil {
		return fmt.Errorf("monitor reader is not present. Is session Answered?")
	}
	if mwriter == nil {
		return fmt.Errorf("monitor writer is not present. Is session Answered?")
	}

	// TODO: do we need resampling if this codecs are not same?
	// Problem storing than as single wav file is not possible
	decR, eR := audio.NewPCMDecoderReader(readCodec.PayloadType, nil)
	decW, eW := audio.NewPCMDecoderWriter(writeCodec.PayloadType, nil)
	if err := errors.Join(eR, eW); err != nil {
		return err
	}

	r.monitorReader = mreader
	r.monitorWriter = mwriter
	r.pcmDecReader = decR
	r.pcmDecWriter = decW
	r.Init()

	wawWriter := audio.WavWriter{
		SampleRate:  int(readCodec.SampleRate),
		BitDepth:    r.BitDepth,
		NumChans:    2, // We will store
		AudioFormat: 1, // PCM
		W:           r.Writer,
	}

	r.wavWriter = &wawWriter

	return nil
}

func (r *Recording) Init() {
	r.bufPCM = make([]byte, RecordingFlushSize)
	if r.BitDepth == 0 {
		r.BitDepth = 16
	}

	r.rightN = r.BitDepth / 8
	r.numChannels = 2
}

func (r *Recording) Close() error {
	log.Debug().Str("id", r.ID).Msg("Saving record")
	if r.wavWriter == nil {
		return fmt.Errorf("no format writer setup")
	}
	// do flush
	off := min(r.leftN, r.rightN)

	fmt.Println("flushing", off, r.leftN, r.rightN, len(r.bufPCM[:off]))
	if _, err := r.wavWriter.Write(r.bufPCM[:off]); err != nil {
		return err
	}

	return r.wavWriter.Close()
}

func (r *Recording) Pause(toggle bool) {
	r.paused.Store(toggle)
}

func (r *Recording) Read(b []byte) (int, error) {
	n, err := r.monitorReader.Read(b)
	if err != nil {
		// We could here close our writer
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
	n, err := r.monitorWriter.Write(b)
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

	size := r.BitDepth / 8
	numChannels := r.numChannels

	off := &r.leftN
	if channel == 2 {
		off = &r.rightN
	}

	buf := r.bufPCM[:*off]
	available := cap(r.bufPCM) - len(buf)

	// Flush if needed
	if available < len(lpcm)*numChannels {
		if _, err := r.wavWriter.Write(buf); err != nil {
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

// Attach attaches to media of session
// This in other words chains new media reader,writers and monitors session
// NOTE: Listen must be called after in order recording stores this stream
func (r *Recording) Attach(d DialogSession) error {
	// TODO
	// We want to have ability to find all recordings on system and refernce them
	// with DialogSession they are listening
	m := d.Media()
	codec := media.CodecFromSession(m.mediaSession)

	err := r.initStreams(
		codec, codec,
		m.AudioReader(), m.AudioWriter(),
	)
	if err != nil {
		return err
	}

	// Chain recording as new stream
	m.setAudioReader(r)
	m.setAudioWriter(r)
	return nil
}
