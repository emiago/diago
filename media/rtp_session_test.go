// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package media

import (
	"bytes"
	"fmt"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/emiago/diago/media/sdp"
	"github.com/emiago/sipgo/fakes"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func fakeSession(lport int, rport int, rtpReader io.Reader, rtpWriter io.Writer, rtcpReader io.Reader, rtcpWriter io.Writer) *RTPSession {
	sess := &MediaSession{
		Codecs:    []Codec{CodecAudioAlaw, CodecAudioUlaw},
		Laddr:     net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: lport},
		Raddr:     net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: rport},
		rtcpRaddr: net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: rport + 1},
	}

	rtpConn := &fakes.UDPConn{
		Reader: rtpReader,
		// Reader: bytes.NewBuffer([]byte{}),
		Writers: map[string]io.Writer{
			sess.Raddr.String(): rtpWriter,
		},
	}
	sess.rtpConn = rtpConn

	rtcpConn := &fakes.UDPConn{
		Reader: rtcpReader,
		Writers: map[string]io.Writer{
			sess.rtcpRaddr.String(): rtcpWriter,
		},
	}
	sess.rtcpConn = rtcpConn

	rtpSess := NewRTPSession(sess)

	return rtpSess
}

func pipeRTP(lport int, rport int) (read *RTPSession, write *RTPSession) {
	read1, write1 := io.Pipe()
	readControl2, writeControl2 := io.Pipe()
	rtpSessRead := fakeSession(lport, rport, read1, nil, readControl2, nil)
	rtpSessWrite := fakeSession(rport, lport, nil, write1, nil, writeControl2)
	return rtpSessRead, rtpSessWrite
}

func TestRTPSessionReadSkipsComfortNoise(t *testing.T) {
	rtpSessRead, rtpSessWrite := pipeRTP(9876, 1234)

	const ssrc uint32 = 1234
	packets := []rtp.Packet{
		{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    CodecComfortNoise8000.PayloadType,
				SequenceNumber: 1,
				Timestamp:      160,
				SSRC:           ssrc,
			},
			Payload: []byte{1},
		},
		{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    CodecAudioAlaw.PayloadType,
				SequenceNumber: 2,
				Timestamp:      320,
				SSRC:           ssrc,
			},
			Payload: []byte{2},
		},
		{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    CodecComfortNoise8000.PayloadType,
				SequenceNumber: 3,
				Timestamp:      480,
				SSRC:           ssrc,
			},
			Payload: []byte{3},
		},
		{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    CodecAudioAlaw.PayloadType,
				SequenceNumber: 4,
				Timestamp:      640,
				SSRC:           ssrc,
			},
			Payload: []byte{4},
		},
	}

	writeDone := make(chan error, 1)
	go func() {
		for i := range packets {
			if err := rtpSessWrite.Sess.WriteRTP(&packets[i]); err != nil {
				writeDone <- err
				return
			}
		}
		writeDone <- nil
	}()

	buf := make([]byte, RTPBufSize)
	for _, wantSequenceNumber := range []uint16{2, 4} {
		var pkt rtp.Packet
		n, err := rtpSessRead.ReadRTP(buf, &pkt)
		require.NoError(t, err)
		require.Positive(t, n)
		assert.Equal(t, CodecAudioAlaw.PayloadType, pkt.PayloadType)
		assert.Equal(t, wantSequenceNumber, pkt.SequenceNumber)
	}
	require.NoError(t, <-writeDone)

	stats := rtpSessRead.ReadStats()
	assert.EqualValues(t, 2, stats.PacketsCount)
	assert.EqualValues(t, 2, stats.OctetCount)
}

