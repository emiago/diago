// SPDX-License-Identifier: MPL-2.0
// Copyright (C) 2024 Emir Aganovic

package audio

import (
	"fmt"
	"io"

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

type PCMDecoder struct {
	Source   io.Reader
	Writer   io.Writer
	Decoder  func(encoded []byte) (lpcm []byte)
	buf      []byte
	lastLPCM []byte
	unread   int
}

// PCM decoder is streamer implementing io.Reader. It reads from underhood reader and returns decoded
// codec data
func NewPCMDecoder(codec uint8, reader io.Reader) (*PCMDecoder, error) {
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

func NewPCMDecoderReader(codec uint8, reader io.Reader) (*PCMDecoder, error) {
	d, err := NewPCMDecoder(codec, nil)
	if err != nil {
		return nil, err
	}
	d.Source = reader
	return d, nil
}

func NewPCMDecoderWriter(codec uint8, writer io.Writer) (*PCMDecoder, error) {
	d, err := NewPCMDecoder(codec, nil)
	if err != nil {
		return nil, err
	}
	d.Writer = writer
	return d, nil
}

func (d *PCMDecoder) Write(b []byte) (n int, err error) {
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
			return nn, err
		}
		nn += n
	}

	return len(b), nil
}
