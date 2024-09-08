// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package media

import (
	"errors"
	"math/rand"
)

var (
	// RTP spec recomned
	maxMisorder uint16 = 100
	maxDropout  uint16 = 3000
	maxSeqNum   uint16 = 65535
)

var (
	ErrRTPSequenceOutOfOrder = errors.New("out of order")
	ErrRTPSequenceBad        = errors.New("bad sequence")
	ErrRTPSequnceDuplicate   = errors.New("sequence duplicate")
)

// RTPExtendedSequenceNumber is embedable/ replacable sequnce number generator
// For thread safety you should wrap it
type RTPExtendedSequenceNumber struct {
	seqNum           uint16 // highest sequence received/created
	wrapArroundCount uint16

	badSeq uint16
}

func NewRTPSequencer() RTPExtendedSequenceNumber {
	// There are more safer approaches but best is just SRTP
	seq := uint16(rand.Uint32())
	sn := RTPExtendedSequenceNumber{}
	sn.InitSeq(seq)
	return sn
}

func (sn *RTPExtendedSequenceNumber) InitSeq(seq uint16) {
	sn.seqNum = seq
	sn.badSeq = maxSeqNum
	sn.wrapArroundCount = 0
}

// Based on https://datatracker.ietf.org/doc/html/rfc1889#appendix-A.2
func (sn *RTPExtendedSequenceNumber) UpdateSeq(seq uint16) error {
	maxSeq := sn.seqNum

	// TODO probation

	udelta := seq - maxSeq
	if udelta < uint16(maxDropout) {
		if seq < maxSeq {
			sn.wrapArroundCount++
		}
		sn.seqNum = seq
		return nil
	}

	badSeq := sn.badSeq
	if udelta <= maxSeqNum-maxMisorder {
		// sequence number made a very large jump
		if seq == badSeq {
			sn.InitSeq(seq)
			return nil
		}

		sn.badSeq = seq + 1
		return ErrRTPSequenceBad
	}

	// Duplicate
	return ErrRTPSequnceDuplicate
}

func (sn *RTPExtendedSequenceNumber) ReadExtendedSeq() uint64 {
	res := uint64(sn.seqNum) + (uint64(maxSeqNum)+1)*uint64(sn.wrapArroundCount)
	return res
}

func (s *RTPExtendedSequenceNumber) NextSeqNumber() uint16 {
	s.seqNum++
	if s.seqNum == 0 {
		s.wrapArroundCount++
	}

	return s.seqNum
}