func TestRTPSessionReadUnsupportedPayload(t *testing.T) {
	rtpSessRead, rtpSessWrite := pipeRTP(9876, 1234)
	rtpSessRead.readUnsupported = true

	const ssrc uint32 = 1234
	packets := []rtp.Packet{
		{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    CodecComfortNoise8000.PayloadType,
				SequenceNumber: 1,
				Timestamp:      160,
				SSRC:           ssrc,
			},
			Payload: []byte{1},
		},
		{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    CodecAudioAlaw.PayloadType,
				SequenceNumber: 2,
				Timestamp:      320,
				SSRC:           ssrc,
			},
			Payload: []byte{2},
		},
	}

	writeDone := make(chan error, 1)
	go func() {
		for i := range packets {
			if err := rtpSessWrite.Sess.WriteRTP(&packets[i]); err != nil {
				writeDone <- err
				return
			}
		}
		writeDone <- nil
	}()

	buf := make([]byte, RTPBufSize)
	var pkt rtp.Packet
	n, err := rtpSessRead.ReadRTP(buf, &pkt)
	require.NoError(t, err)
	assert.Zero(t, n)
	assert.Equal(t, CodecComfortNoise8000.PayloadType, pkt.PayloadType)
	assert.Equal(t, []byte{1}, pkt.Payload)

	n, err = rtpSessRead.ReadRTP(buf, &pkt)
	require.NoError(t, err)
	require.Positive(t, n)
	assert.Equal(t, CodecAudioAlaw.PayloadType, pkt.PayloadType)
	require.NoError(t, <-writeDone)

	stats := rtpSessRead.ReadStats()
	assert.EqualValues(t, 1, stats.PacketsCount)
	assert.EqualValues(t, 1, stats.OctetCount)

	fork := rtpSessRead.Fork(rtpSessRead.Sess)
	assert.True(t, fork.readUnsupported)
}

func TestRTPSessionReadPassesSupportedPayloadTypeChanges(t *testing.T) {
	rtpSessRead, rtpSessWrite := pipeRTP(9876, 1234)

	const ssrc uint32 = 1234
	packets := []rtp.Packet{
		{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    CodecAudioAlaw.PayloadType,
				SequenceNumber: 1,
				Timestamp:      160,
				SSRC:           ssrc,
			},
			Payload: []byte{1},
		},
		{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    CodecAudioUlaw.PayloadType,
				SequenceNumber: 2,
				Timestamp:      320,
				SSRC:           ssrc,
			},
			Payload: []byte{2},
		},
		{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    CodecAudioAlaw.PayloadType,
				SequenceNumber: 3,
				Timestamp:      480,
				SSRC:           ssrc,
			},
			Payload: []byte{3},
		},
	}

	writeDone := make(chan error, 1)
	go func() {
		for i := range packets {
			if err := rtpSessWrite.Sess.WriteRTP(&packets[i]); err != nil {
				writeDone <- err
				return
			}
		}
		writeDone <- nil
	}()

	buf := make([]byte, RTPBufSize)
	for i := range packets {
		var pkt rtp.Packet
		n, err := rtpSessRead.ReadRTP(buf, &pkt)
		require.NoError(t, err)
		require.Positive(t, n)
		assert.Equal(t, packets[i].PayloadType, pkt.PayloadType)
		assert.Equal(t, packets[i].SequenceNumber, pkt.SequenceNumber)
	}
	require.NoError(t, <-writeDone)

	stats := rtpSessRead.ReadStats()
	assert.EqualValues(t, CodecAudioAlaw.SampleRate, stats.SampleRate)
	assert.EqualValues(t, 3, stats.PacketsCount)
	assert.EqualValues(t, 3, stats.OctetCount)
}

func TestRTPSessionReadPassesAuxiliaryOnAudioSSRC(t *testing.T) {
	rtpSessRead, rtpSessWrite := pipeRTP(9876, 1234)
	rtpSessRead.Sess.Codecs = append(rtpSessRead.Sess.Codecs, CodecTelephoneEvent8000, CodecComfortNoise8000)

	const ssrc uint32 = 1234
	pts := []uint8{
		CodecTelephoneEvent8000.PayloadType, // before any audio
		CodecAudioAlaw.PayloadType,
		CodecTelephoneEvent8000.PayloadType, // mid-call DTMF
		CodecComfortNoise8000.PayloadType,   // negotiated CN
		CodecAudioAlaw.PayloadType,
		CodecAudioUlaw.PayloadType, // supported audio codec changes also pass through
		127,                        // unnegotiated payload remains rejected
		CodecAudioAlaw.PayloadType,
	}
	packets := make([]rtp.Packet, len(pts))
	for i, pt := range pts {
		packets[i] = rtp.Packet{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    pt,
				SequenceNumber: uint16(i + 1),
				Timestamp:      uint32(160 * (i + 1)),
				SSRC:           ssrc,
			},
			Payload: []byte{byte(i + 1)},
		}
	}

	writeDone := make(chan error, 1)
	go func() {
		for i := range packets {
			if err := rtpSessWrite.Sess.WriteRTP(&packets[i]); err != nil {
				writeDone <- err
				return
			}
		}
		writeDone <- nil
	}()

	buf := make([]byte, RTPBufSize)
	for _, wantSequenceNumber := range []uint16{1, 2, 3, 4, 5, 6, 8} {
		var pkt rtp.Packet
		n, err := rtpSessRead.ReadRTP(buf, &pkt)
		require.NoError(t, err)
		require.Positive(t, n)
		assert.Equal(t, wantSequenceNumber, pkt.SequenceNumber)
	}
	require.NoError(t, <-writeDone)

	stats := rtpSessRead.ReadStats()
	assert.EqualValues(t, CodecAudioAlaw.SampleRate, stats.SampleRate)
	assert.EqualValues(t, 7, stats.PacketsCount)
}

