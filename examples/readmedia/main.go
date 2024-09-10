// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package main

import (
	"context"
	"errors"
	"io"
	"os"
	"os/signal"
	"time"

	"github.com/emiago/diago"
	"github.com/emiago/diago/audio"
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

	decoder, err := audio.NewPCMDecoder(m.Codec.PayloadType, audioReader)
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
			log.Info().Uint8("PayloadType", pkt.PayloadType).Int("pkts", pktsCount).Msg("Received packets")
		}
		pktsCount++
	}
}
