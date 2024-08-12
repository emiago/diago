// SPDX-License-Identifier: BSD-2-Clause
// Copyright (C) 2024 Emir Aganovic

package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"time"

	"github.com/emiago/diago"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// Have receiver running:
// gophone answer -l "127.0.0.1:5090"
//
// Run app:
// go run . sip:uas@127.0.0.1:5090
//
// Run dialer:
// gophone dial sip:bob@127.0.0.1

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

	flag.Parse()
	recipientUri := flag.Arg(0)

	err = start(ctx, recipientUri)
	if err != nil {
		log.Fatal().Err(err).Msg("PBX finished with error")
	}
}

func start(ctx context.Context, recipientUri string) error {
	// Setup our main transaction user
	ua, _ := sipgo.NewUA()
	d := diago.NewDiago(ua)

	recipient := sip.Uri{}
	if err := sip.ParseUri(recipientUri, &recipient); err != nil {
		return err
	}

	return d.Serve(ctx, func(inDialog *diago.DialogServerSession) {
		BridgeCall(d, inDialog, recipient)
	})
}

func BridgeCall(d *diago.Diago, inDialog *diago.DialogServerSession, recipient sip.Uri) error {
	inDialog.Progress() // Progress -> 100 Trying
	inDialog.Ringing()  // Ringing -> 180 Response
	inDialog.Answer()   // Answer -> 200 Response

	inCtx := inDialog.Context()
	ctx, cancel := context.WithTimeout(inCtx, 5*time.Second)
	defer cancel()

	bridge := diago.NewBridge()
	// Now answer our in dialog
	inDialog.Answer()
	if err := bridge.AddDialogSession(inDialog); err != nil {
		return err
	}

	outDialog, err := d.InviteBridge(ctx, recipient, &bridge, sipgo.AnswerOptions{})
	if err != nil {
		return err
	}
	defer outDialog.Close()
	outCtx := outDialog.Context()

	defer inDialog.Hangup(inCtx)
	defer outDialog.Hangup(outCtx)

	// You can even easily detect who hangups
	select {
	case <-inCtx.Done():
	case <-outCtx.Done():
	}
	return nil
}
