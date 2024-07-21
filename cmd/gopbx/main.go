package main

import (
	"context"
	"os"
	"os/signal"
	"time"

	"github.com/emiago/diago"
	"github.com/emiago/diago/testdata"
	"github.com/emiago/media"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/pion/rtp"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func main() {
	// Create transaction users, as many as needed.
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	lev, err := zerolog.ParseLevel(os.Getenv("LOG_LEVEL"))
	if err != nil || lev == zerolog.NoLevel {
		lev = zerolog.InfoLevel
	}

	sip.SIPDebug = os.Getenv("SIP_DEBUG") != ""
	sip.TransactionFSMDebug = os.Getenv("SIP_TRANSACTION_DEBUG") != ""

	zerolog.TimeFieldFormat = zerolog.TimeFormatUnixMicro
	log.Logger = zerolog.New(zerolog.ConsoleWriter{
		Out:        os.Stdout,
		TimeFormat: time.StampMicro,
	}).With().Timestamp().Logger().Level(lev)

	err = func(ctx context.Context) error {
		// Setup our main transaction user
		ua, _ := sipgo.NewUA()
		transportUDP := diago.Transport{
			Transport: "udp",
			BindHost:  "127.0.0.1",
			BindPort:  5060,
		}

		transportTCP := diago.Transport{
			Transport: "tcp",
			BindHost:  "127.0.0.1",
			BindPort:  5060,
		}

		srv := diago.NewDiago(ua,
			diago.WithTransport(transportUDP),
			diago.WithTransport(transportTCP),
		)

		// Setup our dialplan for this user
		dialplan := Dialplan{
			srv: srv,
		}

		log.Info().Interface("udp", transportUDP).Interface("tcp", transportTCP).Msg("Serving requests")
		err := srv.Serve(ctx, func(inDialog *diago.DialogServerSession) {
			log.Info().Str("callid", inDialog.InviteRequest.CallID().Value()).Msg("New dialog request")
			defer log.Info().Str("callid", inDialog.InviteRequest.CallID().Value()).Msg("Dialog finished")
			// Do the routing for incoming request
			switch inDialog.ToUser() {
			case "playback":
				dialplan.Playback(inDialog)
			case "playbackurl":
				dialplan.PlaybackURL(inDialog)
			case "bridge":
				dialplan.BridgeCall(inDialog)
			case "externalmedia":
				dialplan.ExternalMedia(inDialog)
			default:
				inDialog.Respond(sip.StatusNotFound, "Not found", nil)
			}
		})
		return err
	}(ctx)

	if err != nil {
		log.Fatal().Err(err).Msg("PBX finished with error")
	}
}

type Dialplan struct {
	srv *diago.Diago
}

func (d *Dialplan) Playback(inDialog *diago.DialogServerSession) {
	// tu := d.tu

	inDialog.Progress() // Progress -> 100 Trying
	inDialog.Ringing()  // Ringing -> 180 Response
	inDialog.Answer()   // Answqer -> 200 Response

	// _, filename, _, _ := runtime.Caller(1)
	// dir := path.Dir(filename)
	// playfile := path.Join(dir, "./demo-thanks.wav")

	file, err := testdata.OpenFile("demo-instruct.wav")
	if err != nil {
		log.Error().Err(err).Msg("Failed to open embeded file")
		return
	}
	fileInfo, _ := file.Stat()

	log.Info().Str("file", fileInfo.Name()).Msg("Playing a file")

	playback, err := inDialog.PlaybackCreate()
	if err != nil {
		log.Error().Err(err).Msg("Failed to create playback")
		return
	}

	if err := playback.Play(file, "audio/wav"); err != nil {
		log.Error().Err(err).Msg("Playing failed")
	}

	// if err := inDialog.PlaybackURL("https://www2.cs.uic.edu/~i101/SoundFiles/CantinaBand60.wav"); err != nil {
	// 	log.Error().Err(err).Msg("Playing failed")
	// }
}

func (d *Dialplan) PlaybackURL(inDialog *diago.DialogServerSession) {
	inDialog.Progress() // Progress -> 100 Trying
	inDialog.Ringing()  // Ringing -> 180 Response
	inDialog.Answer()   // Answqer -> 200 Response

	if err := inDialog.PlaybackURL(inDialog.Context(), "http://127.0.0.1:8080/"); err != nil {
		log.Error().Err(err).Msg("Playing url failed")
	}
}

func (d *Dialplan) BridgeCall(inDialog *diago.DialogServerSession) {
	tu := d.srv

	inDialog.Progress() // Progress -> 100 Trying
	inDialog.Ringing()  // Ringing -> 180 Response
	inDialog.Answer()   // Answqer -> 200 Response

	inCtx := inDialog.Context()
	ctx, cancel := context.WithTimeout(inCtx, 5*time.Second)
	defer cancel()

	// Wa want to bridge this call with originator
	bridge := new(diago.Bridge)
	outDialog, err := tu.Dial(ctx, sip.Uri{User: "test", Host: "127.0.0.1", Port: 5090}, bridge, sipgo.AnswerOptions{})
	if err != nil {
		log.Error().Err(err).Msg("Failed to dial")
		return
	}

	outCtx := outDialog.Context()
	defer func() {
		hctx, hcancel := context.WithTimeout(outCtx, 5*time.Second)
		defer hcancel()
		if err := outDialog.Hangup(hctx); err != nil {
			log.Error().Err(err).Msg("Failed to hangup")
		}
	}()

	// This is beauty, as you can even easily detect who hangups
	select {
	case <-inCtx.Done():
	case <-outCtx.Done():
	}
	// How to now do bridging
}

func (d *Dialplan) ExternalMedia(inDialog *diago.DialogServerSession) {
	inDialog.Progress() // Progress -> 100 Trying
	inDialog.Ringing()  // Ringing -> 180 Response
	inDialog.Answer()   // Answqer -> 200 Response

	lastPrint := time.Now()
	pktsCount := 0
	buf := make([]byte, media.RTPBufSize)
	for {
		pkt := rtp.Packet{}
		err := inDialog.Media().MediaSession.ReadRTP(buf, &pkt)
		if err != nil {
			return
		}

		if time.Since(lastPrint) > 3*time.Second {
			lastPrint = time.Now()
			log.Info().Uint8("PayloadType", pkt.PayloadType).Int("pkts", pktsCount).Msg("Received packets")
		}
		pktsCount++
	}
}