func TestRTPSessionReadSupportedClockRateChanges(t *testing.T) {
	sess := fakeSession(9876, 1234, nil, nil, nil, nil)
	t.Cleanup(sess.rtcpTicker.Stop)
	codec16k := Codec{Name: "custom-format", PayloadType: 112, SampleRate: 16000}
	sess.Sess.Codecs = append(sess.Sess.Codecs, codec16k, CodecTelephoneEvent8000)

	start := time.Unix(0, 0)
	for i, a := range []struct {
		codec     Codec
		timestamp uint32
	}{
		{CodecAudioAlaw, 0},
		{CodecAudioUlaw, 160},
		{codec16k, 10000},
		{codec16k, 10320},
		{CodecTelephoneEvent8000, 777},
		{CodecAudioAlaw, 937},
	} {
		pkt := &rtp.Packet{
			Header: rtp.Header{
				Version: 2, PayloadType: a.codec.PayloadType, SequenceNumber: uint16(i + 1),
				Timestamp: a.timestamp, SSRC: 1234,
			},
			Payload: []byte{1},
		}
		require.True(t, sess.updateReadStats(pkt, pkt.MarshalSize(), start.Add(time.Duration(i)*20*time.Millisecond)))
		stats := sess.ReadStats()
		assert.Equal(t, a.codec.SampleRate, stats.SampleRate)
		assert.EqualValues(t, i+1, stats.PacketsCount)
		assert.Zero(t, stats.jitter)
	}
}

func TestRTPSessionReadRejectsUnnegotiatedPayloadAfterSenderReport(t *testing.T) {
	sess := fakeSession(9876, 1234, nil, nil, nil, nil)
	t.Cleanup(sess.rtcpTicker.Stop)
	sess.Sess.Codecs = []Codec{CodecAudioAlaw} // Only PCMA (PT 8) is negotiated.

	// RTCP can establish the SSRC before the first RTP packet arrives.
	const ssrc uint32 = 1234
	sess.readRTCPPacket(&rtcp.SenderReport{SSRC: ssrc})
	require.Equal(t, ssrc, sess.ReadStats().SSRC)

	// PCMU (PT 0) matches the zero-valued payload cache, but has never been
	// validated. A known SSRC must not let this unnegotiated payload through.
	pkt := &rtp.Packet{
		Header:  rtp.Header{Version: 2, PayloadType: CodecAudioUlaw.PayloadType, SequenceNumber: 1, SSRC: ssrc},
		Payload: []byte{1},
	}
	assert.False(t, sess.updateReadStats(pkt, pkt.MarshalSize(), time.Unix(0, 0)), "unnegotiated PT 0 must be rejected")
	assert.Zero(t, sess.ReadStats().PacketsCount, "rejected RTP must not be counted")
}

