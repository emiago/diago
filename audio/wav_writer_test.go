// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package audio

import (
	"bytes"
	"os"
	"testing"

	"github.com/go-audio/riff"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWavWriter(t *testing.T) {
	f, err := os.OpenFile("/tmp/test-waw-writer.wav", os.O_CREATE|os.O_RDWR|os.O_TRUNC, 0755)
	require.NoError(t, err)
	defer f.Close()

	w := NewWavWriter(f)
	n, err := w.Write(bytes.Repeat([]byte{1}, 100))
	require.NoError(t, err)
	require.Equal(t, 100, n)

	f.Seek(0, 0)

	p := riff.New(f)
	err = p.ParseHeaders()
	require.NoError(t, err)

	for {
		chunk, err := p.NextChunk()
		require.NoError(t, err)

		if chunk.ID != riff.FmtID {
			chunk.Drain()
			continue
		}
		err = chunk.DecodeWavHeader(p)
		require.NoError(t, err)
		break
	}

	assert.EqualValues(t, 8000, p.SampleRate)
	assert.EqualValues(t, 100, w.dataSize)
}
