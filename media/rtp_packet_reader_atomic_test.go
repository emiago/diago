// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2026, Emir Aganovic

package media

import (
	"testing"

	"github.com/pion/rtp"
)

type atomicTestRTPReader struct{}

func (*atomicTestRTPReader) ReadRTP([]byte, *rtp.Packet) (int, error) {
	return 1, nil
}

type atomicTestOtherRTPReader struct{}

func (*atomicTestOtherRTPReader) ReadRTP([]byte, *rtp.Packet) (int, error) {
	return 2, nil
}

// Updating must support different concrete reader types while audio is read.
func TestRTPPacketReaderConcurrentUpdate(t *testing.T) {
	readers := [2]RTPReader{new(atomicTestRTPReader), new(atomicTestOtherRTPReader)}
	r := NewRTPPacketReader(readers[0], CodecAudioAlaw)
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, RTPBufSize)
		var pkt rtp.Packet
		for i := 0; i < 10000; i++ {
			n, err := r.readRTP(buf, &pkt)
			if err != nil || (n != 1 && n != 2) {
				t.Errorf("read during update: n=%d, err=%v", n, err)
				return
			}
		}
	}()
	for i := 0; i < 10000; i++ {
		r.UpdateReader(readers[i&1])
	}
	<-done
}

// These benchmarks isolate reader access from networking and RTP processing.
func BenchmarkRTPPacketReaderAtomic(b *testing.B) {
	b.Run("Read", func(b *testing.B) {
		r := NewRTPPacketReader(new(atomicTestRTPReader), CodecAudioAlaw)
		buf := make([]byte, RTPBufSize)
		var pkt rtp.Packet
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if n, err := r.readRTP(buf, &pkt); n != 1 || err != nil {
				b.Fatal(n, err)
			}
		}
	})
	b.Run("Update", func(b *testing.B) {
		readers := [2]RTPReader{new(atomicTestRTPReader), new(atomicTestOtherRTPReader)}
		r := NewRTPPacketReader(readers[0], CodecAudioAlaw)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			r.UpdateReader(readers[i&1])
		}
	})
}