func TestRTPSessionReadTelephoneEvents(t *testing.T) {
	checkTelephoneEvents := func(t *testing.T, codec Codec, audioPackets int, negotiated bool, wantDigits string) {
		t.Helper()
		events := RTPDTMFEncode8000('5')

		// Each reader holds one datagram; MultiReader returns one packet per Read.
		var packets []io.Reader
		addPacket := func(pt uint8, timestamp uint32, marker bool, payload []byte) {
			pkt := rtp.Packet{
				Header: rtp.Header{
					Version: 2, PayloadType: pt, SequenceNumber: uint16(len(packets) + 1),
					Timestamp: timestamp, SSRC: 1234, Marker: marker,
				},
				Payload: payload,
			}
			raw, err := pkt.Marshal()
			require.NoError(t, err)
			packets = append(packets, bytes.NewReader(raw))
		}
		for i := 0; i < audioPackets; i++ {
			addPacket(CodecAudioAlaw.PayloadType, uint32(i*160), i == 0, []byte{1})
		}
		timestamp := uint32(audioPackets * 160)
		for i, event := range events {
			addPacket(codec.PayloadType, timestamp, i == 0, DTMFEncode(event))
		}
		addPacket(CodecAudioAlaw.PayloadType, timestamp+800, true, []byte{1})

		read := fakeSession(9876, 1234, io.MultiReader(packets...), nil, nil, nil)
		t.Cleanup(read.rtcpTicker.Stop)
		if negotiated {
			read.Sess.Codecs = append(read.Sess.Codecs, codec)
		}
		packetReader := NewRTPPacketReaderSession(read)
		dtmfReader := NewRTPDTMFReader(codec, packetReader, packetReader)

		var digits string
		buf := make([]byte, RTPBufSize)
		for {
			_, err := dtmfReader.Read(buf)
			if err == io.EOF {
				break
			}
			require.NoError(t, err)
			if digit, ok := dtmfReader.ReadDTMF(); ok {
				digits += string(digit)
			}
		}
		assert.Equal(t, wantDigits, digits)
		wantPackets := audioPackets + 1
		if negotiated {
			wantPackets += len(events)
		}
		stats := read.ReadStats()
		assert.EqualValues(t, wantPackets, stats.PacketsCount)
		assert.Equal(t, CodecAudioAlaw.SampleRate, stats.SampleRate)
	}

	t.Run("audio before DTMF", func(t *testing.T) {
		checkTelephoneEvents(t, CodecTelephoneEvent8000, 5, true, "5")
	})

	t.Run("DTMF before audio", func(t *testing.T) {
		checkTelephoneEvents(t, CodecTelephoneEvent8000, 0, true, "5")
	})

	t.Run("negotiated dynamic payload type", func(t *testing.T) {
		codec := CodecTelephoneEvent8000
		codec.PayloadType = 110
		checkTelephoneEvents(t, codec, 5, true, "5")
	})

	t.Run("unnegotiated telephone-event", func(t *testing.T) {
		checkTelephoneEvents(t, CodecTelephoneEvent8000, 5, false, "")
	})
}

func TestRTPSessionJitter(t *testing.T) {
	type arrival struct {
		pt        uint8
		timestamp uint32
		ms        int
		marker    bool
		jitter    float64
	}
	checkJitter := func(t *testing.T, arrivals []arrival) {
		t.Helper()
		sess := fakeSession(9876, 1234, nil, nil, nil, nil)
		t.Cleanup(sess.rtcpTicker.Stop)
		sess.Sess.Codecs = append(sess.Sess.Codecs, CodecTelephoneEvent8000)
		start := time.Unix(0, 0)
		for i, a := range arrivals {
			pkt := &rtp.Packet{
				Header: rtp.Header{
					Version: 2, PayloadType: a.pt, SequenceNumber: uint16(i + 1),
					Timestamp: a.timestamp, SSRC: 1, Marker: a.marker,
				},
				Payload: []byte{1},
			}
			now := start.Add(time.Duration(a.ms) * time.Millisecond)
			require.True(t, sess.updateReadStats(pkt, pkt.MarshalSize(), now))
			assert.InDelta(t, a.jitter, sess.readStats.jitter, 1e-9, "packet %d", i+1)
		}
		assert.EqualValues(t, len(arrivals), sess.ReadStats().PacketsCount)
		var report rtcp.ReceptionReport
		sess.parseReceptionReport(&report, start.Add(time.Second))
		assert.EqualValues(t, uint32(arrivals[len(arrivals)-1].jitter), report.Jitter)
	}

	t.Run("telephone-event updates and end retransmissions", func(t *testing.T) {
		// RFC 4733 section 2.5.2.2 includes packets with repeated
		// timestamps in RTCP jitter. At 8 kHz, 20 ms is 160 ticks.
		checkJitter(t, []arrival{
			{8, 0, 0, false, 0},
			{8, 160, 20, false, 0},
			{101, 320, 40, true, 0},
			{101, 320, 60, false, 10},
			{101, 320, 80, false, 19.375},
			{8, 800, 100, false, 38.1640625},
		})
	})

	t.Run("telephone-event marker resets baseline", func(t *testing.T) {
		checkJitter(t, []arrival{
			{8, 0, 0, false, 0},
			{8, 160, 20, false, 0},
			{101, 320, 50, true, 0},
			{8, 480, 60, false, 5},
		})
	})

	t.Run("audio marker resets baseline", func(t *testing.T) {
		checkJitter(t, []arrival{
			{8, 0, 0, true, 0},
			{8, 160, 20, false, 0},
			{8, 320, 50, true, 0},
			{8, 480, 60, false, 5},
		})
	})

	t.Run("audio marker after paused stream", func(t *testing.T) {
		checkJitter(t, []arrival{
			{8, 0, 0, true, 0},
			{8, 160, 20, false, 0},
			{8, 320, 10000, true, 0},
			{8, 480, 10020, false, 0},
		})
	})

	t.Run("repeated event timestamp across clock wrap", func(t *testing.T) {
		checkJitter(t, []arrival{
			{8, 0xfffffff0, 0, false, 0},
			{101, 0xfffffff0, 20, false, 10},
			{8, 304, 40, false, 19.375},
		})
	})
}

