// SPDX-License-Identifier: MPL-2.0
// Copyright (C) 2024 Emir Aganovic

package media

import (
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/emiago/diago/media/sdp"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/rs/zerolog"
)

// RTP session is RTP ReadWriter with control (RTCP) reporting
// Session is identified by network address and port pair to which data should be sent and received
// Participant can be part of multiple rtp sessions. One for audio, one for media.
// So for different MediaSession (audio,video etc) different RTP session needs to be created
// TODO
// RTPSession can be different type:
// - unicast or multicase
// - replicated unicast (mixers audio)
// RTP session is attached to RTPReader and RTPWriter
// Now:
// - Designed for UNICAST sessions
// - Acts as RTP Reader Writer
// - Only makes sense for Reporting Quality mediaw
// Roadmap:
// - Can handle multiple SSRC from Reader
// - Multiple RTCP Recever Blocks
type RTPSession struct {
	// Keep pointers at top to reduce GC
	Sess *MediaSession

	rtcpMU sync.Mutex
	// All below fields should not be updated without rtcpMu Lock
	rtcpTicker *time.Ticker
	readStats  RTPReadStats
	writeStats RTPWriteStats

	// Experimental
	// this intercepts reading or writing rtcp packet. Allows manipulation
	OnReadRTCP  func(pkt rtcp.Packet, rtpStats RTPReadStats)
	OnWriteRTCP func(pkt rtcp.Packet, rtpStats RTPWriteStats)

	log zerolog.Logger
}

// Some of fields here are exported (as readonly) intentionally
type RTPReadStats struct {
	SSRC                   uint32
	FirstPktSequenceNumber uint16
	LastSequenceNumber     uint16
	lastSeq                RTPExtendedSequenceNumber
	// tracks first pkt seq in this interval to calculate loss of packets
	IntervalFirstPktSeqNum uint16
	IntervalTotalPackets   uint16

	TotalPackets uint64

	// RTP reading stats
	sampleRate       uint32
	lastRTPTime      time.Time
	lastRTPTimestamp uint32
	jitter           float64

	// Reading RTCP stats
	lastSenderReportNTP       uint64
	lastSenderReportRecvTime  time.Time
	lastReceptionReportSeqNum uint32

	// Round TRIP Time based on LSR and DLSR
	RTT time.Duration
}

// Some of fields here are exported (as readonly) intentionally
type RTPWriteStats struct {
	SSRC uint32

	lastPacketTime      time.Time
	lastPacketTimestamp uint32
	sampleRate          uint32

	// RTCP stats
	PacketsCount uint32
	OctetCount   uint32
}

// RTP session creates new RTP reader/writer from session
func NewRTPSession(sess *MediaSession) *RTPSession {
	return &RTPSession{
		Sess:       sess,
		rtcpTicker: time.NewTicker(5 * time.Second),
	}
}

func (s *RTPSession) Close() error {
	s.rtcpTicker.Stop()
	// Stop monitor routing
	err := s.Sess.rtcpConn.SetDeadline(time.Now())
	// defer s.Sess.rtcpConn.SetDeadline(time.Time{})

	return err
}

