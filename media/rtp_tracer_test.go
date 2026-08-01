// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package media

import (
	"bytes"
	"io"
	"log/slog"
	"net"
	"testing"

	"github.com/emiago/sipgo/fakes"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/require"
)

type rtpTraceEvent struct {
	laddr  string
	raddr  string
	packet *rtp.Packet
}

type testRTPTracer struct {
	reads  []rtpTraceEvent
	writes []rtpTraceEvent
}

func (t *testRTPTracer) RTPTraceRead(laddr string, raddr string, packet *rtp.Packet) {
	t.reads = append(t.reads, rtpTraceEvent{laddr: laddr, raddr: raddr, packet: packet.Clone()})
}

func (t *testRTPTracer) RTPTraceWrite(laddr string, raddr string, packet *rtp.Packet) {
	t.writes = append(t.writes, rtpTraceEvent{laddr: laddr, raddr: raddr, packet: packet.Clone()})
}

func TestRTPDebugTracer(t *testing.T) {
	prevDebug := RTPDebug
	prevTracer := rtpTracer
	prevLogger := defLogger
	t.Cleanup(func() {
		RTPDebug = prevDebug
		RTPDebugTracer(prevTracer)
		SetDefaultLogger(prevLogger)
	})

	tracer := &testRTPTracer{}
	RTPDebugTracer(tracer)

	sess := fakeMediaSessionWriter(0, 1234, nil)
	pkt := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    8,
			SequenceNumber: 42,
			Timestamp:      160,
			SSRC:           1234,
		},
		Payload: []byte("payload"),
	}

	RTPDebug = false
	require.NoError(t, sess.WriteRTP(pkt))
	require.Empty(t, tracer.writes)

	RTPDebug = true
	require.NoError(t, sess.WriteRTP(pkt))
	require.Len(t, tracer.writes, 1)
	require.Equal(t, sess.Laddr.String(), tracer.writes[0].laddr)
	require.Equal(t, sess.Raddr.String(), tracer.writes[0].raddr)
	require.Equal(t, pkt, tracer.writes[0].packet)

	data, err := pkt.Marshal()
	require.NoError(t, err)
	reader := &MediaSession{
		Laddr: sess.Laddr,
		Raddr: sess.Raddr,
		rtpConn: &fakes.UDPConn{
			RAddr:  sess.Raddr,
			Reader: bytes.NewReader(data),
		},
	}
	readPkt := &rtp.Packet{}
	_, err = reader.ReadRTP(make([]byte, RTPBufSize), readPkt)
	require.NoError(t, err)
	require.Len(t, tracer.reads, 1)
	require.Equal(t, reader.Laddr.String(), tracer.reads[0].laddr)
	require.Equal(t, reader.Raddr.String(), tracer.reads[0].raddr)
	require.Equal(t, pkt.SequenceNumber, tracer.reads[0].packet.SequenceNumber)
	require.Equal(t, pkt.Timestamp, tracer.reads[0].packet.Timestamp)
	require.Equal(t, pkt.PayloadType, tracer.reads[0].packet.PayloadType)
	require.Equal(t, pkt.SSRC, tracer.reads[0].packet.SSRC)
	require.Equal(t, pkt.Payload, tracer.reads[0].packet.Payload)

	RTPDebugTracer(nil)
	require.Nil(t, rtpTracer)

	var logs bytes.Buffer
	SetDefaultLogger(slog.New(slog.NewTextHandler(&logs, &slog.HandlerOptions{Level: slog.LevelDebug})))
	logRTPWrite(sess, pkt)
	require.Contains(t, logs.String(), "RTP write")
}

func TestRTPDebugTracerSkipsFailedWrite(t *testing.T) {
	prevDebug := RTPDebug
	prevTracer := rtpTracer
	t.Cleanup(func() {
		RTPDebug = prevDebug
		RTPDebugTracer(prevTracer)
	})

	tracer := &testRTPTracer{}
	RTPDebug = true
	RTPDebugTracer(tracer)

	sess := &MediaSession{
		Laddr: net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)},
		Raddr: net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 1234},
		rtpConn: &fakes.UDPConn{
			Writers: map[string]io.Writer{},
		},
	}

	err := sess.WriteRTP(&rtp.Packet{Header: rtp.Header{Version: 2}})
	require.Error(t, err)
	require.Empty(t, tracer.writes)
}