func TestRTPSessionReadCodecChangesWithSSRC(t *testing.T) {
	rtpSessRead, rtpSessWrite := pipeRTP(9876, 1234)
	codec16k := Codec{
		Name:        "test",
		PayloadType: 96,
		SampleRate:  16000,
		NumChannels: 1,
	}
	rtpSessRead.Sess.Codecs = append(rtpSessRead.Sess.Codecs, codec16k)

	packets := []rtp.Packet{
		{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    CodecAudioAlaw.PayloadType,
				SequenceNumber: 1,
				Timestamp:      160,
				SSRC:           1234,
			},
			Payload: []byte{1},
		},
		{
			Header: rtp.Header{
				Version:        2,
				PayloadType:    codec16k.PayloadType,
				SequenceNumber: 2,
				Timestamp:      320,
				SSRC:           5678,
			},
			Payload: []byte{2},
		},
	}

	writeDone := make(chan error, 1)
	go func() {
		for i := range packets {
			if err := rtpSessWrite.Sess.WriteRTP(&packets[i]); err != nil {
				writeDone <- err
				return
			}
		}
		writeDone <- nil
	}()

	buf := make([]byte, RTPBufSize)
	for i := range packets {
		var pkt rtp.Packet
		n, err := rtpSessRead.ReadRTP(buf, &pkt)
		require.NoError(t, err)
		require.Positive(t, n)
		assert.Equal(t, packets[i].SSRC, pkt.SSRC)
		assert.Equal(t, packets[i].PayloadType, pkt.PayloadType)
	}
	require.NoError(t, <-writeDone)

	stats := rtpSessRead.ReadStats()
	assert.Equal(t, packets[1].SSRC, stats.SSRC)
	assert.EqualValues(t, codec16k.SampleRate, stats.SampleRate)
	assert.EqualValues(t, 1, stats.PacketsCount)
	assert.EqualValues(t, 1, stats.OctetCount)
}

