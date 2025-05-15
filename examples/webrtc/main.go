package main

import (
	"context"
	"flag"
	"log/slog"
	"os"
	"os/signal"

	"github.com/emiago/diago/diagomod"
	"github.com/emiago/diago/testdata"

	"github.com/emiago/diago"
	"github.com/emiago/diago/examples"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

var (
	fbindHostPort = flag.String("l", "127.0.0.1:5443", "SIP listen Addr")
)

func main() {
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	examples.SetupLogger()

	err := start(ctx)
	if err != nil {
		slog.Error("PBX finished with error", "error", err)
	}
}

func start(ctx context.Context) error {
	// Setup our main transaction user
	host, port, err := sip.ParseAddr(*fbindHostPort)
	if err != nil {
		return err
	}

	transportWS := diago.Transport{
		// Transport:    "wss",
		Transport:    "ws",
		BindHost:     host,
		BindPort:     port,
		ExternalHost: host,
		ExternalPort: port,
		// TLSConf:      testdata.ServerTLSConfig(),
		// RewriteContact: true,
	}

	ua, _ := sipgo.NewUA()
	server, _ := sipgo.NewServer(ua)
	tu := diago.NewDiago(ua,
		diago.WithTransport(transportWS),
		diago.WithServer(server),
	)

	server.OnRegister(func(req *sip.Request, tx sip.ServerTransaction) {
		res := sip.NewResponseFromRequest(req, 200, "OK", nil)
		tx.Respond(res)
	})

	return tu.Serve(ctx, func(inDialog *diago.DialogServerSession) {
		slog.Info("Webrtc call start")
		defer slog.Info("Webrtc call end")
		if err := WebrtcPlaybackMemory(inDialog); err != nil {
			slog.Error("Webrtc playback failed", "error", err)
		}
	})
}

func WebrtcPlaybackMemory(inDialog *diago.DialogServerSession) error {
	inDialog.Progress() // Progress -> 100 Trying
	inDialog.Ringing()  // Ringing -> 180 Response

	if err := diagomod.AnswerWebrtc(inDialog, diagomod.AnswerWebrtcOptions{}); err != nil {
		return err
	}

	playfile, err := testdata.OpenFile("demo-echotest.wav")
	if err != nil {
		return err
	}
	pb, err := inDialog.PlaybackCreate()
	if err != nil {
		return err
	}

	_, err = pb.Play(playfile, "audio/wav")
	return err
}
