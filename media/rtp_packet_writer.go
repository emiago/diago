// SPDX-License-Identifier: MPL-2.0
// Copyright (C) 2024 Emir Aganovic

package media

import (
	"math/rand"
	"sync"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
)

type RTPWriter interface {
	WriteRTP(p *rtp.Packet) error
}
type RTPWriterRaw interface {
	WriteRTPRaw(buf []byte) (int, error) // -> io.Writer
}

type RTCPWriter interface {
	WriteRTCP(p rtcp.Packet) error
}

type RTCPWriterRaw interface {
	WriteRTCPRaw(buf []byte) (int, error) // -> io.Writer
}

// RTPPacketWriter packetize any payload before pushing to active media session
// It creates SSRC as identifier and all packets sent will be with this SSRC
// For multiple streams, multiple RTP Writer needs to be created
type RTPPacketWriter struct {
	mu         sync.RWMutex
	RTPSession *RTPSession
	Sess       *MediaSession
	writer     RTPWriter

	// After each write this is set as packet.
	LastPacket rtp.Packet
	// SSRC is readOnly and it is not changed
	SSRC uint32

	payloadType uint8
	sampleRate  uint32

	// Internals
	// clock rate is decided based on media
	sampleRateTimestamp uint32
	clockTicker         *time.Ticker
	seqWriter           RTPExtendedSequenceNumber
	nextTimestamp       uint32
	initTimestamp       uint32
}

// RTP writer packetize payload in RTP packet before passing on media session
// Not having:
// - random Timestamp
// - allow different clock rate
// - CSRC contribution source
// - Silence detection and marker set
// updateClockRate- Padding and encryyption
func NewRTPPacketWriterSession(sess *RTPSession) *RTPPacketWriter {
	codec := CodecFromSession(sess.Sess)
	w := NewRTPPacketWriter(sess, codec)
	// We need to add our SSRC due to sender report, which can be empty until data comes
	// It is expected that nothing travels yet through rtp session
	sess.writeStats.SSRC = w.SSRC
	sess.writeStats.sampleRate = w.sampleRate
	w.RTPSession = sess
	w.writer = sess
	return w
}

// NewRTPPacketWriterMedia is left for backward compability. It does not add RTCP reporting
// func NewRTPPacketWriterMedia(sess *MediaSession) *RTPPacketWriter {
// 	codec := codecFromSession(sess)

// 	w := NewRTPPacketWriter(sess, codec)
// 	w.Sess = sess // Backward compatibility
// 	// w := RTPWriter{
// 	// 	Sess:        sess,
// 	// 	Writer:      sess,
// 	// 	seqWriter:   NewRTPSequencer(),
// 	// 	PayloadType: codec.PayloadType,
// 	// 	SampleRate:  codec.SampleRate,
// 	// 	SSRC:        rand.Uint32(),
// 	// 	// initTimestamp: rand.Uint32(), // TODO random start timestamp
// 	// 	// MTU:         1500,

// 	// 	// TODO: CSRC CSRC is contribution source identifiers.
// 	// 	// This is set when media is passed trough mixer/translators and original SSRC wants to be preserverd
// 	// }

// 	// w.nextTimestamp = w.initTimestamp
// 	// w.updateClockRate(codec)

// 	return w
// }

// RTPSession should be used for media quality reporting

func NewRTPPacketWriter(writer RTPWriter, codec Codec) *RTPPacketWriter {
	w := RTPPacketWriter{
		writer:      writer,
		seqWriter:   NewRTPSequencer(),
		payloadType: codec.PayloadType,
		sampleRate:  codec.SampleRate,
		SSRC:        rand.Uint32(),
		// initTimestamp: rand.Uint32(), // TODO random start timestamp
		// MTU:         1500,

		// TODO: CSRC CSRC is contribution source identifiers.
		// This is set when media is passed trough mixer/translators and original SSRC wants to be preserverd
	}

	w.nextTimestamp = w.initTimestamp
	w.updateClockRate(codec)
	return &w
}

func (w *RTPPacketWriter) updateClockRate(cod Codec) {
	w.sampleRateTimestamp = cod.SampleTimestamp()
	if w.clockTicker != nil {
		w.clockTicker.Reset(cod.SampleDur)
	} else {
		w.clockTicker = time.NewTicker(cod.SampleDur)
	}
}

// Write implements io.Writer and does payload RTP packetization
// Media clock rate is determined
// For more control or dynamic payload WriteSamples can be used
// It is not thread safe and order of payload frames is required
// Has no capabilities (yet):
// - MTU UDP limit handling
// - Media clock rate of payload is consistent
// - Packet loss detection
// - RTCP generating
func (p *RTPPacketWriter) Write(b []byte) (int, error) {
	p.mu.RLock()
	n, err := p.WriteSamples(p.writer, b, p.sampleRateTimestamp, p.nextTimestamp == p.initTimestamp, p.payloadType)
	p.mu.RUnlock()
	<-p.clockTicker.C
	return n, err
}