func TestRTPSessionReading(t *testing.T) {
	// pipeRTP := bytes.NewBuffer([]byte{})

	rtpSessRead, rtpSessWrite := pipeRTP(9876, 1234)

	// Now setup RTP session as reader
	// rtpSess := newRTPSession(rtpRead, NewRTPWriter(rtpRead.Sess))
	rtpSessRead.rtcpTicker = time.NewTicker(1 * time.Hour) // DO NOT TICK
	// rtpSess.rtcpTicker = time.NewTicker(500 * time.Millisecond) // Make fast rtcp

	// 1 means good, 0 means sequence number skipepd
	rtpStream := []int{
		1, 1, 1, 1, 1,
		0, 1, 0, 0, 1,
		1, 1, 1, 1, 1,
	}

	rtpWriter := NewRTPPacketWriterSession(rtpSessWrite)
	go func() {
		// Setup remote session
		// defer rtpWrite.Sess.Close()

		payload := make([]byte, 160)

		for _, b := range rtpStream {
			switch b {
			case 1:
			case 0:
				rtpWriter.seqWriter.NextSeqNumber()
			}

			_, err := rtpWriter.Write(payload)
			assert.NoError(t, err)
		}
	}()

	rtpReader := NewRTPPacketReaderSession(rtpSessRead)
	readBuf := make([]byte, 1500)
	for i := 0; i < len(rtpStream); i++ {
		_, err := rtpReader.Read(readBuf)
		if err != nil {
			break
		}
	}

	// stream pkts + 3 -1 as increase of seq number
	lostPackets := 2
	expectedPkts := len(rtpStream) + lostPackets

	// rtpSess.readStats.firstPktSequenceNumber
	assert.Equal(t, len(rtpStream), int(rtpSessRead.readStats.IntervalPacketsCount))
	// assert.Equal(t, Npkts, int(rtpSess.readStats.intervalTotalPackets))

	// Now make a sender report
	senderReport := rtcp.SenderReport{}
	rtpSessRead.parseSenderReport(&senderReport, time.Now(), 1234)

	recReport := senderReport.Reports[0]
	assert.Equal(t, uint32(1234), senderReport.SSRC)
	assert.Equal(t, lostPackets, int(recReport.TotalLost))
	// firstpkt + expected pkts = last seq numb
	assert.Equal(t, int(rtpSessRead.readStats.FirstPktSequenceNumber)+expectedPkts, int(recReport.LastSequenceNumber))
	assert.Equal(t, int(float32(lostPackets)/float32(expectedPkts)*256), int(recReport.FractionLost))
}

func TestRTPSessionWriting(t *testing.T) {
	// pipeRTP := bytes.NewBuffer([]byte{})

	rtpSessRead, rtpSessWrite := pipeRTP(9876, 1234)

	// Now setup RTP session as reader
	rtpSessRead.rtcpTicker = time.NewTicker(1 * time.Hour) // DO NOT TICK
	// rtpSess.rtcpTicker = time.NewTicker(500 * time.Millisecond) // Make fast rtcp

	// 1 means good, 0 means sequence number skipepd
	rtpStream := []int{
		1, 1, 1, 1, 1,
		0, 1, 0, 0, 1,
		1, 1, 1, 1, 1,
	}

	rtpReader := NewRTPPacketReaderSession(rtpSessRead)
	go func() {
		readBuf := make([]byte, 1500)
		for i := 0; i < len(rtpStream); i++ {
			_, err := rtpReader.Read(readBuf)
			if err != nil {
				break
			}
		}
	}()

	rtpWriter := NewRTPPacketWriterSession(rtpSessWrite)
	payload := make([]byte, 160)
	for _, b := range rtpStream {
		switch b {
		case 1:
		case 0:
			rtpWriter.seqWriter.NextSeqNumber()
		}

		_, err := rtpWriter.Write(payload)
		assert.NoError(t, err)
	}

	// stream pkts + 3 -1 as increase of seq number
	// lostPackets := 2
	// expectedPkts := len(rtpStream) + lostPackets

	// rtpSess.readStats.firstPktSequenceNumber
	// assert.Equal(t, len(rtpStream), int(rtpSess.readStats.intervalTotalPackets))
	// assert.Equal(t, Npkts, int(rtpSess.readStats.intervalTotalPackets))

	// Now make a sender report
	senderReport := rtcp.SenderReport{}
	fmt.Println(rtpSessWrite.writeStats.lastPacketTime, time.Now())
	now := time.Now()
	rtpSessWrite.parseSenderReport(&senderReport, now, rtpWriter.SSRC)

	N := len(rtpStream)
	assert.Equal(t, rtpWriter.SSRC, senderReport.SSRC)
	assert.Equal(t, uint32(N), senderReport.PacketCount, "packets=%d ", senderReport.PacketCount)
	assert.Equal(t, uint32(N)*160, senderReport.OctetCount, "octes=%d", senderReport.OctetCount)
	assert.LessOrEqual(t, rtpWriter.initTimestamp+uint32(N-1)*160, senderReport.RTPTime, "RTPTime=%d", int(senderReport.RTPTime))

	// recReport := senderReport.Reports[0]
	// assert.Equal(t, lostPackets, int(recReport.TotalLost))
	// firstpkt + expected pkts = last seq numb
	// assert.Equal(t, int(rtpSess.readStats.firstPktSequenceNumber)+expectedPkts, int(recReport.LastSequenceNumber))
	// assert.Equal(t, int(float32(lostPackets)/float32(expectedPkts)*256), int(recReport.FractionLost))
}

