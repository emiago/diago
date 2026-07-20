// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2026, Emir Aganovic

package mediaweb

import (
	"errors"
	"fmt"
	"io"
	"math"
	"math/rand"
	"net"
	"sync"
	"time"

	"github.com/emiago/diago/media"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
)

var (
	errRTPSessionClosed         = errors.New("rtp session is closed")
	errRTPSessionMonitorStarted = errors.New("rtp session monitor already started")
)

// RTPReadStats is shared with the core media package so existing statistics
// hooks can consume WebRTC and UDP RTP sessions uniformly.
type RTPReadStats = media.RTPReadStats

// RTPWriteStats is shared with the core media package so existing statistics
// hooks can consume WebRTC and UDP RTP sessions uniformly.
type RTPWriteStats = media.RTPWriteStats

type rtpReadStats struct {
	RTPReadStats
	lastSeq                   media.RTPExtendedSequenceNumber
	firstRTPTime              time.Time
	firstRTPTimestamp         uint32
	jitter                    float64
	transit                   int64
	lastSenderReportNTP       uint64
	lastSenderReportRecvTime  time.Time
	lastReceptionReportSeqNum uint32
	clockReset                bool
}

func (stats *rtpReadStats) calcJitter(now time.Time, timestamp uint32) {
	rtpSampleArrival := stats.firstRTPTimestamp + uint32(now.Sub(stats.firstRTPTime).Seconds()*float64(stats.SampleRate))
	transit := int64(rtpSampleArrival) - int64(timestamp)
	delta := transit - stats.transit
	stats.transit = transit
	if delta < 0 {
		delta = -delta
	}
	stats.jitter += (float64(delta) - stats.jitter) / 16
}

type rtpWriteStats struct {
	RTPWriteStats
	lastPacketTime      time.Time
	lastPacketTimestamp uint32
	sampleRate          uint32
}

// RTPSessionWebrtc adds RTP statistics and RTCP reporting to a negotiated
// MediaSessionWebrtc transport. It is separate from MediaSessionWebrtc so the
// ICE/DTLS/SRTP lifecycle does not own application-facing packet wrappers.
//
// Unlike the legacy RTPSession this type has no symmetric-RTP address learning,
// RTP source lock or separate RTCP destination. ICE authenticates the selected
// path, while WebRTC sends both RTP and RTCP over that one path (rtcp-mux).
type RTPSessionWebrtc struct {
	// Keep pointers at the top to reduce GC scanning work.
	Sess *MediaSessionWebrtc

	rtcpMU     sync.Mutex
	rtcpTicker *time.Ticker
	rtcpClosed chan struct{}
	monitorWG  sync.WaitGroup
	monitorRun bool
	closed     bool

	readStats  rtpReadStats
	writeStats rtpWriteStats

	intervalFirstExtended uint64
	intervalStarted       bool
	reportSSRC            uint32

	onReadRTCP  func(pkt rtcp.Packet, rtpStats RTPReadStats)
	onWriteRTCP func(pkt rtcp.Packet, rtpStats RTPWriteStats)

	// RTCPReportInterval controls sender/receiver report frequency. Set it
	// before Monitor or MonitorBackground. Zero uses five seconds.
	RTCPReportInterval time.Duration
}

func NewRTPSessionWebrtc(sess *MediaSessionWebrtc) *RTPSessionWebrtc {
	reportSSRC := rand.Uint32()
	if reportSSRC == 0 {
		reportSSRC = 1
	}
	return &RTPSessionWebrtc{
		Sess:               sess,
		rtcpTicker:         time.NewTicker(5 * time.Second),
		rtcpClosed:         make(chan struct{}),
		reportSSRC:         reportSSRC,
		onReadRTCP:         media.DefaultOnReadRTCP,
		onWriteRTCP:        media.DefaultOnWriteRTCP,
		RTCPReportInterval: 5 * time.Second,
	}
}

