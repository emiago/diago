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
	"time"

	"github.com/emiago/diago"
	"github.com/emiago/diago/audio"
	"github.com/emiago/diago/examples"
	"github.com/emiago/diago/media"
	"github.com/emiago/sipgo"
)

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
		ReadMedia(inDialog)
	})
}

func ReadMedia(inDialog *diago.DialogServerSession) error {
	inDialog.Progress() // Progress -> 100 Trying
	inDialog.Ringing()  // Ringing -> 180 Response
	inDialog.Answer()   // Answqer -> 200 Response

	lastPrint := time.Now()
	pktsCount := 0
	buf := make([]byte, media.RTPBufSize)

	// After answer we can access audio reader and read props
	m := diago.MediaProps{}
	audioReader, _ := inDialog.AudioReader(
		diago.WithAudioReaderMediaProps(&m),
	)

	decoder, err := audio.NewPCMDecoderReader(m.Codec.PayloadType, audioReader)
	if err != nil {
		return err
	}
	for {
		_, err := decoder.Read(buf)
		if err != nil {
			if errors.Is(err, io.EOF) {
				return nil
			}
			return err
		}

		pkt := inDialog.RTPPacketReader.PacketHeader
		if time.Since(lastPrint) > 3*time.Second {
			lastPrint = time.Now()
			slog.Info("Received packets", "PayloadType", pkt.PayloadType, "pkts", pktsCount)
		}
		pktsCount++
	}
}
