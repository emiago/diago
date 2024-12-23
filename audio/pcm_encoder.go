// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package audio

import (
	"fmt"
	"io"
	"sync"

	"github.com/emiago/diago/media"
	"github.com/zaf/g711"
	"gopkg.in/hraban/opus.v2"
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
	FORMAT_TYPE_OPUS = 96
)

var (
	decoderBufPool = sync.Pool{
		New: func() any {
			return make([]byte, 160)
		},
	}
)

type PCMDecoder struct {
	codec   uint8
	Decoder func(encoded []byte) (lpcm []byte)
	// DecoderTo Must know size in advance!
	DecoderTo func(lpcm []byte, encoded []byte) (int, error)
}

// PCM decoder is streamer implementing io.Reader. It reads from underhood reader and returns decoded
// codec data
func NewPCMDecoder(codec uint8) (*PCMDecoder, error) {
	dec := &PCMDecoder{
		codec: codec,
	}
	switch codec {
	case FORMAT_TYPE_ULAW:
		dec.Decoder = g711.DecodeUlaw // returns 16bit LPCM
		dec.DecoderTo = DecodeUlawTo
	case FORMAT_TYPE_ALAW:
		dec.Decoder = g711.DecodeAlaw // returns 16bit LPCM
		dec.DecoderTo = DecodeAlawTo
	case FORMAT_TYPE_OPUS:
		// opusDec, err := opus.NewDecoder(48000, 2)
		numChannels := 2
		opusDec, err := opus.NewDecoder(48000, numChannels)
		if err != nil {
			return nil, fmt.Errorf("failed to create opus decoder: %w", err)
		}

		opusWrap := OpusDecoder{
			Decoder:     opusDec,
			pcmInt16:    make([]int16, 48000*0.02*numChannels), // 20ms= 960 samples/1920 stereo
			numChannels: numChannels,
		}
		dec.Decoder = opusWrap.Decode
		dec.DecoderTo = opusWrap.DecodeTo
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
	limit := media.RTPBufSize
	// TODO this limiting is normally not needed as 20ms of alaw is 160 bytes
	// if d.codec == FORMAT_TYPE_ALAW || d.codec == FORMAT_TYPE_ULAW {
	// 	// Formats are uncompressed and size of output can be predicted
	// 	// This avoids any buffering unread, and leaves buffering on system
	// 	limit = min(len(b)/2, media.RTPBufSize)
	// }

	// Check do we have some buffered decoding.
	// This is workarround for now where we send nil data
	// to tell decoder to return any unread we just pass no data to decode
	n, err = d.DecoderTo(b, nil)
	if err != nil {
		return 0, err
	}

	if n > 0 {
		return n, nil
	}

	n, err = d.Source.Read(d.buf[:limit])
	if err != nil {
		return n, err
	}

	encoded := d.buf[:n]
	n, err = d.DecoderTo(b, encoded)
	if err != nil {
		return 0, err
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
	EncoderTo func(encoded []byte, lpcm []byte) (int, error)
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

	case FORMAT_TYPE_OPUS:
		// TODO handle mono
		numChannels := 2

		opusEnc, err := opus.NewEncoder(48000, numChannels, opus.AppVoIP)
		if err != nil {
			return nil, fmt.Errorf("failed to create opus decoder: %w", err)
		}
		opusWrap := OpusEncoder{Encoder: opusEnc, pcmInt16: make([]int16, 48000*0.02*numChannels), numChannels: numChannels} // 960 samples
		dec.EncoderTo = opusWrap.EncodeTo
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

func (d *PCMEncoderWriter) Write(lpcm []byte) (int, error) {
	if d.buf == nil {
		// We expect constant rate
		// d.buf = make([]byte, len(lpcm)/2)
		d.buf = make([]byte, media.RTPBufSize)
	}
	// If encoder can not fit our network buffer it will error
	// TODO we may want this to be configurable for some other applications
	n, err := d.EncoderTo(d.buf, lpcm)
	if err != nil {
		return n, err
	}
	encoded := d.buf[:n]
	// fmt.Println("Writing lpcm, encoded", len(lpcm), len(encoded))

	nn, err := d.Destination.Write(encoded)
	if err != nil {
		return nn, err
	}
	if nn != len(encoded) {
		return 0, io.ErrShortWrite
	}

	return len(lpcm), nil

	// ind := 0
	// nn := 0
	// double := len(d.buf) * 2
	// for nn < len(lpcm) {
	// 	max := min(double, len(lpcm[ind:]))
	// 	// EncoderTo avoids allocation
	// 	n, err := d.EncoderTo(d.buf, lpcm[ind:ind+max])
	// 	if err != nil {
	// 		return 0, err
	// 	}
	// 	ind = max

	// 	encoded := d.buf[:n]
	// 	n, err = d.Destination.Write(encoded)
	// 	if err != nil {
	// 		return nn, err
	// 	}
	// 	if n < len(encoded) {
	// 		return nn, io.ErrShortWrite
	// 	}

	// 	nn += max
	// }
	// return nn, nil
}
