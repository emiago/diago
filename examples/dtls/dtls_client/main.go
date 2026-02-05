package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"time"

	"github.com/emiago/diago"
	"github.com/emiago/diago/audio"
	"github.com/emiago/diago/examples"
	"github.com/emiago/diago/media"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

func main() {
	// TODO: USE TLS as transport for more correct test
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	examples.SetupLogger()

	ua, _ := sipgo.NewUA()
	dg := diago.NewDiago(ua,
		diago.WithTransport(
			diago.Transport{
				ID:            "tcp",
				Transport:     "tcp",
				BindHost:      "127.0.0.1",
				BindPort:      16441,
				MediaSRTP:     2, // USE DTLS
				MediaDTLSConf: media.DTLSConfig{},
			},
		),
		diago.WithMediaConfig(
			diago.MediaConfig{
				Codecs: []media.Codec{media.CodecAudioUlaw, media.CodecAudioAlaw},
			},
		))

	// err = dg.ServeBackground(ctx, func(d *DialogServerSession) {})
	// require.NoError(t, err)

	d, err := dg.Invite(ctx, sip.Uri{User: "11", Host: "127.0.0.1", Port: 16443}, diago.InviteOptions{Transport: "tcp"})
	if err != nil {
		panic(err)
	}
	defer d.Close()
	defer d.Hangup(d.Context())

	ulaw := make([]byte, 160)

	r, _ := d.AudioReader()
	w, _ := d.AudioWriter()
	time.Sleep(1 * time.Second)

	go func() {
		audio.EncodeUlawTo(ulaw, bytes.Repeat([]byte{1}, 320))
		reader := bytes.NewReader(ulaw)
		for {
			// reader.Reset()
			reader.Seek(0, 0)
			_, err = media.Copy(reader, w)
			if err != nil && !errors.Is(err, io.EOF) {
				slog.Error("Failed to copy", "error", err)
				return
			}

			recv := make([]byte, 160)
			if _, err := r.Read(recv); err != nil {
				slog.Error("Failed to read", "error", err)
				return
			}

			slog.Info("Echo received")
			if !bytes.Equal(ulaw, recv) {
				slog.Error("Send is not received")
			}
			time.Sleep(1 * time.Second)
		}
	}()
	<-ctx.Done()
}
