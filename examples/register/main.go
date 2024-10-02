// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	"github.com/emiago/diago"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

// Run app:
// go run . sip:user@myregistrar.com

func main() {
	fUsername := flag.String("username", "", "Digest username")
	fPassword := flag.String("password", "", "Digest password")
	flag.Usage = func() {
		fmt.Fprintf(os.Stderr, "Usage: %s -username <username> -password <pass> sip:123@example.com\n", os.Args[0])
		flag.PrintDefaults()
	}
	flag.Parse()

	// Setup signaling
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	// Setup logger
	lev, err := zerolog.ParseLevel(os.Getenv("LOG_LEVEL"))
	if err != nil || lev == zerolog.NoLevel {
		lev = zerolog.InfoLevel
	}
	zerolog.TimeFieldFormat = zerolog.TimeFormatUnixMicro
	log.Logger = zerolog.New(zerolog.ConsoleWriter{
		Out:        os.Stdout,
		TimeFormat: time.StampMicro,
	}).With().Timestamp().Logger().Level(lev)

	// Have some debugging
	sip.SIPDebug = os.Getenv("SIP_DEBUG") == "true"

	recipientUri := flag.Arg(0)
	if recipientUri == "" {
		flag.Usage()
		return
	}

	err = start(ctx, recipientUri, diago.RegisterOptions{
		Username: *fUsername,
		Password: *fPassword,
	})
	if err != nil {
		log.Fatal().Err(err).Msg("PBX finished with error")
	}
}

func start(ctx context.Context, recipientURI string, regOpts diago.RegisterOptions) error {
	recipient := sip.Uri{}
	if err := sip.ParseUri(recipientURI, &recipient); err != nil {
		return fmt.Errorf("failed to parse register uri: %w", err)
	}

	// Setup our main transaction user
	useragent := regOpts.Username
	if useragent == "" {
		useragent = "change-me"
	}

	ua, _ := sipgo.NewUA(
		sipgo.WithUserAgent(useragent),
		sipgo.WithUserAgentHostname("localhost"),
	)
	defer ua.Close()

	tu := diago.NewDiago(ua, diago.WithTransport(
		diago.Transport{
			Transport: "udp",
			BindHost:  "127.0.0.1",
			BindPort:  15060,
		},
	))

	// Start listening incoming calls
	go func() {
		tu.Serve(ctx, func(inDialog *diago.DialogServerSession) {
			log.Info().Str("id", inDialog.ID).Msg("New dialog request")
			defer log.Info().Str("id", inDialog.ID).Msg("Dialog finished")
		})
	}()

	// Do register or fail on error
	return tu.Register(ctx, recipient, regOpts)
}
