// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2026, Emir Aganovic

package voipwebrtc

import (
	"errors"
	"io"
	"net"
	"os"
	"time"

	"github.com/emiago/diago"
	"github.com/emiago/diago/media"
	"github.com/emiago/diago/testdata"
)

// PlaybackAndRecord plays the bundled WAV file, records received and sent
// audio to a stereo WAV, and keeps draining incoming RTP during playback.
func PlaybackAndRecord(med *diago.DialogVoipWebrtc, recordingPath string) error {
	wavFile, err := os.OpenFile(recordingPath, os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0o644)
	if err != nil {
		return err
	}
	defer wavFile.Close()

	recording, err := med.AudioStereoRecordingCreate(wavFile)
	if err != nil {
		return err
	}
	med.SetAudioReader(recording.AudioReader())
	med.SetAudioWriter(recording.AudioWriter())

	readDone := make(chan error, 1)
	go func() {
		_, copyErr := media.Copy(recording.AudioReader(), io.Discard)
		readDone <- copyErr
	}()

	playFile, err := testdata.OpenFile("demo-echodone.wav")
	if err != nil {
		_ = med.MediaSession().StopRTP(1, time.Millisecond)
		<-readDone
		return errors.Join(err, recording.Close())
	}
	playback, err := med.PlaybackCreate()
	if err == nil {
		_, err = playback.Play(playFile, "audio/wav")
	}
	_ = playFile.Close()

	// Allow the peer's last packets to arrive, then unblock the recording read.
	_ = med.MediaSession().StopRTP(1, 250*time.Millisecond)
	readErr := <-readDone
	var netErr net.Error
	if errors.As(readErr, &netErr) && netErr.Timeout() {
		readErr = nil
	}
	if errors.Is(readErr, io.EOF) || errors.Is(readErr, net.ErrClosed) {
		readErr = nil
	}
	return errors.Join(err, readErr, recording.Close())
}
