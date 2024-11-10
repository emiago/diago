// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package audio

import (
	"fmt"
	"io"
	"sync"

	"github.com/emiago/media"
	"github.com/zaf/g711"
)

/*
	This is PCM Decoder and Encoder (translators from VOIP codecs)
	They are io.Reader io.Writter which should wrap RTP Reader Writter and pass to upper PCM player
	It operates on RTP payload and for every ticked sample it does decoding.
	As decoding can add delay for compressed codecs, it may be usefull that upper Reader buffers,
	but for ulaw, alaw codecs this should be no delays

	PCM allows translation to any codec or creating wav files
*/

const (
	// ITU-T G.711.0 codec supports frame lengths of 40, 80, 160, 240 and 320 samples per frame.
	FrameSize  = 3200
	ReadBuffer = 160

	FORMAT_TYPE_ULAW = 0
	FORMAT_TYPE_ALAW = 8
)

var (
	decoderBufPool = sync.Pool{
		New: func() any {
			return make([]byte, 160)
		},
	}
)

type PCMDecoder struct {
	Decoder func(encoded []byte) (lpcm []byte)
	// DecoderTo Must know size in advance!
	DecoderTo func(lpcm []byte, encoded []byte) (int, error)
}

// PCM decoder is streamer implementing io.Reader. It reads from underhood reader and returns decoded
// codec data
func NewPCMDecoder(codec uint8) (*PCMDecoder, error) {
	dec := &PCMDecoder{}
	switch codec {
	case FORMAT_TYPE_ULAW:
		dec.Decoder = g711.DecodeUlaw // returns 16bit LPCM
		dec.DecoderTo = DecodeUlawTo
	case FORMAT_TYPE_ALAW:
		dec.Decoder = g711.DecodeAlaw // returns 16bit LPCM
		dec.DecoderTo = DecodeAlawTo
	// case FORMAT_TYPE_PCM:
	// 	decoder = func(lpcm []byte) []byte { return lpcm }
	default:
		return nil, fmt.Errorf("not supported codec %d", codec)
	}
	return dec, nil
}

type PCMDecoderReader struct {
	PCMDecoder
	Source io.Reader
	buf    []byte
}

func NewPCMDecoderReader(codec uint8, reader io.Reader) (*PCMDecoderReader, error) {
	d, err := NewPCMDecoder(codec)
	if err != nil {
		return nil, err
	}

	return &PCMDecoderReader{
		PCMDecoder: *d,
		Source:     reader,
	}, nil
}

func (d *PCMDecoderReader) Read(b []byte) (n int, err error) {
	if d.buf == nil {
		d.buf = make([]byte, media.RTPBufSize)
	}
	limit := min(len(b)/2, media.RTPBufSize)

	n, err = d.Source.Read(d.buf[:limit])
	if err != nil {
		return n, err
	}

	encoded := d.buf[:n]
	n, err = d.DecoderTo(b, encoded)
	if err != nil {
		return 0, err
	}

	if len(encoded)*2 < n {
		return 0, io.ErrShortBuffer
	}

	return n, nil
}

type PCMDecoderWriter struct {
	PCMDecoder
	Writer io.Writer
}

func NewPCMDecoderWriter(codec uint8, writer io.Writer) (*PCMDecoderWriter, error) {
	d, err := NewPCMDecoder(codec)
	if err != nil {
		return nil, err
	}
	return &PCMDecoderWriter{
		PCMDecoder: *d,
		Writer:     writer,
	}, nil
}

func (d *PCMDecoderWriter) Write(b []byte) (n int, err error) {
	// TODO avoid this allocation
	lpcm := d.Decoder(b)
	nn := 0
	for nn < len(lpcm) {
		n, err = d.Writer.Write(lpcm)
		if err != nil {
			return 0, err
		}
		nn += n
	}

	return len(b), nil
}

type PCMEncoder struct {
	Encoder   func(lpcm []byte) (encoded []byte)
	EncoderTo func(lpcm []byte, encoded []byte) (int, error)
}

// PCMEncoder encodes data from pcm to codec and passes to writer
func NewPCMEncoder(codec uint8) (*PCMEncoder, error) {
	dec := &PCMEncoder{}
	switch codec {
	case FORMAT_TYPE_ULAW:
		dec.Encoder = g711.EncodeUlaw
		dec.EncoderTo = EncodeUlawTo
	case FORMAT_TYPE_ALAW:
		dec.Encoder = g711.EncodeAlaw
		dec.EncoderTo = EncodeAlawTo
	// case FORMAT_TYPE_PCM:
	// 	encoder = func(lpcm []byte) []byte { return lpcm }
	default:
		return nil, fmt.Errorf("not supported codec %d", codec)
	}

	return dec, nil
}

type PCMEncoderWriter struct {
	PCMEncoder
	Destination io.Writer
	buf         []byte
}

func NewPCMEncoderWriter(codec uint8, writer io.Writer) (*PCMEncoderWriter, error) {
	d, err := NewPCMEncoder(codec)
	if err != nil {
		return nil, err
	}
	return &PCMEncoderWriter{
		PCMEncoder:  *d,
		Destination: writer,
	}, nil
}

func (d *PCMEncoderWriter) Write(lpcm []byte) (n int, err error) {
	if d.buf == nil {
		// We expect constant rate
		// TODO can we even remove this allocation
		// d.buf = make([]byte, len(lpcm)/2)
		d.buf = make([]byte, media.RTPBufSize)
	}

	ind := 0
	nn := 0
	double := len(d.buf) * 2
	for nn < len(lpcm) {
		max := min(double, len(lpcm[ind:]))
		// EncoderTo avoids allocation
		n, err := d.EncoderTo(d.buf, lpcm[ind:ind+max])
		if err != nil {
			return 0, err
		}
		ind = max

		encoded := d.buf[:n]
		n, err = d.Destination.Write(encoded)
		if err != nil {
			return nn, err
		}
		if n < len(encoded) {
			return nn, io.ErrShortWrite
		}

		nn += max
	}
	return nn, nil
}
