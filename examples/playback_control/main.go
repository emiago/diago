// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"time"

	"github.com/emiago/diago"
	"github.com/emiago/diago/examples"
	"github.com/emiago/diago/testdata"
	"github.com/emiago/sipgo"
)

// Dial this app with
// gophone dial -media=audio "sip:123@127.0.0.1"

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	examples.SetupLogger()

	err := start(ctx)
	if err != nil {
		slog.Error("PBX finished with error", "error", err)
	}
}

func start(ctx context.Context) error {
	// Setup our main transaction user
	ua, _ := sipgo.NewUA()
	tu := diago.NewDiago(ua)

	return tu.Serve(ctx, func(inDialog *diago.DialogServerSession) {
		slog.Info("New dialog request", "id", inDialog.ID)
		defer slog.Info("Dialog finished", "id", inDialog.ID)

		if err := Playback(inDialog); err != nil {
			slog.Error("Playback finished with error", "error", err)
		}
	})
}

func Playback(inDialog *diago.DialogServerSession) error {
	inDialog.Progress() // Progress -> 100 Trying
	inDialog.Ringing()  // Ringing -> 180 Response

	playfile, _ := testdata.OpenFile("demo-echotest.wav")
	defer playfile.Close()

	slog.Info("Playing a file", "file", "demo-echotest.wav")

	inDialog.Answer() // Answer -> 200 Response

	pb, err := inDialog.PlaybackControlCreate()
	if err != nil {
		return err
	}

	playFinished := make(chan error)
	go func() {
		_, err = pb.Play(playfile, "audio/wav")
		playFinished <- err
	}()

	t1 := time.After(3 * time.Second)
	t2 := time.After(6 * time.Second)
	t3 := time.After(10 * time.Second)
	for {
		select {
		case <-t1:
			pb.Mute(true)
			slog.Info("Audio muted")
		case <-t2:
			pb.Mute(false)
			slog.Info("Audio unmuted")
		case <-t3:
			pb.Stop()
			slog.Info("Audio stopped")
		case err := <-playFinished:
			slog.Info("Play finished", "error", err)
			return nil
		case <-inDialog.Context().Done():
			slog.Info("Call hanguped", "error", inDialog.Context().Err())
			return nil
		}
	}
}
