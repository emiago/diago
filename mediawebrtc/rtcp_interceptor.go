// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2026, Emir Aganovic

package mediawebrtc

import (
	"sync"
	"sync/atomic"
	"time"

	"github.com/emiago/diago/media"
	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
)

// RTPReadStats is shared with the core media package so RTCP callbacks can be
// consumed in the same way for WebRTC and UDP RTP sessions.
type RTPReadStats = media.RTPReadStats

// RTPWriteStats is shared with the core media package so RTCP callbacks can be
// consumed in the same way for WebRTC and UDP RTP sessions.
type RTPWriteStats = media.RTPWriteStats

type rtcpHooks struct {
	mu sync.Mutex

	onReadRTCP  func(pkt rtcp.Packet, rtpStats RTPReadStats)
	onWriteRTCP func(pkt rtcp.Packet, rtpStats RTPWriteStats)

	readStats  RTPReadStats
	writeStats RTPWriteStats
	readSeq    media.RTPExtendedSequenceNumber

	intervalFirstExtended uint64
	intervalStarted       bool
}

func newRTCPHooks() *rtcpHooks {
	return &rtcpHooks{
		onReadRTCP:  media.DefaultOnReadRTCP,
		onWriteRTCP: media.DefaultOnWriteRTCP,
	}
}

func (h *rtcpHooks) onRead(f func(pkt rtcp.Packet, rtpStats RTPReadStats)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.onReadRTCP = f
}

func (h *rtcpHooks) onWrite(f func(pkt rtcp.Packet, rtpStats RTPWriteStats)) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.onWriteRTCP = f
}

func (h *rtcpHooks) readRTP(header *rtp.Header, payloadSize int, sampleRate uint32) {
	h.mu.Lock()
	defer h.mu.Unlock()

	stats := &h.readStats
	if stats.PacketsCount == 0 || stats.SSRC != header.SSRC {
		*stats = RTPReadStats{
			SSRC:                   header.SSRC,
			FirstPktSequenceNumber: header.SequenceNumber,
			LastSequenceNumber:     header.SequenceNumber,
			SampleRate:             sampleRate,
		}
		h.readSeq.InitSeq(header.SequenceNumber)
		h.intervalFirstExtended = h.readSeq.ReadExtendedSeq()
		h.intervalStarted = true
	} else {
		_ = h.readSeq.UpdateSeq(header.SequenceNumber)
		if !h.intervalStarted {
			h.intervalFirstExtended = h.readSeq.ReadExtendedSeq()
			h.intervalStarted = true
		}
	}

	stats.IntervalFirstPktSeqNum = uint16(h.intervalFirstExtended)
	stats.IntervalPacketsCount++
	stats.PacketsCount++
	stats.OctetCount += uint64(payloadSize)
	stats.LastSequenceNumber = header.SequenceNumber
}

func (h *rtcpHooks) writeRTP(header *rtp.Header, payloadSize int) {
	h.mu.Lock()
	defer h.mu.Unlock()

	stats := &h.writeStats
	if stats.SSRC != header.SSRC {
		*stats = RTPWriteStats{SSRC: header.SSRC}
	}
	stats.PacketsCount++
	stats.OctetCount += uint64(payloadSize)
}

func (h *rtcpHooks) readRTCP(pkts []rtcp.Packet) {
	for _, pkt := range pkts {
		h.mu.Lock()
		callback := h.onReadRTCP
		stats := h.readStats
		h.mu.Unlock()
		if callback != nil {
			callback(pkt, stats)
		}

		h.mu.Lock()
		h.updateReadRTCPStats(pkt, time.Now())
		h.mu.Unlock()
	}
}

func (h *rtcpHooks) updateReadRTCPStats(pkt rtcp.Packet, now time.Time) {
	var reports []rtcp.ReceptionReport
	switch report := pkt.(type) {
	case *rtcp.SenderReport:
		if h.readStats.SSRC == 0 {
			h.readStats.SSRC = report.SSRC
		}
		reports = report.Reports
	case *rtcp.ReceiverReport:
		reports = report.Reports
	}
	for _, report := range reports {
		if report.SSRC != h.writeStats.SSRC || report.LastSenderReport == 0 {
			continue
		}
		now32 := uint32(media.NTPTimestamp(now) >> 16)
		rtt32 := now32 - report.LastSenderReport - report.Delay
		seconds := rtt32 >> 16
		fractions := float64(rtt32&0xFFFF) / 65536
		h.readStats.RTT = time.Duration(seconds)*time.Second + time.Duration(fractions*float64(time.Second))
	}
}

