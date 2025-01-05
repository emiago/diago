// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package media

import (
	"errors"
	"io"
	"net"
	"sync"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// var rtpBufPool = &sync.Pool{
// 	New: func() any { return make([]byte, RTPBufSize) },
// }

type RTPReader interface {
	ReadRTP(buf []byte, p *rtp.Packet) (int, error)
}

type RTPReaderRaw interface {
	ReadRTPRaw(buf []byte) (int, error)
}

type RTCPReader interface {
	ReadRTCP(buf []byte, pkts []rtcp.Packet) (n int, err error)
}

type RTPCReaderRaw interface {
	ReadRTCPRaw(buf []byte) (int, error)
}

// RTPPacketReader reads RTP packet and extracts payload and header
type RTPPacketReader struct {
	mu  sync.RWMutex
	log zerolog.Logger

	reader RTPReader

	// PacketHeader is stored after calling Read
	// Safe to read only in same goroutine as Read
	PacketHeader rtp.Header
	// packet is temporarly packet holder for header and data
	packet rtp.Packet

	// payloadType uint8
	seqReader RTPExtendedSequenceNumber

	unreadPayload []byte
	unread        int
	// We want to track our last SSRC.
	lastSSRC uint32
}

// NewRTPPacketReaderSession just helper constructor
func NewRTPPacketReaderSession(sess *RTPSession) *RTPPacketReader {
	r := newRTPPacketReaderMedia(sess.Sess)
	r.reader = sess
	return r
}

// used for tests only
func newRTPPacketReaderMedia(sess *MediaSession) *RTPPacketReader {
	codec := CodecFromSession(sess)
	w := NewRTPPacketReader(sess, codec)
	return w
}

func NewRTPPacketReader(reader RTPReader, codec Codec) *RTPPacketReader {
	w := RTPPacketReader{
		reader: reader,
		// payloadType:   codec.PayloadType,
		seqReader: RTPExtendedSequenceNumber{},
		// unreadPayload: make([]byte, RTPBufSize),
		// rtpBuffer:     make([]byte, RTPBufSize),
		log: log.With().Str("caller", "media").Logger(),
	}

	return &w
}

// Read Implements io.Reader and extracts Payload from RTP packet
// has no input queue or sorting control of packets
// Buffer is used for reading headers and Headers are stored in PacketHeader
//
// NOTE: Consider that if you are passsing smaller buffer than RTP header+payload, io.ErrShortBuffer is returned
func (r *RTPPacketReader) Read(b []byte) (int, error) {
	if r.unread > 0 {
		n := r.readPayload(b, r.unreadPayload[:r.unread])
		return n, nil
	}

	var n int

	// For io.ReadAll buffer size is constantly changing and starts small. Normally user should set buf > RTPBufSize
	// Use unread buffer and still avoid alloc
	buf := b
	unreadPayload := b
	if len(b) < RTPBufSize {
		if r.unreadPayload == nil {
			r.log.Debug().Msg("Read RTP buf is < RTPBufSize!!! Creating larger buffer!!!")
			r.unreadPayload = make([]byte, RTPBufSize)
		}

		buf = r.unreadPayload
		unreadPayload = r.unreadPayload
	}

	pkt := &r.packet
	pkt.Payload = unreadPayload

	r.mu.RLock()
	reader := r.reader
	r.mu.RUnlock()

	rtpN, err := reader.ReadRTP(buf, pkt)
	if err != nil {
		// For now underhood IO should only net closed
		// Here we are returning EOF to be io package compatilble
		// like with func io.ReadAll
		if errors.Is(err, net.ErrClosed) {
			return 0, io.EOF
		}
		return 0, err
	}
	payloadSize := rtpN - pkt.Header.MarshalSize() - int(pkt.PaddingSize)
	// In case of DTMF we can receive different payload types
	// if pt != pkt.PayloadType {
	// 	return 0, fmt.Errorf("payload type does not match. expected=%d, actual=%d", pt, pkt.PayloadType)
	// }

	// If we are tracking this source, do check are we keep getting pkts in sequence
	if r.lastSSRC == pkt.SSRC {
		prevSeq := r.seqReader.ReadExtendedSeq()
		if err := r.seqReader.UpdateSeq(pkt.SequenceNumber); err != nil {
			r.log.Warn().Msg(err.Error())
		}

		newSeq := r.seqReader.ReadExtendedSeq()
		if prevSeq+1 != newSeq {
			r.log.Warn().Uint64("expected", prevSeq+1).Uint64("actual", newSeq).Uint16("real", pkt.SequenceNumber).Msg("Out of order pkt received")
		}
	} else {
		r.seqReader.InitSeq(pkt.SequenceNumber)
	}

	r.lastSSRC = pkt.SSRC
	r.PacketHeader = pkt.Header
	// Is there better way to compare this?
	if len(b) != len(unreadPayload) {
		// We are not using passed buffer. We need to copy payload
		pkt.Payload = pkt.Payload[:payloadSize]
		n = r.readPayload(b, pkt.Payload)
		return n, nil

	}
	return payloadSize, nil
}

func (r *RTPPacketReader) readPayload(b []byte, payload []byte) int {
	n := copy(b, payload)
	if n < len(payload) {
		written := copy(r.unreadPayload, payload[n:])
		if written < len(payload[n:]) {
			r.log.Error().Msg("Payload is huge, it will be unread")
		}
		r.unread = written
	} else {
		r.unread = 0
	}
	return n
}

func (r *RTPPacketReader) Reader() RTPReader {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.reader
}

func (r *RTPPacketReader) UpdateRTPSession(rtpSess *RTPSession) {
	r.UpdateReader(rtpSess)
	// codec := CodecFromSession(rtpSess.Sess)
	// r.mu.Lock()
	// r.RTPSession = rtpSess
	// // r.payloadType = codec.PayloadType
	// r.reader = rtpSess
	// r.mu.Unlock()
}

func (r *RTPPacketReader) UpdateReader(reader RTPReader) {
	// codec := CodecFromSession(rtpSess.Sess)
	r.mu.Lock()
	r.reader = reader
	r.mu.Unlock()
}
