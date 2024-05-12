package audio

import (
	"io"
	"sync/atomic"
)

/*
	Playback control should provide functionality like Mute Unmute over audio.
*/

type PlaybackControl struct {
	Reader io.Reader // MUST be set if usede as reader
	Writer io.Writer // Must be set if used as writer

	muted atomic.Bool
	stop  atomic.Bool
}

func (p *PlaybackControl) Init() {
}

func (c *PlaybackControl) Read(b []byte) (n int, err error) {
	n, err = c.Reader.Read(b)
	if err != nil {
		return n, err
	}

	if c.stop.Load() {
		return 0, io.EOF
	}

	if c.muted.Load() {
		for i := range b[:n] {
			b[i] = 0
		}
	}

	return n, err
}

func (c *PlaybackControl) Write(b []byte) (n int, err error) {
	if c.stop.Load() {
		return 0, io.EOF
	}

	if c.muted.Load() {
		for i := range b {
			b[i] = 0
		}
	}

	return c.Writer.Write(b)
}

func (c *PlaybackControl) Mute(mute bool) {
	c.muted.Store(mute)
}

// Stop will stop reader/writer and return io.Eof
func (c *PlaybackControl) Stop() {
	c.stop.Store(true)
}