func (h *rtcpHooks) writeRTCP(pkts []rtcp.Packet) {
	h.mu.Lock()
	callback := h.onWriteRTCP
	stats := h.writeStats
	for _, pkt := range pkts {
		switch pkt.(type) {
		case *rtcp.SenderReport, *rtcp.ReceiverReport:
			h.readStats.IntervalFirstPktSeqNum = 0
			h.readStats.IntervalPacketsCount = 0
			h.intervalStarted = false
		}
	}
	h.mu.Unlock()

	if callback == nil {
		return
	}
	for _, pkt := range pkts {
		callback(pkt, stats)
	}
}

type rtcpInterceptorFactory struct {
	createMu sync.Mutex
	mu       sync.Mutex
	created  []*rtcpInterceptor
}

func (f *rtcpInterceptorFactory) NewInterceptor(string) (interceptor.Interceptor, error) {
	i := &rtcpInterceptor{}
	f.mu.Lock()
	f.created = append(f.created, i)
	f.mu.Unlock()
	return i, nil
}

func (f *rtcpInterceptorFactory) createdCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.created)
}

func (f *rtcpInterceptorFactory) takeCreated(index int) *rtcpInterceptor {
	f.mu.Lock()
	defer f.mu.Unlock()
	if index < 0 || index >= len(f.created) {
		return nil
	}
	i := f.created[index]
	f.created = append(f.created[:index], f.created[index+1:]...)
	return i
}

type rtcpInterceptor struct {
	interceptor.NoOp
	hooks atomic.Pointer[rtcpHooks]
}

func (i *rtcpInterceptor) BindLocalStream(info *interceptor.StreamInfo, writer interceptor.RTPWriter) interceptor.RTPWriter {
	return interceptor.RTPWriterFunc(func(header *rtp.Header, payload []byte, attributes interceptor.Attributes) (int, error) {
		n, err := writer.Write(header, payload, attributes)
		if err == nil {
			if hooks := i.hooks.Load(); hooks != nil {
				hooks.writeRTP(header, len(payload))
			}
		}
		return n, err
	})
}

func (i *rtcpInterceptor) BindRemoteStream(info *interceptor.StreamInfo, reader interceptor.RTPReader) interceptor.RTPReader {
	return interceptor.RTPReaderFunc(func(buf []byte, attributes interceptor.Attributes) (int, interceptor.Attributes, error) {
		n, attributes, err := reader.Read(buf, attributes)
		if err != nil {
			return n, attributes, err
		}
		if hooks := i.hooks.Load(); hooks != nil {
			if attributes == nil {
				attributes = make(interceptor.Attributes)
			}
			header, headerErr := attributes.GetRTPHeader(buf[:n])
			if headerErr == nil {
				hooks.readRTP(header, n-header.MarshalSize(), info.ClockRate)
			}
		}
		return n, attributes, nil
	})
}

func (i *rtcpInterceptor) BindRTCPReader(reader interceptor.RTCPReader) interceptor.RTCPReader {
	return interceptor.RTCPReaderFunc(func(buf []byte, attributes interceptor.Attributes) (int, interceptor.Attributes, error) {
		n, attributes, err := reader.Read(buf, attributes)
		if err != nil {
			return n, attributes, err
		}
		if hooks := i.hooks.Load(); hooks != nil {
			if attributes == nil {
				attributes = make(interceptor.Attributes)
			}
			pkts, unmarshalErr := attributes.GetRTCPPackets(buf[:n])
			if unmarshalErr == nil {
				hooks.readRTCP(pkts)
			}
		}
		return n, attributes, nil
	})
}

func (i *rtcpInterceptor) BindRTCPWriter(writer interceptor.RTCPWriter) interceptor.RTCPWriter {
	return interceptor.RTCPWriterFunc(func(pkts []rtcp.Packet, attributes interceptor.Attributes) (int, error) {
		if hooks := i.hooks.Load(); hooks != nil {
			hooks.writeRTCP(pkts)
		}
		return writer.Write(pkts, attributes)
	})
}