func (s *RTPSessionWebrtc) OnReadRTCP(f func(pkt rtcp.Packet, rtpStats RTPReadStats)) {
	s.rtcpMU.Lock()
	defer s.rtcpMU.Unlock()
	s.onReadRTCP = f
}

func (s *RTPSessionWebrtc) OnWriteRTCP(f func(pkt rtcp.Packet, rtpStats RTPWriteStats)) {
	s.rtcpMU.Lock()
	defer s.rtcpMU.Unlock()
	s.onWriteRTCP = f
}

func (s *RTPSessionWebrtc) ReadStats() RTPReadStats {
	s.rtcpMU.Lock()
	defer s.rtcpMU.Unlock()
	return s.readStats.RTPReadStats
}

func (s *RTPSessionWebrtc) WriteStats() RTPWriteStats {
	s.rtcpMU.Lock()
	defer s.rtcpMU.Unlock()
	return s.writeStats.RTPWriteStats
}

// ReadRTP decrypts through MediaSessionWebrtc and updates the receiver report
// counters. The generic RTPPacketReader can use RTPSessionWebrtc directly.
func (s *RTPSessionWebrtc) ReadRTP(buf []byte, pkt *rtp.Packet) (int, error) {
	for {
		n, err := s.Sess.ReadRTP(buf, pkt)
		if err != nil || n == 0 {
			return n, err
		}
		if pkt.Version != 2 || len(pkt.Payload) == 0 {
			continue
		}

		now := time.Now()
		s.rtcpMU.Lock()
		stats := &s.readStats
		if stats.SSRC != pkt.SSRC || stats.PacketsCount == 0 {
			codec, codecErr := webRTCRTPCodec(s.Sess.Codecs, pkt.PayloadType)
			if codecErr != nil {
				s.rtcpMU.Unlock()
				return 0, codecErr
			}
			lastSenderReportNTP := stats.lastSenderReportNTP
			lastSenderReportRecvTime := stats.lastSenderReportRecvTime
			*stats = rtpReadStats{
				RTPReadStats: RTPReadStats{
					SSRC:                   pkt.SSRC,
					FirstPktSequenceNumber: pkt.SequenceNumber,
					LastSequenceNumber:     pkt.SequenceNumber,
					SampleRate:             codec.SampleRate,
				},
				firstRTPTime:             now,
				firstRTPTimestamp:        pkt.Timestamp,
				lastSenderReportNTP:      lastSenderReportNTP,
				lastSenderReportRecvTime: lastSenderReportRecvTime,
			}
			stats.lastSeq.InitSeq(pkt.SequenceNumber)
			s.intervalFirstExtended = stats.lastSeq.ReadExtendedSeq()
			s.intervalStarted = true
		} else {
			_ = stats.lastSeq.UpdateSeq(pkt.SequenceNumber)
			if stats.clockReset || pkt.Marker {
				stats.firstRTPTime = now
				stats.firstRTPTimestamp = pkt.Timestamp
				stats.transit = 0
				stats.clockReset = false
			} else {
				stats.calcJitter(now, pkt.Timestamp)
			}
			if !s.intervalStarted {
				s.intervalFirstExtended = stats.lastSeq.ReadExtendedSeq()
				s.intervalStarted = true
			}
		}
		stats.IntervalFirstPktSeqNum = uint16(s.intervalFirstExtended)
		stats.IntervalPacketsCount++
		stats.PacketsCount++
		stats.OctetCount += uint64(len(pkt.Payload))
		stats.LastSequenceNumber = pkt.SequenceNumber
		s.rtcpMU.Unlock()
		return n, nil
	}
}

func (s *RTPSessionWebrtc) ReadRTPRaw(buf []byte) (int, error) {
	return s.Sess.ReadRTPRaw(buf)
}

