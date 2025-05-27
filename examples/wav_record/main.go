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
	"github.com/emiago/diago/audio"
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
		if err := Record(inDialog); err != nil {
			slog.Error("Record finished with error", "error", err)
		}
	})
}

func Record(inDialog *diago.DialogServerSession) error {
	inDialog.Progress() // Progress -> 100 Trying
	inDialog.Ringing()  // Ringing -> 180 Response
	if err := inDialog.Answer(); err != nil {
		return err
	} // Answer -> 200 Response

	mpropsR := diago.MediaProps{}
	mpropsW := diago.MediaProps{}
	ar, _ := inDialog.AudioReader(diago.WithAudioReaderMediaProps(&mpropsR))
	aw, _ := inDialog.AudioWriter(diago.WithAudioWriterMediaProps(&mpropsW))
	if mpropsR.Codec != mpropsW.Codec {
		// We can not use stereo monitor.
		if mpropsR.Codec.SampleDur != mpropsW.Codec.SampleDur &&
			mpropsR.Codec.SampleRate != mpropsW.Codec.SampleRate {
			return fmt.Errorf("Codecs do not match. Can not create stereo recording")
		}
	}

	// Create wav file to store recording
	wawFile, err := os.OpenFile("/tmp/diago_record_"+inDialog.InviteRequest.CallID().Value()+".wav", os.O_RDWR|os.O_CREATE, 0755)
	if err != nil {
		return err
	}
	defer wawFile.Close()
	// Now create WavWriter to have Wav Container written
	wawWriter := audio.NewWavWriter(wawFile)
	defer wawWriter.Close() // Must be called for header update

	mon := audio.MonitorPCMStereo{}
	mon.Init(wawWriter, mpropsR.Codec, ar, aw)
	defer mon.Close()

	// Do echo
	_, err = media.Copy(&mon, &mon)
	if errors.Is(err, io.EOF) {
		// Call finished
		return nil
	}
	return err
}
