package audio

import (
	"io"
	"sync"
	"sync/atomic"
)

/*
	Playback control should provide functionality like Mute Unmute over audio.
*/

type PlaybackControl struct {
	mu     sync.Mutex
	reader io.Reader
	writer io.Writer

	muted atomic.Bool
}

func NewPlaybackControl(reader io.Reader, writer io.Writer) *PlaybackControl {
	return &PlaybackControl{
		reader: reader,
		writer: writer,
	}
}

func NewPlaybackControlReader(reader io.Reader) *PlaybackControl {
	return &PlaybackControl{
		reader: reader,
	}
}

func NewPlaybackControlWriter(writer io.Writer) *PlaybackControl {
	return &PlaybackControl{
		writer: writer,
	}
}

func (c *PlaybackControl) Read(b []byte) (n int, err error) {
	n, err = c.reader.Read(b)
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

func (c *PlaybackControl) Write(b []byte) (n int, err error) {
	if !c.muted.Load() {
		return
	}

	for i, _ := range b {
		b[i] = 0
	}

	return c.writer.Write(b)
}

func (c *PlaybackControl) Mute(mute bool) {
	c.muted.Store(mute)
}
