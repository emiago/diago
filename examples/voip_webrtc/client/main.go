// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2026, Emir Aganovic

package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"

	"github.com/emiago/diago"
	"github.com/emiago/diago/examples"
	voipwebrtc "github.com/emiago/diago/examples/voip_webrtc"
	"github.com/emiago/diago/media"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/pion/ice/v4"
)

const recordingPath = "/tmp/diago-voip-webrtc-client.wav"

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	examples.SetupLogger()
	if err := run(ctx); err != nil {
		slog.Error("client stopped", "error", err)
	}
}

func run(ctx context.Context) error {
	ua, err := sipgo.NewUA(sipgo.WithUserAgent("diago-voip-webrtc-client"))
	if err != nil {
		return err
	}
	defer ua.Close()
	client := diago.NewDiago(ua, diago.WithTransport(diago.Transport{
		Transport: "tcp",
		BindHost:  "127.0.0.1",
		BindPort:  0,
	}))
	dialog, err := client.NewDialog(sip.Uri{
		User: "playback",
		Host: "127.0.0.1",
		Port: 15060,
	}, diago.NewDialogOptions{Transport: "tcp"})
	if err != nil {
		return err
	}
	defer dialog.Close()
	med, err := dialog.InviteVoipWebrtc(ctx, diago.InviteVoipWebrtcOptions{
		WebrtcConfig: media.MediaSessionWebrtcConfig{
			NetworkTypes:    []ice.NetworkType{ice.NetworkTypeUDP4},
			IncludeLoopback: true,
		},
	})
	if err != nil {
		return err
	}
	defer med.Close()

	slog.Info("playing and recording", "recording", recordingPath)
	mediaErr := voipwebrtc.PlaybackAndRecord(med, recordingPath)
	hangupErr := dialog.Hangup(context.Background())
	return errors.Join(mediaErr, hangupErr)
}
