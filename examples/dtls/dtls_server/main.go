package main

import (
	"context"
	"crypto/tls"
	"log/slog"
	"os"
	"os/signal"

	"github.com/emiago/diago"
	"github.com/emiago/diago/examples"
	"github.com/emiago/diago/media"
	"github.com/emiago/diago/testdata"
	"github.com/emiago/sipgo"
)

func main() {
	// TODO: USE TLS as transport for more correct test
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	examples.SetupLogger()

	ua, _ := sipgo.NewUA()
	defer ua.Close()

	dg := diago.NewDiago(ua,
		diago.WithTransport(
			diago.Transport{
				ID:        "tcp",
				Transport: "tcp",
				BindHost:  "127.0.0.1",
				BindPort:  16443,
				MediaSRTP: 2, // This enables SRTP DTLS
				MediaDTLSConf: media.DTLSConfig{
					Certificates:     []tls.Certificate{testdata.ServerCertificate()},
					ServerClientAuth: media.ServerClientAuthNoCert,
				},
			},
		),
		diago.WithMediaConfig(
			diago.MediaConfig{
				Codecs: []media.Codec{media.CodecAudioUlaw, media.CodecAudioAlaw},
			},
		))

	err := dg.Serve(ctx, func(d *diago.DialogServerSession) {
		d.Trying()
		if err := d.Answer(); err != nil {
			panic(err)
		}

		slog.Info("Starting echo")
		err := d.Echo()
		slog.Info("Echo finished with", "error", err)

	})
	if err != nil {
		slog.Error("serve stopped", "error", err)
	}
}
