// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package media

import (
	"fmt"
	"io"
	"net"
	"testing"
	"time"

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