func TestRTPSessionRTCPCallbacksConcurrentUpdate(t *testing.T) {
	const iterations = 10_000

	t.Run("read", func(t *testing.T) {
		rtpSess := fakeSession(9876, 1234, nil, nil, nil, nil)
		t.Cleanup(rtpSess.rtcpTicker.Stop)

		callback := func(rtcp.Packet, RTPReadStats) {}
		start := make(chan struct{})
		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < iterations; i++ {
				if i%2 == 0 {
					rtpSess.OnReadRTCP(callback)
				} else {
					rtpSess.OnReadRTCP(nil)
				}
			}
		}()

		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < iterations; i++ {
				rtpSess.readRTCPPacket(&rtcp.ReceiverReport{})
			}
		}()

		close(start)
		wg.Wait()
	})

	t.Run("write", func(t *testing.T) {
		rtpSess := fakeSession(9876, 1234, nil, nil, nil, io.Discard)
		t.Cleanup(rtpSess.rtcpTicker.Stop)
		rtpSess.writeStats.SSRC = 1

		callback := func(rtcp.Packet, RTPWriteStats) {}
		start := make(chan struct{})
		errCh := make(chan error, 1)
		var wg sync.WaitGroup
		wg.Add(2)

		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < iterations; i++ {
				if i%2 == 0 {
					rtpSess.OnWriteRTCP(callback)
				} else {
					rtpSess.OnWriteRTCP(nil)
				}
			}
		}()

		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < iterations; i++ {
				if err := rtpSess.writeRTCP(time.Now()); err != nil {
					errCh <- err
					return
				}
			}
		}()

		close(start)
		wg.Wait()
		close(errCh)
		for err := range errCh {
			require.NoError(t, err)
		}
	})
}

// func TestRTPSessionMonitoring(t *testing.T) {
// 	// LSR and DLSR calc
// 	// SenderReport sent and SenderReport received
// 	rtcpReader, rtcpWriter := io.Pipe()
// 	rtpRawReader, rtpRawWriter := io.Pipe()
// 	rtpR, rtpW := fakeSession(1234, 9876, nil, rtpRawWriter, rtcpReader, nil)

// 	rtpSess := newRTPSession(rtpR, rtpW)
// 	rtpSess.Monitor()

// 	// How to trigger RTCP with sent data
// 	rtpSess.Write()
// 	sr := rtcp.SenderReport{
// 		SSRC: 10,
// 	}
// 	data, _ := sr.Marshal()
// 	rtcpWriter.Write(data)

// }

func TestRTPSessionClose(t *testing.T) {
	sess, err := NewMediaSession(net.IPv4(127, 0, 0, 1), 0)
	require.NoError(t, err)

	rtpSess := NewRTPSession(sess)

	closed := make(chan struct{})
	go func() {
		defer close(closed)
		rtpSess.readRTCP()
	}()

	time.Sleep(100 * time.Millisecond)
	rtpSess.Close()

	select {
	case <-time.After(3 * time.Second):
		t.Error("RTP Session did not close RTCP")
		return
	case <-closed:
	}

	go func() {
		time.Sleep(100 * time.Millisecond)
		if err := rtpSess.Close(); err != nil {
			t.Error(err)
		}
	}()
	err = rtpSess.readRTCP()
	neterr, ok := err.(net.Error)
	require.True(t, ok)
	require.True(t, neterr.Timeout())
}

type pipePacketConn struct {
	net.Conn
}

func (c *pipePacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	n, err := c.Conn.Read(p)
	return n, c.Conn.RemoteAddr(), err
}

func (c *pipePacketConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	return c.Conn.Write(p)
}

