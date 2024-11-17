// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package media

import (
	"io"
	"sync"
	"time"
)

type RTPDtmfWriter struct {
	codec        Codec
	writer       io.Writer
	packetWriter *RTPPacketWriter

	mu sync.Mutex
}

// RTP DTMF writer is midleware for passing RTP DTMF event.
// If it is chained it uses to block writer while writing DTFM events
func NewRTPDTMFWriter(codec Codec, rtpPacketizer *RTPPacketWriter, writer io.Writer) *RTPDtmfWriter {
	return &RTPDtmfWriter{
		codec:        codec,
		packetWriter: rtpPacketizer,
		writer:       writer,
	}
}

// Write is RTP io.Writer which adds more sync mechanism
func (w *RTPDtmfWriter) Write(b []byte) (int, error) {
	// If locked it means writer is currently writing DTMF over same stream
	w.mu.Lock()
	defer w.mu.Unlock()
	// Write whatever is intended
	n, err := w.writer.Write(b)
	if err != nil {
		return n, err
	}

	return n, nil
}

func (w *RTPDtmfWriter) WriteDTMF(dtmf rune) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writeDTMF(dtmf)
}

func (w *RTPDtmfWriter) writeDTMF(dtmf rune) error {
	// DTMF events are send directly to packet writer as they are different Codec
	packetWriter := w.packetWriter

	evs := RTPDTMFEncode(dtmf)
	ticker := time.NewTicker(w.codec.SampleDur)
	defer ticker.Stop()
	for i, e := range evs {
		data := DTMFEncode(e)
		marker := i == 0

		// https://datatracker.ietf.org/doc/html/rfc2833#section-3.6
		// 		An audio source SHOULD start transmitting event packets as soon as it
		//    recognizes an event and every 50 ms thereafter or the packet interval
		//    for the audio codec used for this session

		<-ticker.C
		// We are simulating RTP clock rate
		// timestamp should not be increased for dtmf
		_, err := packetWriter.WriteSamples(data, 0, marker, w.codec.PayloadType)
		if err != nil {
			return err
		}
	}
	return nil
}
