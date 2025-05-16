package main

import (
	"context"
	"flag"
	"log/slog"
	"net/http"
	"os"
	"os/signal"

	"github.com/emiago/diago/testdata"

	"github.com/emiago/diago"
	"github.com/emiago/diago/diagomod"
	"github.com/emiago/diago/examples"
	"github.com/emiago/diago/examples/webrtc_jssip/web"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

var (
	fbindHostPort  = flag.String("l", "0.0.0.0:8080", "SIP listen Addr")
	fhttpHostPort  = flag.String("http", "0.0.0.0:8081", "HTTP server listen Addr")
	fwebPathPrefix = flag.String("web-path", "/", "Web path prefix for static content")
)

func main() {
	flag.Parse()

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	examples.SetupLogger()

	// Start HTTP server in a goroutine
	go func() {
		if err := startHTTPServer(ctx); err != nil && err != http.ErrServerClosed {
			slog.Error("HTTP server failed", "error", err)
		}
	}()

	err := start(ctx)
	if err != nil {
		slog.Error("PBX finished with error", "error", err)
	}
}

// startHTTPServer starts an HTTP server that serves the embedded web content
func startHTTPServer(ctx context.Context) error {
	mux := http.NewServeMux()

	// Mount the embedded file system
	if err := web.MountHTTPHandler(mux, *fwebPathPrefix); err != nil {
		return err
	}

	server := &http.Server{
		Addr:    *fhttpHostPort,
		Handler: mux,
	}

	slog.Info("Starting HTTP server", "address", *fhttpHostPort, "web-path", *fwebPathPrefix)

	// Shutdown the server when context is done
	go func() {
		<-ctx.Done()
		slog.Info("Shutting down HTTP server")
		if err := server.Shutdown(context.Background()); err != nil {
			slog.Error("HTTP server shutdown error", "error", err)
		}
	}()

	return server.ListenAndServe()
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
		res.AppendHeader(req.Contact()) // Required by jssip to accept registration response
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
