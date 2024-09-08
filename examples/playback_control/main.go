// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

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
		lev = zerolog.InfoLevel
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

		if err := Playback(inDialog); err != nil {
			log.Error().Err(err).Msg("Playback finished with error")
		}
	})
}

func Playback(inDialog *diago.DialogServerSession) error {
	inDialog.Progress() // Progress -> 100 Trying
	inDialog.Ringing()  // Ringing -> 180 Response

	playfile, _ := testdata.OpenFile("demo-echotest.wav")
	defer playfile.Close()

	log.Info().Str("file", "demo-echotest.wav").Msg("Playing a file")

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
			log.Info().Msg("Audio muted")
		case <-t2:
			pb.Mute(false)
			log.Info().Msg("Audio unmuted")
		case <-t3:
			pb.Stop()
			log.Info().Msg("Audio stopped")
		case err := <-playFinished:
			log.Info().Err(err).Msg("Play finished")
			return nil
		case <-inDialog.Context().Done():
			log.Info().Err(inDialog.Context().Err()).Msg("Call hanguped")
			return nil
		}
	}
}