func (s *RTPSession) ReadRTP(b []byte, readPkt *rtp.Packet) error {
	for {
		if err := s.Sess.ReadRTP(b, readPkt); err != nil {
			return err
		}

		// Validate pkt. Check is it keep alive
		if readPkt.Version == 0 {
			continue
		}
		if len(readPkt.Payload) == 0 {
			continue
		}

		break
	}

	s.rtcpMU.Lock()
	// pktArrival := time.Now()
	stats := &s.readStats
	now := time.Now()

	// For now we only track latest SSRC
	if stats.SSRC != readPkt.SSRC {
		// For now we will reset all our stats.
		// We expect that SSRC only changed but MULTI RTP stream per one session are not fully supported!
		codec := CodecFromPayloadType(readPkt.PayloadType)

		*stats = RTPReadStats{
			SSRC:                   readPkt.SSRC,
			FirstPktSequenceNumber: readPkt.SequenceNumber,

			sampleRate: codec.SampleRate,
		}
		stats.lastSeq.InitSeq(readPkt.SequenceNumber)
	} else {
		stats.lastSeq.UpdateSeq(readPkt.SequenceNumber)
		sampleRate := s.readStats.sampleRate

		// Jitter here will mostly be incorrect as Reading RTP can be faster slower
		// and not actually dictated by sampling (clock)
		// https://datatracker.ietf.org/doc/html/rfc3550#page-39
		Sij := readPkt.Timestamp - stats.lastRTPTimestamp
		Rij := now.Sub(stats.lastRTPTime)
		D := Rij.Seconds()*float64(sampleRate) - float64(Sij)
		if D < 0 {
			D = -D
		}
		stats.jitter = stats.jitter + (D-stats.jitter)/16
	}
	stats.IntervalTotalPackets++
	stats.TotalPackets++
	stats.LastSequenceNumber = readPkt.SequenceNumber
	// stats.clockRTPTimestamp+=

	if stats.IntervalFirstPktSeqNum == 0 {
		stats.IntervalFirstPktSeqNum = readPkt.SequenceNumber
	}

	stats.lastRTPTime = now
	stats.lastRTPTimestamp = readPkt.Timestamp

	s.rtcpMU.Unlock()

	// select {
	// case t := <-s.rtcpTicker.C:
	// 	s.writeRTCP(t)
	// 	// stats.IntervalFirstPktSeqNum = 0
	// 	// stats.IntervalTotalPackets = 0
	// default:
	// }

	return nil
}

func (s *RTPSession) ReadRTPRaw(buf []byte) (int, error) {
	// In this case just proxy RTP. RTP Session can not work without full RTP decoded
	// It is expected that RTCP is also proxied
	return s.Sess.ReadRTPRaw(buf)
}

func (s *RTPSession) WriteRTP(pkt *rtp.Packet) error {
	// Do not write if we are creating RTCP packet

	if err := s.Sess.WriteRTP(pkt); err != nil {
		return err
	}

	s.rtcpMU.Lock()
	writeStats := &s.writeStats
	// For now we only track latest SSRC
	if writeStats.SSRC != pkt.SSRC {
		codec := CodecFromPayloadType(pkt.PayloadType)

		*writeStats = RTPWriteStats{
			SSRC:       pkt.SSRC,
			sampleRate: codec.SampleRate,
		}
	}

	writeStats.PacketsCount++
	writeStats.OctetCount += uint32(len(pkt.Payload))
	writeStats.lastPacketTime = time.Now()
	writeStats.lastPacketTimestamp = pkt.Timestamp

	s.rtcpMU.Unlock()

	// select {
	// case t := <-s.rtcpTicker.C:
	// 	s.writeRTCP(t)
	// default:
	// }
	return nil
}

func (s *RTPSession) WriteRTPRaw(buf []byte) (int, error) {
	// In this case just proxy RTP. RTP Session can not work without full RTP decoded
	// It is expected that RTCP is also proxied
	return s.Sess.WriteRTCPRaw(buf)
}

// Monitor starts reading RTCP and monitoring media quality
func (s *RTPSession) Monitor() error {
	errchan := make(chan error)
	go func() {
		errchan <- s.readRTCP()
	}()

	var err error
	for {
		now := <-s.rtcpTicker.C
		if err = s.writeRTCP(now); err != nil {
			break
		}
	}
	return errors.Join(err, <-errchan)
}

// MonitorBackground is helper to keep monitoring in background
func (s *RTPSession) MonitorBackground() {
	go func() {
		sess := s.Sess
		sess.log.Debug().Msg("RTCP reader started")
		if err := s.readRTCP(); err != nil {
			if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
				sess.log.Debug().Msg("RTP session RTCP reader exit")
				return
			}

			if e, ok := err.(net.Error); ok && e.Timeout() {
				// Must be  closed
				sess.log.Debug().Msg("RTP session RTCP closed with timeout")
				return
			}
			sess.log.Error().Err(err).Msg("RTP session RTCP reader stopped with error")
		}
	}()

	go func() {
		sess := s.Sess
		sess.log.Debug().Msg("RTCP writer started")
		for {
			now, open := <-s.rtcpTicker.C
			if !open {
				sess.log.Debug().Msg("RTCP writer closed")
				return
			}
			if err := s.writeRTCP(now); err != nil {
				if errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
					sess.log.Debug().Msg("RTP session RTCP writer exit")
					return
				}
				sess.log.Error().Err(err).Msg("RTP session RTCP writer stopped with error")
				return
			}
		}
	}()
}

