// SPDX-License-Identifier: BSD-2-Clause
// Copyright (C) 2024 Emir Aganovic

package media

import (
	"math/rand"
	"sync"
	"sync/atomic"
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
	RTPSession *RTPSession
	Sess       *MediaSession
	Writer     RTPWriter

	// After each write this is set as packet.
	LastPacket rtp.Packet
	OnRTP      func(pkt *rtp.Packet)

	// This properties are read only or can be changed only after creating writer
	PayloadType uint8
	SSRC        uint32
	SampleRate  uint32

	// Internals
	// clock rate is decided based on media
	sampleRateTimestamp uint32
	closed              atomic.Bool
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
	w := NewRTPPacketWriterMedia(sess.Sess)
	// We need to add our SSRC due to sender report, which can be empty until data comes
	// It is expected that nothing travels yet through rtp session
	sess.writeStats.SSRC = w.SSRC
	sess.writeStats.sampleRate = w.SampleRate
	w.RTPSession = sess
	w.Writer = sess
	return w
}

// NewRTPPacketWriterMedia is left for backward compability. It does not add RTCP reporting
// RTPSession should be used for media quality reporting
func NewRTPPacketWriterMedia(sess *MediaSession) *RTPPacketWriter {
	codec := codecFromSession(sess)

	w := NewRTPPacketWriter(sess, codec)
	w.Sess = sess // Backward compatibility
	// w := RTPWriter{
	// 	Sess:        sess,
	// 	Writer:      sess,
	// 	seqWriter:   NewRTPSequencer(),
	// 	PayloadType: codec.PayloadType,
	// 	SampleRate:  codec.SampleRate,
	// 	SSRC:        rand.Uint32(),
	// 	// initTimestamp: rand.Uint32(), // TODO random start timestamp
	// 	// MTU:         1500,

	// 	// TODO: CSRC CSRC is contribution source identifiers.
	// 	// This is set when media is passed trough mixer/translators and original SSRC wants to be preserverd
	// }

	// w.nextTimestamp = w.initTimestamp
	// w.updateClockRate(codec)

	return w
}

func NewRTPPacketWriter(writer RTPWriter, codec Codec) *RTPPacketWriter {
	w := RTPPacketWriter{
		Writer:      writer,
		seqWriter:   NewRTPSequencer(),
		PayloadType: codec.PayloadType,
		SampleRate:  codec.SampleRate,
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
	n, err := p.WriteSamples(b, p.sampleRateTimestamp, p.nextTimestamp == p.initTimestamp, p.PayloadType)
	<-p.clockTicker.C
	return n, err
}

func (p *RTPPacketWriter) WriteSamples(payload []byte, clockRateTimestamp uint32, marker bool, payloadType uint8) (int, error) {
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

	if p.OnRTP != nil {
		p.OnRTP(&pkt)
	}

	p.LastPacket = pkt
	p.nextTimestamp += clockRateTimestamp

	err := p.Writer.WriteRTP(&pkt)
	// if p.RTPSession != nil {
	// 	err := p.RTPSession.WriteRTP(&pkt)
	// 	return len(pkt.Payload), err
	// }

	// err := p.Sess.WriteRTP(&pkt)
	return len(pkt.Payload), err
}

// Experimental
//
// RTPPacketWriterConcurent allows updating RTPSession on RTPWriter and more (in case of reestablish)
type RTPPacketWriterConcurent struct {
	*RTPPacketWriter
	mu sync.Mutex
}

func (w *RTPPacketWriterConcurent) Write(b []byte) (int, error) {
	w.mu.Lock()
	n, err := w.RTPPacketWriter.Write(b)
	w.mu.Unlock()
	return n, err
}

func (w *RTPPacketWriterConcurent) SetRTPSession(rtpSess *RTPSession) {
	// THis is buggy for some reason
	codec := codecFromSession(rtpSess.Sess)

	w.mu.Lock()
	w.RTPPacketWriter.Sess = rtpSess.Sess
	w.RTPPacketWriter.RTPSession = rtpSess
	w.PayloadType = codec.PayloadType
	w.SampleRate = codec.SampleRate
	w.updateClockRate(codec)

	rtpSess.writeStats.SSRC = w.SSRC
	rtpSess.writeStats.sampleRate = w.SampleRate
	w.mu.Unlock()
}

func (w *RTPPacketWriterConcurent) SetRTPWriter(rtpWriter *RTPPacketWriter) {

	w.mu.Lock()
	ssrc := w.RTPPacketWriter.SSRC
	sampleRate := w.RTPPacketWriter.SampleRate

	// Preserve same stream ID timestamp but only in case same clock rate
	// https://datatracker.ietf.org/doc/html/rfc7160#section-4.1
	if rtpWriter.SampleRate == sampleRate {
		rtpWriter.SSRC = ssrc
		rtpWriter.initTimestamp = w.RTPPacketWriter.initTimestamp
		rtpWriter.nextTimestamp = w.RTPPacketWriter.nextTimestamp
	}
	w.RTPPacketWriter = rtpWriter
	w.mu.Unlock()
}
