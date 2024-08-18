// SPDX-License-Identifier: BSD-2-Clause
// Copyright (C) 2024 Emir Aganovic

package media

import (
	"errors"
	"fmt"
	"io"
	"net"
	"sync"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
)

type RTPReader interface {
	ReadRTP(buf []byte, p *rtp.Packet) error
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
	mu sync.RWMutex

	Sess       *MediaSession // TODO remove this
	RTPSession *RTPSession   // TODO remove this
	reader     RTPReader

	// Deprecated
	//
	// Should not be used
	OnRTP func(pkt *rtp.Packet)

	// PacketHeader is stored after calling Read this will be stored before returning
	PacketHeader rtp.Header
	PayloadType  uint8

	seqReader RTPExtendedSequenceNumber

	unreadPayload []byte
	unread        int

	// We want to track our last SSRC.
	lastSSRC uint32
}

// RTP reader consumes samples of audio from RTP session
// Use NewRTPSession to construct RTP session
func NewRTPPacketReaderSession(sess *RTPSession) *RTPPacketReader {
	r := NewRTPPacketReaderMedia(sess.Sess)
	r.RTPSession = sess
	r.reader = sess
	return r
}

// NewRTPWriterMedia is left for backward compability. It does not add RTCP reporting
// It talks directly to network
func NewRTPPacketReaderMedia(sess *MediaSession) *RTPPacketReader {
	codec := CodecFromSession(sess)

	// w := RTPReader{
	// 	Sess:        sess,
	// 	PayloadType: codec.PayloadType,
	// 	OnRTP:       func(pkt *rtp.Packet) {},

	// 	seqReader:     RTPExtendedSequenceNumber{},
	// 	unreadPayload: make([]byte, RTPBufSize),
	// }
	w := NewRTPPacketReader(sess, codec)
	w.Sess = sess // For backward compatibility

	return w
}

func NewRTPPacketReader(reader RTPReader, codec Codec) *RTPPacketReader {
	w := RTPPacketReader{
		reader:      reader,
		PayloadType: codec.PayloadType,
		OnRTP:       func(pkt *rtp.Packet) {},

		seqReader:     RTPExtendedSequenceNumber{},
		unreadPayload: make([]byte, RTPBufSize),
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
	// var err error

	// For io.ReadAll buffer size is constantly changing and starts small
	// Normally user should > RTPBufSize
	// Use unread buffer and still avoid alloc
	buf := b
	if len(b) < RTPBufSize {
		r.Sess.log.Debug().Msg("Read RTP buf is < RTPBufSize. Using internal")
		buf = r.unreadPayload
	}

	pkt := rtp.Packet{}

	r.mu.RLock()
	pt := r.PayloadType
	reader := r.reader
	r.mu.RUnlock()

	if err := reader.ReadRTP(buf, &pkt); err != nil {
		// For now underhood IO should only net closed
		// Here we are returning EOF to be io package compatilble
		// like with func io.ReadAll
		if errors.Is(err, net.ErrClosed) {
			return 0, io.EOF
		}
		return 0, err
	}

	// if r.RTPSession != nil {
	// 	if err := r.RTPSession.ReadRTP(buf, &pkt); err != nil {
	// 		if errors.Is(err, net.ErrClosed) {
	// 			return 0, io.EOF
	// 		}
	// 		return 0, err
	// 	}
	// } else if r.Sess != nil {

	// 	// Reuse read buffer.
	// 	if err := r.Sess.ReadRTP(buf, &pkt); err != nil {
	// 		if errors.Is(err, net.ErrClosed) {
	// 			return 0, io.EOF
	// 		}
	// 		return 0, err
	// 	}

	// 	// NOTE: pkt after unmarshall will hold reference on b buffer.
	// 	// Caller should do copy of PacketHeader if it reuses buffer
	// 	// if err := pkt.Unmarshal(buf[:n]); err != nil {
	// 	// 	return 0, err
	// 	// }
	// } else {

	// }

	if pt != pkt.PayloadType {
		return 0, fmt.Errorf("payload type does not match. expected=%d, actual=%d", pt, pkt.PayloadType)
	}

	// If we are tracking this source, do check are we keep getting pkts in sequence
	if r.lastSSRC == pkt.SSRC {
		prevSeq := r.seqReader.ReadExtendedSeq()
		if err := r.seqReader.UpdateSeq(pkt.SequenceNumber); err != nil {
			r.Sess.log.Warn().Msg(err.Error())
		}

		newSeq := r.seqReader.ReadExtendedSeq()
		if prevSeq+1 != newSeq {
			r.Sess.log.Warn().Uint64("expected", prevSeq+1).Uint64("actual", newSeq).Uint16("real", pkt.SequenceNumber).Msg("Out of order pkt received")
		}
	} else {
		r.seqReader.InitSeq(pkt.SequenceNumber)
	}

	r.lastSSRC = pkt.SSRC
	r.PacketHeader = pkt.Header
	r.OnRTP(&pkt)

	size := min(len(b), len(buf))
	n = r.readPayload(buf[:size], pkt.Payload)
	return n, nil
}

func (r *RTPPacketReader) readPayload(b []byte, payload []byte) int {
	n := copy(b, payload)
	if n < len(payload) {
		written := copy(r.unreadPayload, payload[n:])
		if written < len(payload[n:]) {
			r.Sess.log.Error().Msg("Payload is huge, it will be unread")
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
	codec := CodecFromSession(rtpSess.Sess)
	r.mu.Lock()
	r.RTPSession = rtpSess
	r.PayloadType = codec.PayloadType
	r.reader = rtpSess
	r.mu.Unlock()
}