func TestRTPSessionFork(t *testing.T) {
	rtpConn, rtpPeer := net.Pipe()
	rtcpConn, rtcpPeer := net.Pipe()
	t.Cleanup(func() {
		_ = rtpConn.Close()
		_ = rtpPeer.Close()
		_ = rtcpConn.Close()
		_ = rtcpPeer.Close()
	})

	sess := &MediaSession{
		Codecs:    []Codec{CodecAudioUlaw},
		Mode:      sdp.ModeSendrecv,
		Laddr:     net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1234},
		Raddr:     net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9876},
		rtcpRaddr: net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9877},
		rtpConn:   &pipePacketConn{Conn: rtpConn},
		rtcpConn:  &pipePacketConn{Conn: rtcpConn},
	}

	rtpSess := NewRTPSession(sess)
	rtpSess.readStats.PacketsCount = 7
	rtpSess.writeStats = RTPWriteStats{
		SSRC:                1,
		lastPacketTime:      time.Now(),
		lastPacketTimestamp: 1,
		sampleRate:          CodecAudioUlaw.SampleRate,
	}

	require.NoError(t, rtpSess.MonitorBackground())
	require.NoError(t, rtpSess.MonitorClose())

	candidate := sess.Fork()
	candidate.SetRemoteAddr(&sess.Raddr)
	fork := rtpSess.Fork(candidate)
	// Confirm Forking works
	assert.Equal(t, uint64(7), fork.ReadStats().PacketsCount)

	// Start the replacement monitor with a short report interval and confirm
	// its first RTCP write succeeds on the shared deadline-aware connection.
	fork.rtcpTicker.Stop()
	fork.rtcpTicker = time.NewTicker(10 * time.Millisecond)
	require.NoError(t, fork.MonitorBackground())
	require.NoError(t, rtcpPeer.SetReadDeadline(time.Now().Add(time.Second)))
	buf := make([]byte, 1500)
	n, err := rtcpPeer.Read(buf)
	require.NoError(t, err)
	require.NotZero(t, n)
	require.NoError(t, fork.MonitorClose())
}

func TestRTTCalc(t *testing.T) {
	now := time.Now()
	lsrTime := now.Add(-6 * time.Second)
	lsrNTP := NTPTimestamp(lsrTime)
	lsr := uint32(lsrNTP >> 16)

	dur, skewed := calcRTT(now, lsr, 0)
	assert.False(t, skewed)
	assert.Equal(t, 6*time.Second, dur)

	dur, skewed = calcRTT(now, lsr, 5*65356) // Delay was 5 second
	assert.False(t, skewed)
	// Due to dividing this can not be exact
	assert.GreaterOrEqual(t, dur, 1*time.Second)
	assert.LessOrEqual(t, dur, 1*time.Second+20*time.Millisecond)
}

func TestJitterCalc(t *testing.T) {
	stats := RTPReadStats{
		SampleRate: 8000,
	}

	now := time.Now()
	stats.firstRTPTime = now
	stats.firstRTPTimestamp = 160
	stats.calcJitter(now.Add(20*time.Millisecond), 160*2)
	assert.EqualValues(t, 0, int(stats.jitter))

	stats.calcJitter(now.Add(40*time.Millisecond), 160*3)
	assert.EqualValues(t, 0, int(stats.jitter))

	stats.calcJitter(now.Add(75*time.Millisecond), 160*4)
	assert.EqualValues(t, 7, int(stats.jitter))

	stats.calcJitter(now.Add(80*time.Millisecond), 160*5)
	assert.EqualValues(t, 14, int(stats.jitter))

	stats.calcJitter(now.Add(100*time.Millisecond), 160*6)
	assert.EqualValues(t, 13, int(stats.jitter))

	// Simulate a gap
	// stats.calcJitterRFC(now.Add(5*time.Second), 640)
	// assert.EqualValues(t, 0, int(stats.jitter))
}

func TestRTPSessionSourceLockProtection(t *testing.T) {
	// slog.SetLogLoggerLevel(slog.LevelDebug)
	// RTPDebug = true

	rtpSessRead, rtpSessWrite := pipeRTP(9876, 1234)
	rtpSessRead.sourceLock = true // Enable source locking

	go func() {
		var seq uint16 = 1
		for ; seq < 5; seq++ {
			pkt := rtp.Packet{
				Header: rtp.Header{
					Version:        2,
					SequenceNumber: seq,
				},
				Payload: []byte{1, 2, 3},
			}
			rtpSessWrite.WriteRTP(&pkt)
		}
	}()

	pkt := rtp.Packet{}
	_, err := rtpSessRead.ReadRTP(make([]byte, 1600), &pkt)
	require.NoError(t, err)

	assert.Equal(t, uint16(4), pkt.SequenceNumber)
}
