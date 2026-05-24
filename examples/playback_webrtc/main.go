// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package main

import (
	"bufio"
	"context"
	"log/slog"
	"os"
	"os/signal"

	"github.com/emiago/diago"
	"github.com/emiago/diago/examples"
	"github.com/emiago/diago/testdata"
	"github.com/emiago/sipgo"
)

// Dial this app with
// gophone dial -media=speaker "sip:123@127.0.0.1"

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
	tu := diago.NewDiago(ua,
		diago.WithTransport(diago.Transport{
			Transport: "ws",
			BindHost:  "127.0.0.1",
			BindPort:  15060,
		}),
	)

	return tu.Serve(ctx, func(inDialog *diago.DialogServerSession) {
		slog.Info("New dialog request", "id", inDialog.ID)
		defer slog.Info("Dialog finished", "id", inDialog.ID)
		if err := Playback(inDialog); err != nil {
			slog.Error("Failed to play", "error", err)
		}
	})
}

func Playback(inDialog *diago.DialogServerSession) error {
	inDialog.Trying()  // Progress -> 100 Trying
	inDialog.Ringing() // Ringing -> 180 Response
	med, err := inDialog.AnswerWebrtc(diago.AnswerWebrtcOptions{})
	if err != nil {
		return err
	} // Answer -> 200 Response

	playfile, _ := testdata.OpenFile("demo-echodone.wav")
	slog.Info("Playing a file", "file", "demo-echodone.wav")

	pb, err := med.PlaybackCreate()
	if err != nil {
		return err
	}

	fileReader := bufio.NewReaderSize(playfile, 64*1024)
	_, err = pb.Play(fileReader, "audio/wav")
	return err
}
