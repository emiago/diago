// SPDX-License-Identifier: BSD-2-Clause
// Copyright (C) 2024 Emir Aganovic

package main

import (
	"context"
	"os"
	"os/signal"
	"time"

	"github.com/emiago/diago"
	"github.com/emiago/diago/media"
	"github.com/emiago/sipgo"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	lev, err := zerolog.ParseLevel(os.Getenv("LOG_LEVEL"))
	if err != nil || lev == zerolog.NoLevel {
		lev = zerolog.InfoLevel
	}
	log.Logger = zerolog.New(zerolog.ConsoleWriter{
		Out:        os.Stdout,
		TimeFormat: time.StampMicro,
	}).With().Timestamp().Logger().Level(lev)

	err = start(ctx)
	if err != nil {
		log.Fatal().Err(err).Msg("PBX finished with error")
	}
}

func start(ctx context.Context) error {
	// Setup our main transaction user
	ua, _ := sipgo.NewUA()
	tu := diago.NewDiago(ua)

	return tu.Serve(ctx, func(inDialog *diago.DialogServerSession) {
		log.Info().Str("id", inDialog.ID).Msg("New dialog request")
		defer log.Info().Str("id", inDialog.ID).Msg("Dialog finished")
		ReadMedia(inDialog)
	})
}

func ReadMedia(inDialog *diago.DialogServerSession) {
	inDialog.Progress() // Progress -> 100 Trying
	inDialog.Ringing()  // Ringing -> 180 Response
	inDialog.Answer()   // Answqer -> 200 Response

	lastPrint := time.Now()
	pktsCount := 0
	buf := make([]byte, media.RTPBufSize)
	for {
		_, err := inDialog.Media().RTPPacketReader.Read(buf)
		if err != nil {
			return
		}
		pkt := inDialog.Media().RTPPacketReader.PacketHeader

		if time.Since(lastPrint) > 3*time.Second {
			lastPrint = time.Now()
			log.Info().Uint8("PayloadType", pkt.PayloadType).Int("pkts", pktsCount).Msg("Received packets")
		}
		pktsCount++
	}
}
