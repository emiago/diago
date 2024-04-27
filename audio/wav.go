package audio

import (
	"io"

	"github.com/go-audio/riff"
	"github.com/go-audio/wav"
)

func NewWavDecoder(r io.ReadSeeker) *wav.Decoder {
	dec := wav.NewDecoder(r)
	return dec
}

// Decoder handles the decoding of wav files.
type Decoder struct {
	*riff.Parser
}

func NewWavDecoderStreamer(r io.Reader) *Decoder {
	return &Decoder{
		riff.New(r),
	}
}

func (d *Decoder) ReadHeaders() (err error) {
	var chunk *riff.Chunk

	d.ParseHeaders()
	for err == nil {
		chunk, err = d.NextChunk()
		if err != nil {
			break
		}

		if chunk.ID == riff.FmtID {
			chunk.DecodeWavHeader(d.Parser)
			break
		}
	}
	return err
}

// func NewWavDecoderStream(r io.Reader) *wav.Decoder {

// 	dec :=  wav.NewDecoder(r)
// }
