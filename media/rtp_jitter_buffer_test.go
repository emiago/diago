// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package media

import (
	"encoding/binary"
	"io"
	"testing"
	"time"

	"github.com/pion/rtp"
	"github.com/stretchr/testify/require"
)

type sliceRTPReader struct {
	packets []rtp.Packet
	pos     int
}

func (r *sliceRTPReader) ReadRTP(buf []byte, p *rtp.Packet) (int, error) {
	if r.pos >= len(r.packets) {
		return 0, io.EOF
	}

	pkt := r.packets[r.pos]
	r.pos++

	raw, err := pkt.Marshal()
	if err != nil {
		return 0, err
	}
	if len(buf) < len(raw) {
		return 0, io.ErrShortBuffer
	}

	n := copy(buf, raw)
	return n, RTPUnmarshal(buf[:n], p)
}

type chanRTPReader struct {
	packets chan rtp.Packet
}

func newChanRTPReader() *chanRTPReader {
	return &chanRTPReader{packets: make(chan rtp.Packet)}
}

func (r *chanRTPReader) ReadRTP(buf []byte, p *rtp.Packet) (int, error) {
	pkt, ok := <-r.packets
	if !ok {
		return 0, io.EOF
	}

	raw, err := pkt.Marshal()
	if err != nil {
		return 0, err
	}
	if len(buf) < len(raw) {
		return 0, io.ErrShortBuffer
	}

	n := copy(buf, raw)
	return n, RTPUnmarshal(buf[:n], p)
}