func (s *RTPSession) readRTCP() error {
	sess := s.Sess
	// TODO use sync pool here
	buf := make([]byte, 1600)
	rtcpBuf := make([]rtcp.Packet, 5)          // What would be more correct value?
	sess.rtcpConn.SetReadDeadline(time.Time{}) // For now make sure we are not getting timeout
	for {
		n, err := sess.ReadRTCP(buf, rtcpBuf)
		if err != nil {
			return err
		}

		for _, pkt := range rtcpBuf[:n] {
			s.readRTCPPacket(pkt)
		}
	}
}

func (s *RTPSession) readRTCPPacket(pkt rtcp.Packet) {
	s.rtcpMU.Lock()
	now := time.Now()

	// Add interceptor
	if s.OnReadRTCP != nil {
		stats := s.readStats
		s.rtcpMU.Unlock()
		s.OnReadRTCP(pkt, stats)
		s.rtcpMU.Lock()
	}

	switch p := pkt.(type) {
	case *rtcp.SenderReport:
		// It can happen that we have nothing received from RTP till now
		if s.readStats.SSRC == 0 {
			s.readStats.SSRC = p.SSRC
		}

		s.readStats.lastSenderReportNTP = p.NTPTime
		s.readStats.lastSenderReportRecvTime = now

		for _, rr := range p.Reports {
			s.readReceptionReport(rr, now)
		}

	case *rtcp.ReceiverReport:
		for _, rr := range p.Reports {
			s.readReceptionReport(rr, now)
		}
	}
	s.rtcpMU.Unlock()

}

func (s *RTPSession) readReceptionReport(rr rtcp.ReceptionReport, now time.Time) {
	// For now only use single SSRC
	if rr.SSRC != s.writeStats.SSRC {
		s.Sess.log.Warn().Uint32("ssrc", rr.SSRC).Uint32("expected", s.readStats.SSRC).Msg("Reception report SSRC does not match our internal")
		return
	}

	// compute jitter
	// https://tools.ietf.org/html/rfc3550#page-39
	// Round trip time
	if rr.LastSenderReport != 0 {
		var skewed bool
		s.readStats.RTT, skewed = calcRTT(now, rr.LastSenderReport, rr.Delay)
		if skewed {
			s.Sess.log.Warn().Uint32("ssrc", rr.SSRC).Str("rtt", s.readStats.RTT.String()).Msg("Internal RTCP clock skew detected")
		}
	}
	// used to calc fraction lost
	s.readStats.lastReceptionReportSeqNum = rr.LastSequenceNumber
}

func (s *RTPSession) writeRTCP(now time.Time) error {

	var pkt rtcp.Packet

	// If there is no writer in session (a=recvonly) then generate only receiver report
	// otherwise always go with sender report with reception reports
	s.rtcpMU.Lock()
	if s.Sess.Mode == sdp.ModeRecvonly {
		if s.readStats.SSRC == 0 {
			s.rtcpMU.Unlock()
			return nil
		}

		rr := rtcp.ReceiverReport{}
		s.parseReceiverReport(&rr, now, s.readStats.SSRC)

		pkt = &rr
	} else {
		if s.writeStats.SSRC == 0 {
			s.rtcpMU.Unlock()
			return nil
		}

		sr := rtcp.SenderReport{}
		s.parseSenderReport(&sr, now, s.writeStats.SSRC)

		pkt = &sr
	}

	// Reset any current reading interval stats
	s.readStats.IntervalFirstPktSeqNum = 0
	s.readStats.IntervalTotalPackets = 0

	// Add interceptor
	if s.OnWriteRTCP != nil {
		stats := s.writeStats
		s.rtcpMU.Unlock()
		s.OnWriteRTCP(pkt, stats)
	} else {
		s.rtcpMU.Unlock()
	}

	return s.Sess.WriteRTCP(pkt)
}

