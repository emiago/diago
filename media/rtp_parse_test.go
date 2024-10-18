// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package media

import (
	"io"
	"testing"

	"github.com/emiago/sipgo/fakes"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
)

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
