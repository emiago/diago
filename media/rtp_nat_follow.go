// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package media

import (
	"net"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
)

// Consecutive in-sequence packets a new source must send before RTP moves to it (Asterisk strictrtp uses 4 too).
const followProbationPackets = 4

// Sequence numbers a NAT move may lose while the stream still counts as the same one.
const followMaxSeqGap = 64

// rtpFollow is RTPNATFollow's read-side state, touched only by the goroutine reading RTP.
type rtpFollow struct {
	streamKnown   bool
	streamSSRC    uint32
	streamLastSeq uint16

	candidate        *net.UDPAddr
	candidateSSRC    uint32
	candidateLastSeq uint16
	candidatePackets int
}

// latches reports whether RTP and RTCP go to the learned source instead of the SDP address.
func (m *MediaSession) latches() bool {
	return m.RTPNAT == RTPNATSymetric || m.RTPNAT == RTPNATFollow
}

func (m *MediaSession) rtpDestination() *net.UDPAddr {
	if learned := m.learnedRTPFrom.Load(); learned != nil {
		return learned
	}
	return &m.Raddr
}

func (m *MediaSession) rtcpDestination() *net.UDPAddr {
	if learned := m.learnedRTCPFrom.Load(); learned != nil {
		return learned
	}
	return &m.rtcpRaddr
}

func (m *MediaSession) followRTP(from net.Addr, header rtp.Header) {
	source, ok := from.(*net.UDPAddr)
	if !ok || header.Version != 2 {
		return
	}

	f := &m.follow
	continuesStream := f.continuesStream(header.SSRC, header.SequenceNumber)
	if continuesStream {
		f.streamLastSeq = header.SequenceNumber
	}

	if udpAddrEqual(*source, *m.rtpDestination()) {
		m.endProbation()
		m.setFollowedStream(header.SSRC, header.SequenceNumber)
		return
	}

	if f.extendsProbation(source, header.SSRC, header.SequenceNumber) {
		f.candidateLastSeq = header.SequenceNumber
		f.candidatePackets++
	} else {
		m.startProbation(source, header.SSRC, header.SequenceNumber, continuesStream)
	}

	if f.candidatePackets >= followProbationPackets {
		m.moveRTPTo(source, header.SSRC, header.SequenceNumber)
	}
}

// continuesStream reports whether the packet is the next part of the stream seen so far; any packet is before the first.
func (f *rtpFollow) continuesStream(ssrc uint32, seq uint16) bool {
	if !f.streamKnown {
		return true
	}

	ahead := seq - f.streamLastSeq
	return ssrc == f.streamSSRC && ahead > 0 && ahead <= followMaxSeqGap
}

func (f *rtpFollow) extendsProbation(source *net.UDPAddr, ssrc uint32, seq uint16) bool {
	return f.candidate != nil &&
		udpAddrEqual(*source, *f.candidate) &&
		ssrc == f.candidateSSRC &&
		seq == f.candidateLastSeq+1
}

func (m *MediaSession) startProbation(source *net.UDPAddr, ssrc uint32, seq uint16, continuesStream bool) {
	f := &m.follow
	f.candidate, f.candidateSSRC, f.candidateLastSeq, f.candidatePackets = source, ssrc, seq, 1

	// Only a candidate carrying our stream gets a copy of our RTP, so a stranger gets nothing.
	if continuesStream {
		m.probationRTPTo.Store(source)
	} else {
		m.probationRTPTo.Store(nil)
	}
}

func (m *MediaSession) endProbation() {
	if m.follow.candidate == nil {
		return
	}

	m.follow.candidate = nil
	m.probationRTPTo.Store(nil)
}

func (m *MediaSession) moveRTPTo(source *net.UDPAddr, ssrc uint32, seq uint16) {
	m.learnedRTPFrom.Store(source)
	m.endProbation()
	m.setFollowedStream(ssrc, seq)

	DefaultLogger().Info("RTP NAT following new source", "addr", source.String(), "ssrc", ssrc)
}

func (m *MediaSession) setFollowedStream(ssrc uint32, seq uint16) {
	f := &m.follow
	f.streamKnown, f.streamSSRC, f.streamLastSeq = true, ssrc, seq

	m.followedSSRC.Store(uint64(1)<<32 | uint64(ssrc))
}

// followedStreamSSRC is the followed stream's SSRC, for the RTCP reader.
func (m *MediaSession) followedStreamSSRC() (uint32, bool) {
	packed := m.followedSSRC.Load()
	return uint32(packed), packed != 0
}

// followRTCP moves RTCP to a new source as soon as it reports for our stream; RTCP is too sparse for a probation.
func (m *MediaSession) followRTCP(from net.Addr, pkts []rtcp.Packet) {
	source, ok := from.(*net.UDPAddr)
	if !ok || len(pkts) == 0 {
		return
	}

	streamSSRC, known := m.followedStreamSSRC()
	senderSSRC, isReport := rtcpSenderSSRC(pkts[0])
	if !known || !isReport || senderSSRC != streamSSRC {
		return
	}

	if udpAddrEqual(*source, *m.rtcpDestination()) {
		return
	}

	m.learnedRTCPFrom.Store(source)
	DefaultLogger().Info("RTCP NAT following new source", "addr", source.String(), "ssrc", senderSSRC)
}

// rtcpSenderSSRC reads the sender from the report a compound RTCP packet starts with (RFC 3550 6.1).
func rtcpSenderSSRC(p rtcp.Packet) (uint32, bool) {
	switch report := p.(type) {
	case *rtcp.SenderReport:
		return report.SSRC, true
	case *rtcp.ReceiverReport:
		return report.SSRC, true
	}
	return 0, false
}
