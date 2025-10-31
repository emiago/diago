// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"context"
	"net"
	"sync"
	"time"

	"github.com/emiago/diago/audio"
	"github.com/emiago/diago/media"
)

// AudioRingtone is playback for ringtone
type AudioRingtone struct {
	writer       *audio.PCMEncoderWriter
	ringtone     []byte
	sampleSize   int
	mediaSession *media.MediaSession
}

func (a *AudioRingtone) PlayBackground() (func() error, error) {
	if err := a.mediaSession.StartRTP(1); err != nil {
		return nil, err
	}

	ctx, cancel := context.WithCancel(context.Background())

	wg := sync.WaitGroup{}
	wg.Add(1)
	var playErr error
	go func() {
		defer wg.Done()
		playErr = a.play(ctx)
	}()

	return func() error {
		cancel()

		if err := a.mediaSession.StopRTP(2, 0); err != nil {
			return err
		}
		wg.Wait()

		// enable RTP again
		if err := a.mediaSession.StartRTP(2); err != nil {
			return err
		}

		if e, ok := playErr.(net.Error); ok && e.Timeout() {
			return nil
		}

		return playErr
	}, nil
}

func (a *AudioRingtone) Play(ctx context.Context) error {
	return a.play(ctx)
}

func (a *AudioRingtone) play(timerCtx context.Context) error {
	t := time.NewTimer(0)
	for {
		_, err := media.WriteAll(a.writer, a.ringtone, a.sampleSize)
		if err != nil {
			return err
		}

		t.Reset(4 * time.Second)
		select {
		case <-t.C:
		case <-timerCtx.Done():
			return timerCtx.Err()
		}
	}
}
