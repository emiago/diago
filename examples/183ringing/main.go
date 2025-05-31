// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic
package main

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/signal"

	"github.com/emiago/diago"
	"github.com/emiago/diago/examples"
	"github.com/emiago/diago/media"
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
		if err := AnswerEarly(inDialog); err != nil {
			slog.Error("Record finished with error", "error", err)
		}
	})
}

func AnswerEarly(inDialog *diago.DialogServerSession) error {
	inDialog.Trying()  // Progress -> 100 Trying
	inDialog.Ringing() // Ringing -> 180 Response
	if err := inDialog.Answer(); err != nil {
		return err
	} // Answer -> 200 Response

	// Create wav file to store recording
	wawFile, err := os.OpenFile("/tmp/diago_record_"+inDialog.InviteRequest.CallID().Value()+".wav", os.O_RDWR|os.O_CREATE, 0755)
	if err != nil {
		return err
	}
	defer wawFile.Close()

	// Create recording audio pipeline
	rec, err := inDialog.AudioStereoRecordingCreate(wawFile)
	if err != nil {
		return err
	}
	// Must be closed for correct flushing
	defer func() {
		if err := rec.Close(); err != nil {
			slog.Error("Failed to close recording", "error", err)
		}
	}()

	// Do echo with audio reader and writer from recording object
	_, err = media.Copy(rec.AudioReader(), rec.AudioWriter())
	if errors.Is(err, io.EOF) {
		// Call finished
		return nil
	}
	return err
}
