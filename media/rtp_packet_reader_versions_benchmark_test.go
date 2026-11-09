package media

import (
	"testing"

	"github.com/pion/rtp"
)

// Supply valid parsed packets so the same public Read benchmark works before
// and after readRTP was extracted. Network I/O and parsing are excluded.
type versionBenchmarkRTPReader struct {
	seq uint16
}

func (r *versionBenchmarkRTPReader) ReadRTP(buf []byte, pkt *rtp.Packet) (int, error) {
	r.seq++
	pkt.Header = rtp.Header{
		Version:        2,
		PayloadType:    8,
		SequenceNumber: r.seq,
		SSRC:           1,
	}
	pkt.Payload = buf[12:172]
	return 172, nil
}

type versionBenchmarkOtherRTPReader struct {
	versionBenchmarkRTPReader
}

var versionBenchmarkReaderSink RTPReader
var versionBenchmarkPacketReaderSink *RTPPacketReader

func BenchmarkRTPPacketReaderVersions(b *testing.B) {
	b.Run("Reader", func(b *testing.B) {
		r := NewRTPPacketReader(new(versionBenchmarkRTPReader), CodecAudioAlaw)
		var reader RTPReader
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			reader = r.Reader()
		}
		versionBenchmarkReaderSink = reader
	})
	b.Run("Read", func(b *testing.B) {
		r := NewRTPPacketReader(new(versionBenchmarkRTPReader), CodecAudioAlaw)
		buf := make([]byte, RTPBufSize)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if n, err := r.Read(buf); n != 160 || err != nil {
				b.Fatal(n, err)
			}
		}
	})
	b.Run("ManyReaders", func(b *testing.B) {
		const count = 16384
		readers := make([]*RTPPacketReader, count)
		for i := range readers {
			readers[i] = NewRTPPacketReader(new(versionBenchmarkRTPReader), CodecAudioAlaw)
		}
		buf := make([]byte, RTPBufSize)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if n, err := readers[i&(count-1)].Read(buf); n != 160 || err != nil {
				b.Fatal(n, err)
			}
		}
	})
	b.Run("New", func(b *testing.B) {
		reader := new(versionBenchmarkRTPReader)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			versionBenchmarkPacketReaderSink = NewRTPPacketReader(reader, CodecAudioAlaw)
		}
	})
	b.Run("Update", func(b *testing.B) {
		readers := [2]RTPReader{new(versionBenchmarkRTPReader), new(versionBenchmarkOtherRTPReader)}
		r := NewRTPPacketReader(readers[0], CodecAudioAlaw)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			r.UpdateReader(readers[i&1])
		}
	})
}