// WriteRTP encrypts through MediaSessionWebrtc and updates sender-report
// counters only after the packet has been written successfully.
func (s *RTPSessionWebrtc) WriteRTP(pkt *rtp.Packet) error {
	s.Sess.mu.Lock()
	mode := s.Sess.Mode
	codecs := s.Sess.Codecs
	s.Sess.mu.Unlock()
	if mode == "recvonly" || mode == "inactive" {
		return s.Sess.WriteRTP(pkt)
	}
	codec, err := webRTCRTPCodec(codecs, pkt.PayloadType)
	if err != nil {
		return err
	}
	if err := s.Sess.WriteRTP(pkt); err != nil {
		return err
	}

	now := time.Now()
	s.rtcpMU.Lock()
	defer s.rtcpMU.Unlock()
	stats := &s.writeStats
	if stats.SSRC != pkt.SSRC {
		*stats = rtpWriteStats{
			RTPWriteStats: RTPWriteStats{SSRC: pkt.SSRC},
			sampleRate:    codec.SampleRate,
		}
	}
	stats.PacketsCount++
	stats.OctetCount += uint64(len(pkt.Payload))
	stats.lastPacketTime = now
	stats.lastPacketTimestamp = pkt.Timestamp
	return nil
}

func (s *RTPSessionWebrtc) ReadRTCP(buf []byte, pkts []rtcp.Packet) (int, error) {
	return s.Sess.ReadRTCP(buf, pkts)
}

func (s *RTPSessionWebrtc) WriteRTCP(pkt rtcp.Packet) error {
	return s.Sess.WriteRTCP(pkt)
}

func (s *RTPSessionWebrtc) startMonitor() error {
	if s.Sess == nil {
		return fmt.Errorf("WebRTC RTP session has no media session")
	}
	s.Sess.mu.Lock()
	ready := s.Sess.ready && s.Sess.mux != nil
	s.Sess.mu.Unlock()
	if !ready {
		return fmt.Errorf("WebRTC media must be finalized before starting RTCP monitoring")
	}

	s.rtcpMU.Lock()
	defer s.rtcpMU.Unlock()
	if s.monitorRun {
		return errRTPSessionMonitorStarted
	}
	if s.closed {
		return errRTPSessionClosed
	}
	interval := s.RTCPReportInterval
	if interval <= 0 {
		interval = 5 * time.Second
	}
	s.rtcpTicker.Reset(interval)
	s.monitorRun = true
	s.monitorWG.Add(2)
	return nil
}

// Monitor reads incoming SRTCP and periodically writes encrypted RTCP reports.
// It blocks until Close, a read failure or a write failure.
func (s *RTPSessionWebrtc) Monitor() error {
	if err := s.startMonitor(); err != nil {
		return err
	}
	errCh := make(chan error, 2)
	go func() {
		defer s.monitorWG.Done()
		errCh <- s.readRTCP()
	}()
	go func() {
		defer s.monitorWG.Done()
		errCh <- s.runRTCPWriter()
	}()
	firstErr := <-errCh
	_ = s.close(false)
	secondErr := <-errCh
	return errors.Join(webRTCPMonitorError(firstErr), webRTCPMonitorError(secondErr))
}

// MonitorBackground starts the RTCP reader and report writer in the background.
// Call MonitorClose before closing MediaSessionWebrtc when deterministic cleanup
// is required.
func (s *RTPSessionWebrtc) MonitorBackground() error {
	if err := s.startMonitor(); err != nil {
		return err
	}
	log := media.DefaultLogger()
	go func() {
		defer s.monitorWG.Done()
		if err := s.readRTCP(); webRTCPMonitorError(err) != nil {
			log.Error("WebRTC RTCP reader stopped", "error", err)
			_ = s.close(false)
		}
	}()
	go func() {
		defer s.monitorWG.Done()
		if err := s.runRTCPWriter(); webRTCPMonitorError(err) != nil {
			log.Error("WebRTC RTCP writer stopped", "error", err)
			_ = s.close(false)
		}
	}()
	return nil
}

