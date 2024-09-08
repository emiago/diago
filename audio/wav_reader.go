// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package audio

import (
	"io"

	"github.com/go-audio/riff"
)

type WavReader struct {
	riff.Parser
	chunkData *riff.Chunk
	DataSize  int
}

func NewWavReader(r io.Reader) *WavReader {
	parser := riff.New(r)
	reader := WavReader{Parser: *parser}
	return &reader
}

// ReadHeaders reads until data chunk
func (r *WavReader) ReadHeaders() error {
	if err := r.readHeaders(); err != nil {
		return err
	}

	return r.readDataChunk()
}

func (r *WavReader) readHeaders() error {
	if err := r.Parser.ParseHeaders(); err != nil {
		return err
	}
	for {
		chunk, err := r.NextChunk()
		if err != nil {
			return err
		}

		if chunk.ID != riff.FmtID {
			chunk.Drain()
			continue
		}
		return chunk.DecodeWavHeader(&r.Parser)
	}
}

func (r *WavReader) readDataChunk() error {
	// if r.Size == 0 {
	// 	r.Parser.ParseHeaders()
	// }

	for {
		chunk, err := r.NextChunk()
		if err != nil {
			return err
		}

		if chunk.ID != riff.DataFormatID {
			chunk.Drain()
			continue
		}
		r.chunkData = chunk
		r.DataSize = chunk.Size
		return nil
	}
}

// Read returns PCM underneath
func (r *WavReader) Read(buf []byte) (n int, err error) {
	if r.chunkData != nil {
		return r.chunkData.Read(buf)
	}

	if err := r.readDataChunk(); err != nil {
		return 0, err
	}
	return r.chunkData.Read(buf)
}