func TestRTPJitterBuffer(t *testing.T) {
	t.Run("inOrder", func(t *testing.T) {
		jb := newTestRTPJitterBuffer(t, rtpPackets(1234, 0, 1, 2))

		require.Equal(t, uint16(0), readJitterSeq(t, jb))
		require.Equal(t, uint16(1), readJitterSeq(t, jb))
		require.Equal(t, uint16(2), readJitterSeq(t, jb))
		requireJitterEOF(t, jb)
	})

	t.Run("outOfOrder", func(t *testing.T) {
		jb := newTestRTPJitterBuffer(t, rtpPackets(1234, 0, 2, 1, 3))

		require.Equal(t, uint16(0), readJitterSeq(t, jb))
		require.Equal(t, uint16(1), readJitterSeq(t, jb))
		require.Equal(t, uint16(2), readJitterSeq(t, jb))
		require.Equal(t, uint16(3), readJitterSeq(t, jb))
	})

	t.Run("missingPacket", func(t *testing.T) {
		jb := newTestRTPJitterBuffer(t, rtpPackets(1234, 0, 2))

		require.Equal(t, uint16(0), readJitterSeq(t, jb))
		require.Equal(t, uint16(2), readJitterSeq(t, jb))
		require.Equal(t, uint64(1), jb.Statistics().PacketsLost)
	})

	t.Run("duplicatePacket", func(t *testing.T) {
		jb := newTestRTPJitterBuffer(t, rtpPackets(1234, 0, 1, 1, 2))

		require.Equal(t, uint16(0), readJitterSeq(t, jb))
		require.Equal(t, uint16(1), readJitterSeq(t, jb))
		require.Equal(t, uint16(2), readJitterSeq(t, jb))
		require.Equal(t, uint64(1), jb.Statistics().PacketsDuplicate)
	})

	t.Run("sequenceWrap", func(t *testing.T) {
		jb := newTestRTPJitterBuffer(t, rtpPackets(1234, 65534, 0, 65535, 1))

		require.Equal(t, uint16(65534), readJitterSeq(t, jb))
		require.Equal(t, uint16(65535), readJitterSeq(t, jb))
		require.Equal(t, uint16(0), readJitterSeq(t, jb))
		require.Equal(t, uint16(1), readJitterSeq(t, jb))
	})

	t.Run("latePacket", func(t *testing.T) {
		reader := newChanRTPReader()
		jb := NewRTPJitterBuffer(reader, RTPJitterBufferOptions{
			PacketDuration: time.Millisecond,
			DelayPackets:   1,
			MaxPackets:     4,
		})

		done := make(chan uint16, 1)
		go func() {
			done <- readJitterSeq(t, jb)
		}()
		reader.packets <- rtpPacket(1234, 0)
		require.Equal(t, uint16(0), <-done)

		go func() {
			done <- readJitterSeq(t, jb)
		}()
		reader.packets <- rtpPacket(1234, 2)
		require.Equal(t, uint16(2), <-done)

		reader.packets <- rtpPacket(1234, 1)
		close(reader.packets)
		requireJitterEOF(t, jb)
		require.Equal(t, uint64(1), jb.Statistics().PacketsLate)
	})

	t.Run("ssrcReset", func(t *testing.T) {
		reader := newChanRTPReader()
		jb := NewRTPJitterBuffer(reader, RTPJitterBufferOptions{
			PacketDuration: time.Millisecond,
			DelayPackets:   1,
			MaxPackets:     4,
		})

		done := make(chan uint16, 1)
		go func() {
			done <- readJitterSeq(t, jb)
		}()
		reader.packets <- rtpPacket(1111, 0)
		require.Equal(t, uint16(0), <-done)

		go func() {
			done <- readJitterSeq(t, jb)
		}()
		reader.packets <- rtpPacket(2222, 10)
		require.Equal(t, uint16(10), <-done)

		require.Equal(t, uint64(1), jb.Statistics().SSRCResets)
	})

	t.Run("startupEarlierPacket", func(t *testing.T) {
		jb := newTestRTPJitterBuffer(t, rtpPackets(1234, 2, 0, 1, 3))

		require.Equal(t, uint16(0), readJitterSeq(t, jb))
		require.Equal(t, uint16(1), readJitterSeq(t, jb))
		require.Equal(t, uint16(2), readJitterSeq(t, jb))
		require.Equal(t, uint16(3), readJitterSeq(t, jb))
	})

	t.Run("forwardWindowDrop", func(t *testing.T) {
		jb := NewRTPJitterBuffer(&sliceRTPReader{packets: rtpPackets(1234, 0, 4, 1)}, RTPJitterBufferOptions{
			PacketDuration: time.Millisecond,
			DelayPackets:   2,
			MaxPackets:     4,
		})

		require.Equal(t, uint16(0), readJitterSeq(t, jb))
		require.Equal(t, uint16(1), readJitterSeq(t, jb))
		requireJitterEOF(t, jb)
		require.Equal(t, uint64(1), jb.Statistics().PacketsDropped)
	})

	t.Run("shortBufferRetry", func(t *testing.T) {
		jb := NewRTPJitterBuffer(&sliceRTPReader{packets: rtpPackets(1234, 7)}, RTPJitterBufferOptions{
			PacketDuration: time.Millisecond,
			DelayPackets:   1,
			MaxPackets:     2,
		})

		var pkt rtp.Packet
		_, err := jb.ReadRTP(make([]byte, 1), &pkt)
		require.ErrorIs(t, err, io.ErrShortBuffer)
		require.Equal(t, uint16(7), readJitterSeq(t, jb))
	})

	t.Run("slotReuse", func(t *testing.T) {
		reader := &chanRTPReader{packets: make(chan rtp.Packet, 3)}
		jb := NewRTPJitterBuffer(reader, RTPJitterBufferOptions{
			PacketDuration: time.Millisecond,
			DelayPackets:   2,
			MaxPackets:     4,
		})

		reader.packets <- rtpPacket(1234, 0)
		reader.packets <- rtpPacket(1234, 1)
		reader.packets <- rtpPacket(1234, 2)
		require.Equal(t, uint16(0), readJitterSeq(t, jb))
		for seq := uint16(3); seq < 100; seq++ {
			reader.packets <- rtpPacket(1234, seq)
			require.Equal(t, seq-2, readJitterSeq(t, jb))
		}
		require.Equal(t, uint16(98), readJitterSeq(t, jb))
		require.Equal(t, uint16(99), readJitterSeq(t, jb))
		close(reader.packets)
		requireJitterEOF(t, jb)
	})

	t.Run("close", func(t *testing.T) {
		reader := newChanRTPReader()
		jb := NewRTPJitterBuffer(reader, RTPJitterBufferOptions{})
		result := make(chan error, 1)
		go func() {
			var pkt rtp.Packet
			_, err := jb.ReadRTP(make([]byte, RTPBufSize), &pkt)
			result <- err
		}()

		require.NoError(t, jb.Close())
		require.NoError(t, jb.Close())
		require.ErrorIs(t, <-result, io.ErrClosedPipe)
		close(reader.packets)
	})
}

