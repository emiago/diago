// SPDX-License-Identifier: MPL-2.0
// Copyright (C) 2024 Emir Aganovic

// Copyright (C) 2024 Emir Aganovic

// Copyright (C) 2024 Emir Aganovic

package diago

import (
	"bytes"
	"io"
	"testing"

	"github.com/emiago/diago/media"
	"github.com/stretchr/testify/require"
)

type fileBuffer struct {
	// *bytes.Buffer
	seekIndex int
	total     int
	realBuf   []byte
}

func NewFileBuffer(b []byte) *fileBuffer {
	return &fileBuffer{
		realBuf: b,
	}
}

func (b *fileBuffer) Write(p []byte) (int, error) {
	// fmt.Println("Writing ", p)
	n := copy(b.realBuf[b.seekIndex:], p)
	b.seekIndex += n
	return n, nil
}

func (b *fileBuffer) Seek(a int64, whence int) (int64, error) {
	// Wehn
	b.total = max(b.seekIndex, b.total)
	b.seekIndex = int(a)
	return 0, nil
}

func (b *fileBuffer) Bytes() []byte {
	return b.realBuf[:b.total]
}

func TestRecording(t *testing.T) {
	RecordingFlushSize = 22
	incommingStream := bytes.Repeat([]byte{1}, 10)
	outgoingStream := bytes.Repeat([]byte{2}, 10)
	wavBytes := make([]byte, 1000)
	wavFile := NewFileBuffer(wavBytes)

	rec, err := NewRecordingWav(media.CodecAudioUlaw, media.CodecAudioUlaw,
		bytes.NewBuffer(incommingStream), bytes.NewBuffer(outgoingStream), wavFile)
	require.NoError(t, err)

	in, err := io.ReadAll(rec)
	require.NoError(t, err)
	require.Equal(t, in, incommingStream)

	n, err := rec.Write(outgoingStream)
	require.NoError(t, err)
	require.Equal(t, n, len(outgoingStream))
	require.Equal(t, rec.MonitorWriter.(*bytes.Buffer).Bytes(), outgoingStream)

	err = rec.Close()
	require.NoError(t, err)

	wavBytes = wavFile.Bytes()
	t.Log(string(wavBytes), wavBytes[44:], len(wavBytes))
}
