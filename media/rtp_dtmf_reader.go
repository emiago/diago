// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package media

import (
	"context"
	"io"
	"log/slog"
)

type RTPDtmfReader struct {
	codec        Codec // Depends on media session. Defaults to 101 per current mapping
	reader       io.Reader
	packetReader *RTPPacketReader

	lastEv  DTMFEvent
	dtmf    rune
	dtmfSet bool
}

// RTP DTMF writer is midleware for reading DTMF events
// It reads from io Reader and checks packet Reader
func NewRTPDTMFReader(codec Codec, packetReader *RTPPacketReader, reader io.Reader) *RTPDtmfReader {
	return &RTPDtmfReader{
		codec:        codec,
		packetReader: packetReader,
		reader:       reader,
		// dmtfs:        make([]rune, 0, 5), // have some
	}
}

// Write is RTP io.Writer which adds more sync mechanism
func (w *RTPDtmfReader) Read(b []byte) (int, error) {
	n, err := w.reader.Read(b)
	if err != nil {
		// Signal our reader that no more dtmfs will be read
		// close(w.dtmfCh)
		return n, err
	}

	// Check is this DTMF
	hdr := w.packetReader.PacketHeader
	if hdr.PayloadType != w.codec.PayloadType {
		return n, nil
	}

	// Now decode DTMF
	ev := DTMFEvent{}
	if err := DTMFDecode(b, &ev); err != nil {
		slog.Error("Failed to decode DTMF event", "error", err)
	}
	w.processDTMFEvent(ev)
	return n, nil
}

func (w *RTPDtmfReader) processDTMFEvent(ev DTMFEvent) {
	if DefaultLogger().Handler().Enabled(context.Background(), slog.LevelDebug) {
		// Expensive call on logger
		DefaultLogger().Debug("Processing DTMF event", "ev", ev)
	}
	if ev.EndOfEvent {
		if w.lastEv.Duration == 0 {
			return
		}
		// Does this match to our last ev
		// Consider Event can be 0, that is why we check is also lastEv.Duration set
		if w.lastEv.Event != ev.Event {
			return
		}

		dur := ev.Duration - w.lastEv.Duration
		if dur <= 3*160 { // Expect at least ~50ms duration
			DefaultLogger().Debug("Received DTMF packet but short duration", "dur", dur)
			return
		}

		w.dtmf = DTMFToRune(ev.Event)
		w.dtmfSet = true
		w.lastEv = DTMFEvent{}
		return
	}
	if w.lastEv.Duration > 0 && w.lastEv.Event == ev.Event {
		return
	}
	w.lastEv = ev
}

func (w *RTPDtmfReader) ReadDTMF() (rune, bool) {
	defer func() { w.dtmfSet = false }()
	return w.dtmf, w.dtmfSet
	// dtmf, ok := <-w.dtmfCh
	// return DTMFToRune(dtmf), ok
}