func (p *RTPPacketWriter) WriteSamples(writer RTPWriter, payload []byte, sampleRateTimestamp uint32, marker bool, payloadType uint8) (int, error) {
	pkt := rtp.Packet{
		Header: rtp.Header{
			Version:     2,
			Padding:     false,
			Extension:   false,
			Marker:      marker,
			PayloadType: payloadType,
			// Timestamp should increase linear and monotonic for media clock
			// Payload must be in same clock rate
			// TODO: what about wrapp arround
			Timestamp:      p.nextTimestamp,
			SequenceNumber: p.seqWriter.NextSeqNumber(),
			SSRC:           p.SSRC,
			CSRC:           []uint32{},
		},
		Payload: payload,
	}

	p.LastPacket = pkt
	p.nextTimestamp += sampleRateTimestamp

	err := writer.WriteRTP(&pkt)
	// if p.RTPSession != nil {
	// 	err := p.RTPSession.WriteRTP(&pkt)
	// 	return len(pkt.Payload), err
	// }

	// err := p.Sess.WriteRTP(&pkt)
	return len(pkt.Payload), err
}

func (w *RTPPacketWriter) Writer() RTPWriter {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.writer
}

func (w *RTPPacketWriter) UpdateRTPSession(rtpSess *RTPSession) {
	w.mu.Lock()
	defer w.mu.Unlock()

	// THis is buggy for some reason
	codec := CodecFromSession(rtpSess.Sess)
	w.Sess = rtpSess.Sess
	w.RTPSession = rtpSess
	w.payloadType = codec.PayloadType
	w.sampleRate = codec.SampleRate
	w.updateClockRate(codec)

	rtpSess.writeStats.SSRC = w.SSRC
	rtpSess.writeStats.sampleRate = w.sampleRate
}

// func (w *RTPPacketWriter) SetRTPWriter(rtpWriter *RTPPacketWriter) {

// 	w.mu.Lock()
// 	ssrc := w.RTPPacketWriter.SSRC
// 	sampleRate := w.RTPPacketWriter.SampleRate

// 	// Preserve same stream ID timestamp but only in case same clock rate
// 	// https://datatracker.ietf.org/doc/html/rfc7160#section-4.1
// 	if rtpWriter.SampleRate == sampleRate {
// 		rtpWriter.SSRC = ssrc
// 		rtpWriter.initTimestamp = w.RTPPacketWriter.initTimestamp
// 		rtpWriter.nextTimestamp = w.RTPPacketWriter.nextTimestamp
// 	}
// 	w.RTPPacketWriter = rtpWriter
// 	w.mu.Unlock()
// }

// // Experimental
// //
// // RTPPacketWriterConcurent allows updating RTPSession on RTPWriter and more (in case of reestablish)
// type RTPPacketWriterConcurent struct {
// 	*RTPPacketWriter
// 	mu sync.Mutex
// }

// func (w *RTPPacketWriterConcurent) Write(b []byte) (int, error) {
// 	w.mu.Lock()
// 	n, err := w.RTPPacketWriter.Write(b)
// 	w.mu.Unlock()
// 	return n, err
// }

// func (w *RTPPacketWriterConcurent) SetRTPSession(rtpSess *RTPSession) {
// 	// THis is buggy for some reason
// 	codec := codecFromSession(rtpSess.Sess)

// 	w.mu.Lock()
// 	w.RTPPacketWriter.Sess = rtpSess.Sess
// 	w.RTPPacketWriter.RTPSession = rtpSess
// 	w.PayloadType = codec.PayloadType
// 	w.SampleRate = codec.SampleRate
// 	w.updateClockRate(codec)

// 	rtpSess.writeStats.SSRC = w.SSRC
// 	rtpSess.writeStats.sampleRate = w.SampleRate
// 	w.mu.Unlock()
// }

// func (w *RTPPacketWriterConcurent) SetRTPWriter(rtpWriter *RTPPacketWriter) {

// 	w.mu.Lock()
// 	ssrc := w.RTPPacketWriter.SSRC
// 	sampleRate := w.RTPPacketWriter.SampleRate

// 	// Preserve same stream ID timestamp but only in case same clock rate
// 	// https://datatracker.ietf.org/doc/html/rfc7160#section-4.1
// 	if rtpWriter.SampleRate == sampleRate {
// 		rtpWriter.SSRC = ssrc
// 		rtpWriter.initTimestamp = w.RTPPacketWriter.initTimestamp
// 		rtpWriter.nextTimestamp = w.RTPPacketWriter.nextTimestamp
// 	}
// 	w.RTPPacketWriter = rtpWriter
// 	w.mu.Unlock()
// }
