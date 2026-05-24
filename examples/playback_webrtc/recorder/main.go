// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"

	"github.com/emiago/diago"
	"github.com/emiago/diago/examples"
	"github.com/emiago/diago/media"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
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
	tu := diago.NewDiago(ua, diago.WithTransport(
		diago.Transport{
			Transport: "ws",
			BindHost:  "127.0.0.1",
		},
	))

	dialog, err := tu.NewDialog(sip.Uri{User: "123", Host: "127.0.0.1", Port: 15060}, diago.NewDialogOptions{Transport: "ws"})
	if err != nil {
		return err
	}
	defer dialog.Close()

	med, err := dialog.InviteWebrtc(ctx, diago.InviteWebrtcOptions{})
	if err != nil {
		return err
	}
	defer med.Close()

	// ar, err := med.AudioReader()
	// if err != nil {
	// 	return err
	// }
	// record, _ := media.ReadAll(ar, 160)
	// return os.WriteFile("/tmp/playback_record.pcmu", record, 0644)
	fmt.Println("Creating recording ./playback_record.wav")
	wavFile, err := os.OpenFile("./playback_record.wav", os.O_CREATE|os.O_TRUNC|os.O_RDWR, 0644)
	if err != nil {
		return fmt.Errorf("failed to create recording file: %w", err)
	}
	defer wavFile.Close()
	rec, err := med.AudioStereoRecordingCreate(wavFile)
	if err != nil {
		return err
	}
	defer rec.Close()

	_, err = media.Copy(rec.AudioReader(), rec.AudioWriter())
	if errors.Is(err, io.EOF) {
		return nil
	}
	return err
}
