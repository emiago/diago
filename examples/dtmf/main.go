// SPDX-License-Identifier: MPL-2.0
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

// Dial this app with
// gophone dial -media=speaker "sip:123@127.0.0.1"

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	lev, err := zerolog.ParseLevel(os.Getenv("LOG_LEVEL"))
	if err != nil || lev == zerolog.NoLevel {
		lev = zerolog.InfoLevel
	}

	zerolog.TimeFieldFormat = zerolog.TimeFormatUnixMicro
	log.Logger = zerolog.New(zerolog.ConsoleWriter{
		Out:        os.Stdout,
		TimeFormat: time.StampMicro,
	}).With().Timestamp().Logger().Level(lev)

	media.RTPDebug = true

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
		ReadDTMF(inDialog)
	})
}

func ReadDTMF(inDialog *diago.DialogServerSession) error {
	inDialog.Progress() // Progress -> 100 Trying
	inDialog.Ringing()  // Ringing -> 180 Response

	// dtmfWriter := diago.DTMFWriter{}
	dtmfReader := diago.DTMFReader{}
	inDialog.AnswerOptions(
		// diago.WithMediaDTMFWriter(&dtmfWriter),
		diago.WithMediaDTMFReader(&dtmfReader),
	)
	log.Info().Msg("Reading DTMF")
	dtmfReader.OnDTMF(func(dtmf rune) {
		log.Info().Str("dtmf", string(dtmf)).Msg("Received DTMF")
	})

	log.Info().Msg("Listening on media to have DTMF read")
	return inDialog.Listen()
}