func (s *RTPSessionWebrtc) close(wait bool) error {
	s.rtcpMU.Lock()
	wasClosed := s.closed
	s.closed = true
	if !wasClosed {
		close(s.rtcpClosed)
	}
	s.rtcpTicker.Stop()
	s.rtcpMU.Unlock()

	var deadlineErr error
	if s.Sess != nil {
		s.Sess.mu.Lock()
		if s.Sess.mux != nil {
			deadlineErr = s.Sess.mux.rtcp.SetReadDeadline(time.Now())
		}
		s.Sess.mu.Unlock()
	}
	if wait {
		s.monitorWG.Wait()
	}
	return deadlineErr
}

// MonitorClose stops RTCP monitoring and waits for both monitor goroutines.
// The ICE and media transports remain open.
func (s *RTPSessionWebrtc) MonitorClose() error { return s.close(true) }

// Close requests RTCP monitoring to stop without waiting for the goroutines.
func (s *RTPSessionWebrtc) Close() error { return s.close(false) }

func (s *RTPSessionWebrtc) readRTCP() error {
	s.Sess.mu.Lock()
	rtcpEndpoint := s.Sess.mux.rtcp
	s.Sess.mu.Unlock()
	if err := rtcpEndpoint.SetReadDeadline(time.Time{}); err != nil {
		return err
	}
	buf := make([]byte, media.RTPBufSize)
	pkts := make([]rtcp.Packet, 8)
	for {
		n, err := s.Sess.ReadRTCP(buf, pkts)
		if err != nil {
			if errors.Is(err, errRTCPFailedToUnmarshal) {
				media.DefaultLogger().Warn("Ignoring malformed WebRTC RTCP packet", "error", err)
				continue
			}
			return err
		}
		for _, pkt := range pkts[:n] {
			s.readRTCPPacket(pkt, time.Now())
		}
	}
}

func (s *RTPSessionWebrtc) readRTCPPacket(pkt rtcp.Packet, now time.Time) {
	s.rtcpMU.Lock()
	callback := s.onReadRTCP
	callbackStats := s.readStats.RTPReadStats
	switch p := pkt.(type) {
	case *rtcp.SenderReport:
		if s.readStats.SSRC == 0 {
			s.readStats.SSRC = p.SSRC
		}
		s.readStats.lastSenderReportNTP = p.NTPTime
		s.readStats.lastSenderReportRecvTime = now
		for _, report := range p.Reports {
			s.readReceptionReport(report, now)
		}
	case *rtcp.ReceiverReport:
		for _, report := range p.Reports {
			s.readReceptionReport(report, now)
		}
	}
	s.rtcpMU.Unlock()
	if callback != nil {
		callback(pkt, callbackStats)
	}
}

func (s *RTPSessionWebrtc) readReceptionReport(report rtcp.ReceptionReport, now time.Time) {
	if report.SSRC != s.writeStats.SSRC {
		return
	}
	if report.LastSenderReport != 0 {
		s.readStats.RTT, _ = calcRTT(now, report.LastSenderReport, report.Delay)
	}
	s.readStats.lastReceptionReportSeqNum = report.LastSequenceNumber
}

func (s *RTPSessionWebrtc) runRTCPWriter() error {
	for {
		select {
		case now := <-s.rtcpTicker.C:
			if err := s.writeReport(now); err != nil {
				return err
			}
		case <-s.rtcpClosed:
			return nil
		}
	}
}

func (s *RTPSessionWebrtc) writeReport(now time.Time) error {
	s.rtcpMU.Lock()
	var pkt rtcp.Packet
	switch {
	case s.writeStats.SSRC != 0:
		pkt = s.senderReport(now)
	case s.readStats.PacketsCount != 0:
		pkt = &rtcp.ReceiverReport{
			SSRC:    s.reportSSRC,
			Reports: []rtcp.ReceptionReport{s.receptionReport(now)},
		}
	default:
		s.rtcpMU.Unlock()
		return nil
	}
	callback := s.onWriteRTCP
	callbackStats := s.writeStats.RTPWriteStats
	s.readStats.IntervalFirstPktSeqNum = 0
	s.readStats.IntervalPacketsCount = 0
	s.intervalStarted = false
	s.rtcpMU.Unlock()

	if callback != nil {
		callback(pkt, callbackStats)
	}
	return s.Sess.WriteRTCP(pkt)
}

