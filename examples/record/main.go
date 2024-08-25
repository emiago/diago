// SPDX-License-Identifier: MPL-2.0
// Copyright (C) 2024 Emir Aganovic

// Copyright (C) 2024 Emir Aganovic

// Copyright (C) 2024 Emir Aganovic

package main

import (
	"context"
	"os"
	"os/signal"
	"time"

	"github.com/emiago/diago"
	"github.com/emiago/diago/testdata"
	"github.com/emiago/sipgo"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// Dial this app with
// gophone dial -media=audio "sip:123@127.0.0.1"

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	lev, err := zerolog.ParseLevel(os.Getenv("LOG_LEVEL"))
	if err != nil || lev == zerolog.NoLevel {
		lev = zerolog.DebugLevel
	}

	zerolog.TimeFieldFormat = zerolog.TimeFormatUnixMicro
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
		Record(inDialog)
	})
}

func Record(inDialog *diago.DialogServerSession) error {
	inDialog.Progress() // Progress -> 100 Trying
	inDialog.Ringing()  // Ringing -> 180 Response

	// Prepare recording file opening before answering
	f, _ := os.OpenFile("/tmp/test-record.wav", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
	defer f.Close()

	rec := diago.NewRecordingWav("test123", f)
	defer func() {
		log.Info().Msg("Record closing")
		if err := rec.Close(); err != nil {
			log.Error().Err(err).Msg("Failed to close recording")
		}
	}()

	inDialog.Answer() // Answer -> 200 Response

	// Now attach session media stream into recording
	if err := rec.Attach(inDialog); err != nil {
		return err
	}

	go func() {
		if err := speak(inDialog); err != nil {
			log.Error().Err(err).Msg("Failed to speak")
		}
	}()

	go inDialog.Listen()

	<-inDialog.Context().Done()
	return nil
}

func speak(inDialog *diago.DialogServerSession) error {
	playfile, _ := testdata.OpenFile("demo-instruct.wav")
	log.Info().Str("file", "demo-instruct.wav").Msg("Playing a file")
	pb, err := inDialog.PlaybackCreate()
	if err != nil {
		return err
	}
	return pb.Play(playfile, "audio/wav")
}
