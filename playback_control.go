// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"io"
	"sync/atomic"
)

type AudioPlaybackControl struct {
	AudioPlayback

	control *audioControl
}

func (p *AudioPlaybackControl) Mute(mute bool) {
	p.control.Mute(mute)
}

func (p *AudioPlaybackControl) Stop() {
	p.control.Stop()
}

/*
	Playback control should provide functionality like Mute Unmute over audio.
*/

type audioControl struct {
	Reader io.Reader // MUST be set if usede as reader
	Writer io.Writer // Must be set if used as writer

	muted atomic.Bool
	stop  atomic.Bool
}

func (c *audioControl) Read(b []byte) (n int, err error) {
	if c.stop.Load() {
		return 0, io.EOF
	}

	n, err = c.Reader.Read(b)
	if err != nil {
		return n, err
	}

	if c.muted.Load() {
		for i := range b[:n] {
			b[i] = 0
		}
	}

	return n, err
}

func (c *audioControl) Write(b []byte) (n int, err error) {
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

func (c *audioControl) Mute(mute bool) {
	c.muted.Store(mute)
}

// Stop will stop reader/writer and return io.Eof
func (c *audioControl) Stop() {
	c.stop.Store(true)
}
