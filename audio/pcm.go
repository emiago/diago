package audio

import (
	"fmt"
	"io"
	"sync"
	"sync/atomic"

	"github.com/zaf/g711"
)

const (
	// ITU-T G.711.0 codec supports frame lengths of 40, 80, 160, 240 and 320 samples per frame.
	FrameSize  = 3200
	ReadBuffer = 160

	FORMAT_TYPE_ULAW = 0
	FORMAT_TYPE_ALAW = 8
)

type PCMDecoder struct {
	Source   io.Reader
	Decoder  func(encoded []byte) (lpcm []byte)
	buf      []byte
	lastLPCM []byte
	unread   int
}

// PCM decoder is streamer implementing io.Reader. It reads from underhood reader and returns decoded
// codec data
func NewPCMDecoder(codec int, reader io.Reader) (*PCMDecoder, error) {
	var decoder func(lpcm []byte) []byte
	switch codec {
	case FORMAT_TYPE_ULAW:
		decoder = g711.DecodeUlaw // returns 16bit LPCM
	case FORMAT_TYPE_ALAW:
		decoder = g711.DecodeAlaw // returns 16bit LPCM
	// case FORMAT_TYPE_PCM:
	// 	decoder = func(lpcm []byte) []byte { return lpcm }
	default:
		return nil, fmt.Errorf("not supported codec %d", codec)
	}

	dec := &PCMDecoder{
		Source:  reader,
		Decoder: decoder,
		buf:     make([]byte, 160), // Read at least 160 samples. Playback starts with 300
	}
	return dec, nil
}

func (d *PCMDecoder) Read(b []byte) (n int, err error) {
	if d.unread > 0 {
		ind := len(d.lastLPCM) - d.unread
		n := copy(b, d.lastLPCM[ind:])
		d.unread -= n
		return n, nil
	}

	n, err = d.Source.Read(d.buf)
	if err != nil {
		return n, err
	}

	// This creates allocation
	lpcm := d.Decoder(d.buf[:n])

	copied := copy(b, lpcm)
	d.unread = len(lpcm) - copied
	d.lastLPCM = lpcm
	// fmt.Printf("Read playback=%d source=%d copied=%d unread=%d \n", len(b), n, copied, d.unread)
	return copied, nil
}

type PCMEncoder struct {
	Destination io.Writer
	Encoder     func(encoded []byte) (lpcm []byte)
}

// PCMEncoder encodes data from pcm to codec and passes to writer
func NewPCMEncoder(codec uint8, writer io.Writer) (*PCMEncoder, error) {
	var encoder func(lpcm []byte) []byte
	switch codec {
	case FORMAT_TYPE_ULAW:
		encoder = g711.EncodeUlaw // returns 16bit LPCM
	case FORMAT_TYPE_ALAW:
		encoder = g711.EncodeAlaw // returns 16bit LPCM
	// case FORMAT_TYPE_PCM:
	// 	encoder = func(lpcm []byte) []byte { return lpcm }
	default:
		return nil, fmt.Errorf("not supported codec %d", codec)
	}

	dec := &PCMEncoder{
		Destination: writer,
		Encoder:     encoder,
	}
	return dec, nil
}

func (d *PCMEncoder) Write(b []byte) (n int, err error) {
	// TODO avoid this allocation
	lpcm := d.Encoder(b)
	nn := 0
	for nn < len(lpcm) {
		n, err = d.Destination.Write(lpcm)
		if err != nil {
			// return must match n
			return nn * 2, err
		}
		nn += n
	}

	return len(b), nil
}

type PlaybackControl struct {
	mu     sync.Mutex
	source io.Reader

	muted atomic.Bool
}

func NewPlaybackControl(source io.Reader) *PlaybackControl {
	return &PlaybackControl{
		source: source,
	}
}

func (c *PlaybackControl) Read(b []byte) (n int, err error) {
	n, err = c.source.Read(b)
	if err != nil {
		return n, err
	}

	if !c.muted.Load() {
		return
	}

	for i, _ := range b[:n] {
		b[i] = 0
	}
	return n, err
}

func (c *PlaybackControl) Mute(mute bool) {
	c.muted.Store(mute)
}
