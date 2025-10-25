// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

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
	mu          sync.RWMutex
	writer      RTPWriter
	clockTicker *time.Ticker

	// After each write packet header is saved for more reading
	PacketHeader rtp.Header
	// packet is temporarly packet holder for header and data
	packet rtp.Packet
	// SSRC is readOnly and it is not changed
	SSRC uint32

	payloadType uint8
	sampleRate  uint32

	// Internals
	// clock rate is decided based on media
	sampleRateTimestamp uint32
	lastSampleTime      time.Time
	seqWriter           RTPExtendedSequenceNumber
	nextTimestamp       uint32
	initTimestamp       uint32
}

// RTPPacketWriter packetize payload in RTP packet before passing on media session
// Not having:
// - random Timestamp
// - allow different clock rate
// - CSRC contribution source
// - Silence detection and marker set
// updateClockRate- Padding and encryyption
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

// NewRTPPacketWriterSession creates RTPPacketWriter and attaches RTP Session expected values
func NewRTPPacketWriterSession(sess *RTPSession) *RTPPacketWriter {
	codec := CodecAudioFromSession(sess.Sess)
	w := NewRTPPacketWriter(sess, codec)
	// We need to add our SSRC due to sender report, which can be empty until data comes
	// It is expected that nothing travels yet through rtp session
	// sess.writeStats.SSRC = w.SSRC
	// sess.writeStats.sampleRate = w.sampleRate
	w.writer = sess
	return w
}

func (w *RTPPacketWriter) updateClockRate(cod Codec) {
	w.sampleRateTimestamp = cod.SampleTimestamp()
	if w.clockTicker != nil {
		w.clockTicker.Reset(cod.SampleDur)
	} else {
		w.clockTicker = time.NewTicker(cod.SampleDur)
	}
}

// ResetTimestamp can mark new stream comming. If stream is continuous it will add timestamp difference
// MUST Not be called during stream Write
func (p *RTPPacketWriter) ResetTimestamp() {
	p.mu.Lock()
	defer p.mu.Unlock()

	if !p.lastSampleTime.IsZero() {
		// Detect delays in audio and update RTP timestamp
		t := time.Now()
		diff := t.Sub(p.lastSampleTime)
		diffTimestamp := uint32(diff.Seconds() * float64(p.sampleRate))

		p.nextTimestamp += diffTimestamp
		p.initTimestamp = p.nextTimestamp // This will now make Marker true
		return
	}
}

// InitTimestamp returns init RTP timestamp
func (p *RTPPacketWriter) InitTimestamp() uint32 {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.initTimestamp
}

func (p *RTPPacketWriter) DelayTimestamp(ofsset uint32) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.nextTimestamp += ofsset
}

// Write implements io.Writer and does payload RTP packetization
// Media clock rate is determined
// For more control or dynamic payload WriteSamples can be used
// It is not thread safe and order of payload frames is required
func (p *RTPPacketWriter) Write(b []byte) (int, error) {
	p.mu.RLock()
	n, err := p.WriteSamples(b, p.sampleRateTimestamp, p.nextTimestamp == p.initTimestamp, p.payloadType)
	p.mu.RUnlock()
	p.lastSampleTime = <-p.clockTicker.C
	return n, err
}

// WriteSamples allows to skip default packet rate.
// This is useful if you need to write different payload but keeping same SSRC
func (p *RTPPacketWriter) WriteSamples(payload []byte, sampleRateTimestamp uint32, marker bool, payloadType uint8) (int, error) {
	writer := p.writer
	pkt := &p.packet
	pkt.Header = rtp.Header{
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
		// CSRC:           []uint32{},
	}
	pkt.Payload = payload
	p.nextTimestamp += sampleRateTimestamp

	err := writer.WriteRTP(pkt)
	// store header for reading. NOTE: in case pointers in header, do nil first
	p.PacketHeader = pkt.Header
	return len(pkt.Payload), err
}

func (p *RTPPacketWriter) WriteWithExt(payload []byte, id uint8, pt []byte, extProfile uint16) (int, error) {
	sampleRateTimestamp := p.sampleRateTimestamp
	marker := p.nextTimestamp == p.initTimestamp
	if len(payload) == 0 {
		sampleRateTimestamp = 0
		marker = false
	}
	p.mu.RLock()
	n, err := p.writeExt(payload, sampleRateTimestamp, marker, id, pt, extProfile)
	p.mu.RUnlock()
	p.lastSampleTime = <-p.clockTicker.C
	return n, err
}

func (p *RTPPacketWriter) writeExt(payload []byte, sampleRateTimestamp uint32, marker bool, id uint8, pt []byte, extProfile uint16) (int, error) {
	writer := p.writer
	pkt := &p.packet
	pkt.Header = rtp.Header{
		Version:        2,
		Padding:        false,
		Extension:      true,
		Marker:         marker,
		PayloadType:    p.payloadType,
		Timestamp:      p.nextTimestamp,
		SequenceNumber: p.seqWriter.NextSeqNumber(),
		SSRC:           p.SSRC,
		// CSRC:           []uint32{},
	}
	pkt.Payload = payload
	p.nextTimestamp += sampleRateTimestamp

	pkt.Extension = true
	pkt.ExtensionProfile = extProfile
	if err := pkt.SetExtension(id, pt); err != nil {
		return 0, err
	}

	err := writer.WriteRTP(pkt)
	// store header for reading. NOTE: in case pointers in header, do nil first
	p.PacketHeader = pkt.Header
	return len(pkt.Payload), err
}

func (w *RTPPacketWriter) Writer() RTPWriter {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.writer
}

// UpdateRTPSession updates rtp writer from current rtp session due to REINVITE
// It is expected that this is now new RTP Session and it is expected tha:
// - Statistics will be reset (SSRC=0) -> Fresh Start of Quality monitoring
// - Should not lead inacurate reporting
// - In case CODEC change than RTP should reset stats anyway
func (w *RTPPacketWriter) UpdateRTPSession(rtpSess *RTPSession) {
	w.mu.Lock()
	defer w.mu.Unlock()

	// In case of codec cha
	codec := CodecAudioFromSession(rtpSess.Sess)
	w.payloadType = codec.PayloadType
	w.sampleRate = codec.SampleRate
	w.updateClockRate(codec)
	w.writer = rtpSess
	// rtpSess.writeStats.SSRC = w.SSRC
	// rtpSess.writeStats.sampleRate = w.sampleRate
}
