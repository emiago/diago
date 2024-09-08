// SPDX-License-Identifier: MPL-2.0
// Copyright (C) 2024 Emir Aganovic

package media

import (
	"sync"
	"time"
)

type RTPDtmfWriter struct {
	codec       Codec
	payloadType uint8 // Depends on media session. Defaults to 101 per current mapping
	rtpWriter   *RTPPacketWriter

	mu sync.Mutex
}

// RTP DTMF writer is midleware for passing RTP DTMF event. It needs to be attached on packetizer
func NewRTPDTMFWriter(codec Codec, rtpPacketizer *RTPPacketWriter) *RTPDtmfWriter {
	return &RTPDtmfWriter{
		codec:     codec,
		rtpWriter: rtpPacketizer,
	}
}

// func (w *RTPDtmfWriter) Init(payloadType uint8, rtpPacketizer *RTPPacketWriter) {
// 	w.payloadType = payloadType
// 	w.rtpWriter = rtpPacketizer
// }

// Write is RTP io.Writer which adds more sync mechanism
func (w *RTPDtmfWriter) Write(b []byte) (int, error) {
	// Do we have some currently dtmf
	w.mu.Lock()
	defer w.mu.Unlock()
	// Write whatever is intended
	n, err := w.rtpWriter.Write(b)
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
	rtpWriter := w.rtpWriter

	// DTMF events are send
	evs := RTPDTMFEncode(dtmf)
	ticker := time.NewTicker(20 * time.Millisecond)
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
		_, err := rtpWriter.WriteSamples(data, 0, marker, w.payloadType)
		if err != nil {
			return err
		}
	}
	return nil
}