func (s *RTPSession) parseReceiverReport(receiverReport *rtcp.ReceiverReport, now time.Time, ssrc uint32) {
	receptionReport := rtcp.ReceptionReport{}

	s.parseReceptionReport(&receptionReport, now)
	*receiverReport = rtcp.ReceiverReport{
		SSRC:    ssrc,
		Reports: []rtcp.ReceptionReport{receptionReport},
	}
}

func (s *RTPSession) parseSenderReport(senderReport *rtcp.SenderReport, now time.Time, ssrc uint32) {

	// Write stats
	writeStats := &s.writeStats
	rtpTimestampOffset := now.Sub(writeStats.lastPacketTime).Seconds() * float64(s.writeStats.sampleRate)
	// Same as asterisk
	// Sender Report should contain Receiver Report if user acts as sender and receiver
	// Otherwise on Read we should generate only receiver Report
	*senderReport = rtcp.SenderReport{
		SSRC:        ssrc,
		NTPTime:     NTPTimestamp(now),
		RTPTime:     writeStats.lastPacketTimestamp + uint32(rtpTimestampOffset),
		PacketCount: writeStats.PacketsCount,
		OctetCount:  writeStats.OctetCount,
	}

	if s.readStats.SSRC > 0 {
		receptionReport := rtcp.ReceptionReport{}
		s.parseReceptionReport(&receptionReport, now)
		senderReport.Reports = []rtcp.ReceptionReport{receptionReport}
	}
}

func (s *RTPSession) parseReceptionReport(receptionReport *rtcp.ReceptionReport, now time.Time) {
	var sequenceCycles uint16 = 0 // TODO have sequence cycles handled

	// Read stats
	readStats := &s.readStats
	lastExtendedSeq := &readStats.lastSeq

	// fraction loss is caluclated as packets loss / number expected in interval as fixed point number with point number at the left edge
	receivedLastSeq := int64(lastExtendedSeq.ReadExtendedSeq())
	readIntervalExpectedPkts := receivedLastSeq - int64(readStats.IntervalFirstPktSeqNum)
	readIntervalLost := max(readIntervalExpectedPkts-int64(readStats.IntervalTotalPackets), 0)
	fractionLost := float64(readIntervalLost) / float64(readIntervalExpectedPkts) // Can be negative

	// Watch OUT FOR -1
	expectedPkts := uint64(receivedLastSeq) - uint64(readStats.FirstPktSequenceNumber)

	// interarrival jitter
	// network transit time
	// Assert

	lastReceivedSenderReportTime := readStats.lastSenderReportRecvTime
	var delay time.Duration
	if !lastReceivedSenderReportTime.IsZero() {
		delay = now.Sub(lastReceivedSenderReportTime)
	}

	// TODO handle multiple SSRC
	*receptionReport = rtcp.ReceptionReport{
		SSRC:               readStats.SSRC,
		FractionLost:       uint8(max(fractionLost*256, 0)),                         // Can be negative. Saturate to zero
		TotalLost:          uint32(min(expectedPkts-readStats.TotalPackets, 1<<32)), // Saturate to largest 32 bit value
		LastSequenceNumber: uint32(sequenceCycles)<<16 + uint32(readStats.LastSequenceNumber),
		Jitter:             uint32(readStats.jitter),                    // TODO
		LastSenderReport:   uint32(readStats.lastSenderReportNTP >> 16), // LSR
		Delay:              uint32(delay.Seconds() * 65356),             // DLSR
	}
}

func FractionLostFloat(f uint8) float64 {
	return float64(f) / 256
}

func calcRTT(now time.Time, lastSenderReport uint32, delaySenderReport uint32) (rtt time.Duration, skewed bool) {
	nowNTP := NTPTimestamp(now)
	now32 := uint32(nowNTP >> 16)

	rtt32 := now32 - lastSenderReport - delaySenderReport
	skewed = now32-delaySenderReport < lastSenderReport

	secs := rtt32 & 0xFFFF0000 >> 16           // higher 16 bits
	fracs := float64(rtt32&0x0000FFFF) / 65356 // lower 16 bits
	rtt = time.Duration(secs)*time.Second + time.Duration(fracs*float64(time.Second))

	return
}