func (s *RTPSessionWebrtc) senderReport(now time.Time) *rtcp.SenderReport {
	stats := &s.writeStats
	timestampOffset := now.Sub(stats.lastPacketTime).Seconds() * float64(stats.sampleRate)
	report := &rtcp.SenderReport{
		SSRC:        stats.SSRC,
		NTPTime:     media.NTPTimestamp(now),
		RTPTime:     stats.lastPacketTimestamp + uint32(max(timestampOffset, 0)),
		PacketCount: uint32(min(stats.PacketsCount, uint64(math.MaxUint32))),
		OctetCount:  uint32(min(stats.OctetCount, uint64(math.MaxUint32))),
	}
	if s.readStats.PacketsCount != 0 {
		report.Reports = []rtcp.ReceptionReport{s.receptionReport(now)}
	}
	return report
}

func (s *RTPSessionWebrtc) receptionReport(now time.Time) rtcp.ReceptionReport {
	stats := &s.readStats
	lastExtended := stats.lastSeq.ReadExtendedSeq()
	var expected, intervalExpected uint64
	if stats.PacketsCount > 0 {
		expected = lastExtended - uint64(stats.FirstPktSequenceNumber) + 1
	}
	if s.intervalStarted {
		intervalExpected = lastExtended - s.intervalFirstExtended + 1
	}
	intervalReceived := uint64(stats.IntervalPacketsCount)
	intervalLost := uint64(0)
	if intervalExpected > intervalReceived {
		intervalLost = intervalExpected - intervalReceived
	}
	fractionLost := uint8(0)
	if intervalExpected > 0 {
		fractionLost = uint8(min(intervalLost*256/intervalExpected, uint64(math.MaxUint8)))
	}
	totalLost := uint64(0)
	if expected > stats.PacketsCount {
		totalLost = expected - stats.PacketsCount
	}
	delay := uint64(0)
	if !stats.lastSenderReportRecvTime.IsZero() {
		delay = uint64(max(now.Sub(stats.lastSenderReportRecvTime).Seconds(), 0) * 65536)
	}
	return rtcp.ReceptionReport{
		SSRC:               stats.SSRC,
		FractionLost:       fractionLost,
		TotalLost:          uint32(min(totalLost, uint64(0xFFFFFF))),
		LastSequenceNumber: uint32(lastExtended),
		Jitter:             uint32(max(stats.jitter, 0)),
		LastSenderReport:   uint32(stats.lastSenderReportNTP >> 16),
		Delay:              uint32(min(delay, uint64(math.MaxUint32))),
	}
}

func webRTCRTPCodec(codecs []media.Codec, payloadType uint8) (media.Codec, error) {
	for _, codec := range codecs {
		if codec.PayloadType == payloadType {
			return codec, nil
		}
	}
	return media.Codec{}, fmt.Errorf("unknown WebRTC RTP payload type %d", payloadType)
}

func calcRTT(now time.Time, lastSenderReport, delaySenderReport uint32) (time.Duration, bool) {
	now32 := uint32(media.NTPTimestamp(now) >> 16)
	rtt32 := now32 - lastSenderReport - delaySenderReport
	skewed := now32-delaySenderReport < lastSenderReport
	seconds := rtt32 >> 16
	fractions := float64(rtt32&0xFFFF) / 65536
	return time.Duration(seconds)*time.Second + time.Duration(fractions*float64(time.Second)), skewed
}

func webRTCPMonitorError(err error) error {
	if err == nil || errors.Is(err, io.EOF) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return nil
	}
	return err
}