type benchmarkRTPReader struct {
	raw  []byte
	next chan uint16
}

func newBenchmarkRTPReader() *benchmarkRTPReader {
	raw, err := rtpPacket(1234, 0).Marshal()
	if err != nil {
		panic(err)
	}
	return &benchmarkRTPReader{
		raw:  raw,
		next: make(chan uint16, 3),
	}
}

func (r *benchmarkRTPReader) ReadRTP(buf []byte, p *rtp.Packet) (int, error) {
	seq, ok := <-r.next
	if !ok {
		return 0, io.EOF
	}
	n := copy(buf, r.raw)
	binary.BigEndian.PutUint16(buf[2:4], seq)
	return n, RTPUnmarshal(buf[:n], p)
}

func BenchmarkRTPJitterBuffer(b *testing.B) {
	reader := newBenchmarkRTPReader()
	jb := NewRTPJitterBuffer(reader, RTPJitterBufferOptions{
		PacketDuration: 10 * time.Microsecond,
		DelayPackets:   2,
		MaxPackets:     8,
	})
	b.Cleanup(func() {
		_ = jb.Close()
		close(reader.next)
	})

	buf := make([]byte, RTPBufSize)
	var pkt rtp.Packet
	reader.next <- 0
	reader.next <- 1
	if _, err := jb.ReadRTP(buf, &pkt); err != nil {
		b.Fatal(err)
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		reader.next <- uint16(i + 2)
		if _, err := jb.ReadRTP(buf, &pkt); err != nil {
			b.Fatal(err)
		}
	}
}

func newTestRTPJitterBuffer(t *testing.T, packets []rtp.Packet) *RTPJitterBuffer {
	t.Helper()

	return NewRTPJitterBuffer(&sliceRTPReader{packets: packets}, RTPJitterBufferOptions{
		PacketDuration: time.Millisecond,
		DelayPackets:   2,
		MaxPackets:     8,
	})
}

func readJitterSeq(t *testing.T, jb *RTPJitterBuffer) uint16 {
	t.Helper()

	buf := make([]byte, RTPBufSize)
	var pkt rtp.Packet
	_, err := jb.ReadRTP(buf, &pkt)
	require.NoError(t, err)
	return pkt.SequenceNumber
}

func requireJitterEOF(t *testing.T, jb *RTPJitterBuffer) {
	t.Helper()

	buf := make([]byte, RTPBufSize)
	var pkt rtp.Packet
	_, err := jb.ReadRTP(buf, &pkt)
	require.ErrorIs(t, err, io.EOF)
}

func rtpPackets(ssrc uint32, seqs ...uint16) []rtp.Packet {
	packets := make([]rtp.Packet, 0, len(seqs))
	for _, seq := range seqs {
		packets = append(packets, rtpPacket(ssrc, seq))
	}
	return packets
}

func rtpPacket(ssrc uint32, seq uint16) rtp.Packet {
	return rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    8,
			SequenceNumber: seq,
			Timestamp:      uint32(seq) * 160,
			SSRC:           ssrc,
		},
		Payload: []byte{byte(seq), byte(seq >> 8)},
	}
}
