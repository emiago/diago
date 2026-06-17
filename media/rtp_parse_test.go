// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package media

import (
	"io"
	"testing"

	"github.com/emiago/sipgo/fakes"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/require"
)

// TestRTPUnmarshalResetsPaddingSize is a regression test for #140.
//
// `rtpUnmarshalPayload` reuses the *rtp.Packet across reads. When a
// packet with Padding=true was followed by a packet with Padding=false,
// the second packet's PaddingSize was left stale at the first packet's
// value, causing the downstream payload-size calculation in
// rtp_packet_reader.go (and rtp_session.go) to truncate the payload.
func TestRTPUnmarshalResetsPaddingSize(t *testing.T) {
	const payloadLen = 160
	payload := make([]byte, payloadLen)
	for i := range payload {
		payload[i] = byte(i)
	}

	// First packet: Padding=true. Marshal a packet whose padding is
	// actually present in the wire bytes.
	paddedPkt := rtp.Packet{
		Header:  rtp.Header{Version: 2, Padding: true, PayloadType: 8, SequenceNumber: 1, Timestamp: 100, SSRC: 0xDEADBEEF, PaddingSize: 4},
		Payload: payload,
	}
	paddedBuf, err := paddedPkt.Marshal()
	require.NoError(t, err)

	// Second packet: Padding=false, same SSRC, no padding in wire bytes.
	cleanPkt := rtp.Packet{
		Header:  rtp.Header{Version: 2, PayloadType: 8, SequenceNumber: 2, Timestamp: 260, SSRC: 0xDEADBEEF},
		Payload: payload,
	}
	cleanBuf, err := cleanPkt.Marshal()
	require.NoError(t, err)

	// Reuse the same *rtp.Packet across both reads, exactly as
	// rtp_packet_reader.go does.
	pkt := &rtp.Packet{}

	require.NoError(t, RTPUnmarshal(paddedBuf, pkt))
	require.Equal(t, byte(4), pkt.PaddingSize, "first packet should report its padding size")
	require.Len(t, pkt.Payload, payloadLen, "first packet payload should match input length")

	require.NoError(t, RTPUnmarshal(cleanBuf, pkt))
	require.Equal(t, byte(0), pkt.PaddingSize, "PaddingSize must reset when Padding bit is unset (#140)")
	require.Len(t, pkt.Payload, payloadLen, "second packet's payload must not be truncated by stale PaddingSize")
	require.Equal(t, payload, pkt.Payload, "second packet payload bytes must match input verbatim")
}

func BenchmarkRTCPUnmarshal(b *testing.B) {
	reader, writer := io.Pipe()
	go func() {
		for {
			sr := rtcp.SenderReport{}
			data, err := sr.Marshal()
			if err != nil {
				return
			}

			writer.Write(data)
		}
	}()

	b.Run("pionRTCP", func(b *testing.B) {
		buf := make([]byte, 1500)
		for i := 0; i < b.N; i++ {
			n, err := reader.Read(buf)
			if err != nil {
				b.Fatal(err)
			}
			pkts, err := rtcp.Unmarshal(buf[:n])
			if err != nil {
				b.Fatal(err)
			}
			if len(pkts) == 0 {
				b.Fatal("no packet read")
			}
		}
	})

	b.Run("RTCPImproved", func(b *testing.B) {
		buf := make([]byte, 1500)
		pkts := make([]rtcp.Packet, 5)
		for i := 0; i < b.N; i++ {
			n, err := reader.Read(buf)
			if err != nil {
				b.Fatal(err)
			}
			n, err = RTCPUnmarshal(buf[:n], pkts)
			if err != nil {
				b.Fatal(err)
			}
			if n < 0 {
				b.Fatal("no read RTCP")
			}
		}
	})
}

func BenchmarkReadRTP(b *testing.B) {
	session := &MediaSession{}
	reader, writer := io.Pipe()
	session.rtpConn = &fakes.UDPConn{
		Reader: reader,
	}

	go func() {
		for {
			pkt := rtp.Packet{
				Payload: make([]byte, 160),
			}
			data, err := pkt.Marshal()
			if err != nil {
				return
			}
			writer.Write(data)
		}
	}()

	b.Run("return", func(b *testing.B) {
		b.ResetTimer()
		b.ReportAllocs()

		b.RunParallel(func(p *testing.PB) {
			for p.Next() {
				pkt, err := session.readRTPParsed()
				if err != nil {
					b.Fatal(err)
				}
				if len(pkt.Payload) != 160 {
					b.Fatal("payload not parsed")
				}
			}
		})

	})

	b.Run("pass", func(b *testing.B) {
		b.ResetTimer()
		b.ReportAllocs()

		b.RunParallel(func(p *testing.PB) {
			buf := make([]byte, RTPBufSize)
			for p.Next() {
				pkt := rtp.Packet{}
				_, err := session.ReadRTP(buf, &pkt)
				if err != nil {
					b.Fatal(err)
				}
				if len(pkt.Payload) != 160 {
					b.Fatal("payload not parsed")
				}
			}
		})
	})

	b.Run("withPayloadBuf", func(b *testing.B) {
		b.ResetTimer()
		b.ReportAllocs()

		b.RunParallel(func(p *testing.PB) {
			buf := make([]byte, RTPBufSize)
			for p.Next() {
				pkt := rtp.Packet{
					Payload: buf,
				}
				_, err := session.ReadRTP(buf, &pkt)
				if err != nil {
					b.Fatal(err)
				}
				if len(pkt.Payload) == 0 {
					b.Fatal("payload not parsed")
				}
			}
		})
	})
}
