// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2026, Emir Aganovic

package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"

	"github.com/emiago/diago"
	"github.com/emiago/diago/examples"
	voipwebrtc "github.com/emiago/diago/examples/voip_webrtc"
	"github.com/emiago/diago/media"
	"github.com/emiago/sipgo"
	"github.com/pion/ice/v4"
)

const recordingPath = "/tmp/diago-voip-webrtc-server.wav"

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	examples.SetupLogger()
	if err := run(ctx); err != nil {
		slog.Error("server stopped", "error", err)
	}
}

func run(ctx context.Context) error {
	ua, err := sipgo.NewUA(sipgo.WithUserAgent("diago-voip-webrtc-server"))
	if err != nil {
		return err
	}
	defer ua.Close()
	server := diago.NewDiago(ua, diago.WithTransport(diago.Transport{
		Transport: "tcp",
		BindHost:  "127.0.0.1",
		BindPort:  15060,
	}))
	return server.Serve(ctx, func(dialog *diago.DialogServerSession) {
		dialog.Trying()
		dialog.Ringing()
		med, answerErr := dialog.AnswerVoipWebrtc(diago.AnswerVoipWebrtcOptions{
			WebrtcConfig: media.MediaSessionWebrtcConfig{
				NetworkTypes:    []ice.NetworkType{ice.NetworkTypeUDP4},
				IncludeLoopback: true,
			},
		})
		if answerErr != nil {
			slog.Error("failed to answer", "error", answerErr)
			return
		}
		defer med.Close()
		slog.Info("playing and recording", "recording", recordingPath)
		if mediaErr := voipwebrtc.PlaybackAndRecord(med, recordingPath); mediaErr != nil {
			slog.Error("media failed", "error", mediaErr)
		}
		<-dialog.Context().Done()
	})
}
