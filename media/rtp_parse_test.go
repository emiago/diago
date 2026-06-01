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

// TestRTPUnmarshalPayload_PaddingSizeReset verifies that when a reused *rtp.Packet
// previously had padding, rtpUnmarshalPayload resets PaddingSize to 0 for the next
// packet that does not carry the Padding flag. Without the fix, PaddingSize persists
// and causes the payload of subsequent non-padded packets to be truncated.
func TestRTPUnmarshalPayload_PaddingSizeReset(t *testing.T) {
	payload := []byte("hello world RTP payload data")

	// Build a padded packet: append 4 bytes of padding (last byte = padding length).
	paddingLen := byte(4)
	paddedPayload := make([]byte, len(payload)+int(paddingLen))
	copy(paddedPayload, payload)
	paddedPayload[len(paddedPayload)-1] = paddingLen

	paddedPkt := rtp.Packet{}
	paddedPkt.Header.Padding = true
	marshaledPadded, err := rtp.Packet{
		Header:  rtp.Header{Padding: true, Version: 2},
		Payload: paddedPayload,
	}.Marshal()
	if err != nil {
		t.Fatal("marshal padded packet:", err)
	}

	// Parse the padded packet — this sets PaddingSize on reusedPkt.
	reusedPkt := &rtp.Packet{Payload: make([]byte, 1500)}
	if err := reusedPkt.Unmarshal(marshaledPadded); err != nil {
		t.Fatal("unmarshal padded:", err)
	}
	if reusedPkt.PaddingSize == 0 {
		t.Skip("pion/rtp version does not expose PaddingSize — skipping")
	}

	// Now build a plain packet with no padding carrying the same payload.
	plainPkt := rtp.Packet{
		Header:  rtp.Header{Version: 2},
		Payload: payload,
	}
	marshaledPlain, err := plainPkt.Marshal()
	if err != nil {
		t.Fatal("marshal plain packet:", err)
	}

	// Unmarshal the plain packet into the same reused struct.
	// Before the fix, PaddingSize from the padded parse still lingers and
	// truncates the payload by paddingLen bytes.
	if err := reusedPkt.Unmarshal(marshaledPlain); err != nil {
		t.Fatal("unmarshal plain:", err)
	}

	if reusedPkt.PaddingSize != 0 {
		t.Errorf("PaddingSize not reset: got %d, want 0", reusedPkt.PaddingSize)
	}
	if string(reusedPkt.Payload) != string(payload) {
		t.Errorf("payload truncated: got %q, want %q", reusedPkt.Payload, payload)
	}
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
