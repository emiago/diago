// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package media

import (
	"fmt"
	"io"
	"sync"
	"time"
)

// RTPComfortNoiseWriter adds explicit RFC 3389 comfort-noise packets to an
// RTP audio stream. It does not schedule packets or verify SDP negotiation.
type RTPComfortNoiseWriter struct {
	mu sync.Mutex

	packetWriter *RTPPacketWriter
	writer       io.Writer
	now          func() time.Time

	lastWriteTime      time.Time
	comfortNoiseActive bool
}

// NewRTPComfortNoiseWriter adds explicit comfort-noise writes to writer while
// keeping them on packetWriter's SSRC and RTP timeline.
func NewRTPComfortNoiseWriter(packetWriter *RTPPacketWriter, writer io.Writer) *RTPComfortNoiseWriter {
	now := time.Now
	return &RTPComfortNoiseWriter{
		packetWriter:  packetWriter,
		writer:        writer,
		now:           now,
		lastWriteTime: now(),
	}
}

// Write forwards encoded audio and resumes the RTP clock after comfort noise.
func (w *RTPComfortNoiseWriter) Write(payload []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	if w.comfortNoiseActive {
		w.packetWriter.resetTimestampBy(w.timestampOffset(w.now()))
		w.comfortNoiseActive = false
	}

	n, err := w.writer.Write(payload)
	w.lastWriteTime = w.now()
	return n, err
}

// WriteComfortNoise sends one zero-order RFC 3389 Silence Insertion
// Descriptor. Level is attenuation in -dBov and must be between 0 and 127.
// The caller is responsible for deciding when packets should be sent.
func (w *RTPComfortNoiseWriter) WriteComfortNoise(level uint8) error {
	if level > 127 {
		return fmt.Errorf("comfort noise level must be between 0 and 127: %d", level)
	}

	w.mu.Lock()
	defer w.mu.Unlock()

	now := w.now()
	offset := w.timestampOffset(now)
	_, err := w.packetWriter.writeComfortNoise([]byte{level}, offset)
	if err != nil {
		return err
	}
	w.lastWriteTime = now
	w.comfortNoiseActive = true
	return nil
}

func (w *RTPComfortNoiseWriter) timestampOffset(now time.Time) uint32 {
	if w.lastWriteTime.IsZero() || !now.After(w.lastWriteTime) {
		return 0
	}

	elapsed := now.Sub(w.lastWriteTime)
	seconds := uint64(elapsed / time.Second)
	remainder := uint64(elapsed % time.Second)
	offset := seconds*uint64(CodecComfortNoise8000.SampleRate) +
		remainder*uint64(CodecComfortNoise8000.SampleRate)/uint64(time.Second)
	return uint32(offset)
}
