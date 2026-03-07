package media

import (
	"fmt"
	"io"
	"time"
)

type RTPRealTimeReader struct {
	r                 io.Reader
	rtpReader         *RTPPacketReader
	firstReadTime     time.Time
	firstRTPTimestamp uint32
	codec             Codec
}

func NewRTPRealTimeReader(reader io.Reader, rtpReader *RTPPacketReader, codec Codec) *RTPRealTimeReader {
	r := &RTPRealTimeReader{}
	r.Init(reader, rtpReader, codec)
	return r
}

func (r *RTPRealTimeReader) Init(reader io.Reader, rtpReader *RTPPacketReader, codec Codec) {
	if codec.SampleDur == 0 {
		panic("Codec sample dur not defined " + fmt.Sprintf("%+v", codec))
	}

	r.r = reader
	r.rtpReader = rtpReader
	r.codec = codec

}

func (r *RTPRealTimeReader) Reset() {
	r.firstReadTime = time.Time{}
}

func (r *RTPRealTimeReader) Read(b []byte) (int, error) {
	if r.firstReadTime.IsZero() {
		// If no read yet made
		n, err := r.r.Read(b)
		if err != nil {
			return n, err
		}

		r.firstReadTime = time.Now()
		r.firstRTPTimestamp = r.rtpReader.PacketHeader.Timestamp
		return n, err
	}

	for {
		n, err := r.r.Read(b)
		if err != nil {
			return n, err
		}

		rtpHeader := r.rtpReader.PacketHeader
		tsDiff := rtpHeader.Timestamp - r.firstRTPTimestamp
		dur := time.Duration(tsDiff/r.codec.SampleTimestamp()) * r.codec.SampleDur

		if time.Since(r.firstReadTime) > dur+2*r.codec.SampleDur {
			// Skip it
			DefaultLogger().Debug("Skipping non realtime packet", "ssrc", rtpHeader.SSRC, "seq", rtpHeader.SequenceNumber, "ts", rtpHeader.Timestamp, "ts_diff", tsDiff)
			continue
		}
		return n, nil
	}
}
