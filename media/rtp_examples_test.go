package media

import (
	"bytes"
	"fmt"
	"os"

	"github.com/pion/rtp"
)

type rtpBuffer struct {
	buf []rtp.Packet
}

func (b *rtpBuffer) WriteRTP(p *rtp.Packet) error {
	b.buf = append(b.buf, *p)
	return nil
}

// Example on how to generate RTP packets from audio bytes
func Example_audio2RTPGenerator() {
	// Create some audio
	audioAlawBuf := make([]byte, 4*160)
	copy(audioAlawBuf, bytes.Repeat([]byte("0123456789"), CodecAudioAlaw.Samples16()*2/10))

	// Create Packet writer and pass RTP buff
	rtpBuf := &rtpBuffer{}
	rtpGenerator := NewRTPPacketWriter(rtpBuf, CodecAudioAlaw)
	WriteAll(rtpGenerator, audioAlawBuf, 160)

	// Now we have RTP packets ready to use from audio
	for _, p := range rtpBuf.buf {
		fmt.Fprint(os.Stderr, p.String())
	}
	fmt.Println(len(rtpBuf.buf))
	// Output: 4
}
