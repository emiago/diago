package diago

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/emiago/sipgox"
	"github.com/emiago/sipgox/sdp"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

type ServeDialogFunc func(d *DialogServerSession)

type Diago struct {
	ua         *sipgo.UserAgent
	client     *sipgo.Client
	server     *sipgo.Server
	transports []Transport

	serveHandler ServeDialogFunc

	dialogServer *sipgo.DialogServer
	dialogClient *sipgo.DialogClient

	auth      sipgo.DigestAuth
	mediaConf MediaConfig

	log zerolog.Logger
}

// We can extend this WithClientOptions, WithServerOptions

type DiagoOption func(dg *Diago)

func WithClientOptions(opts ...sipgo.ClientOption) DiagoOption {
	return func(dg *Diago) {
		// TODO remove error here
		cli, err := sipgo.NewClient(dg.ua)
		if err != nil {
			panic(err)
		}

		dg.client = cli
	}
}

func WithServerOptions(opts ...sipgo.ServerOption) DiagoOption {
	return func(dg *Diago) {
		// TODO remove error here
		srv, err := sipgo.NewServer(dg.ua)
		if err != nil {
			panic(err)
		}

		dg.server = srv
	}
}

func WithAuth(auth sipgo.DigestAuth) DiagoOption {
	return func(dg *Diago) {
		dg.auth = auth
	}
}

type Transport struct {
	Transport string
	BindHost  string
	BindPort  int

	ExternalAddr string // SIP signaling and media external addr
	// ExternalMediaAddr string // External media addr

	// In case TLS protocol
	TLSConf *tls.Config
}

func WithTransport(t Transport) DiagoOption {
	return func(dg *Diago) {
		dg.transports = append(dg.transports, t)
	}
}

type MediaConfig struct {
	Formats sdp.Formats

	// TODO
	// RTPPortStart int
	// RTPPortEnd   int
}

func WithMediaConfig(conf MediaConfig) DiagoOption {
	return func(dg *Diago) {
		dg.mediaConf = conf
	}
}

// NewDiago construct b2b user agent that will act as server and client
func NewDiago(ua *sipgo.UserAgent, opts ...DiagoOption) *Diago {
	client, _ := sipgo.NewClient(ua)
	server, _ := sipgo.NewServer(ua)

	dg := &Diago{
		ua:     ua,
		client: client,
		server: server,
		log:    log.Logger,
		serveHandler: func(d *DialogServerSession) {
			fmt.Println("Serve Handler not implemented")
		},
		transports: []Transport{},
		mediaConf: MediaConfig{
			Formats: sdp.NewFormats(sdp.FORMAT_TYPE_ULAW, sdp.FORMAT_TYPE_ALAW),
		},
	}

	for _, o := range opts {
		o(dg)
	}

	if len(dg.transports) == 0 {
		dg.transports = append(dg.transports, Transport{
			Transport: "udp",
			BindHost:  "127.0.0.1",
			BindPort:  5060,
		})
	}

	transport := dg.transports[0]
	// Create our default contact hdr
	contactHDR := sip.ContactHeader{
		DisplayName: "", // TODO
		Address: sip.Uri{
			User:      ua.Name(),
			Host:      transport.BindHost,
			Port:      transport.BindPort,
			UriParams: sip.NewParams(),
		},
	}

	dg.dialogServer = sipgo.NewDialogServer(dg.client, contactHDR)
	dg.dialogClient = sipgo.NewDialogClient(dg.client, contactHDR)

	// Sedgp our dialog
	server.OnInvite(func(req *sip.Request, tx sip.ServerTransaction) {
		// What if multiple server transports?

		dialog, err := dg.dialogServer.ReadInvite(req, tx)
		if err != nil {
			dg.log.Error().Err(err).Msg("Handling new INVITE failed")
			return
		}
		// dialog.Close
		// We will close dialog with our wrapper below

		// TODO authentication
		// TODO media and SDP
		dWrap := &DialogServerSession{
			DialogServerSession: dialog,
			DialogMedia:         DialogMedia{},

			contactHDR: contactHDR,
			formats:    dg.mediaConf.Formats,
		}
		defer dWrap.Close()

		// Find contact hdr matching transport
		if len(dg.transports) > 1 {
			for _, t := range dg.transports {
				if strings.EqualFold(req.Transport(), t.Transport) {
					dWrap.contactHDR = sip.ContactHeader{
						DisplayName: "", // TODO
						Address: sip.Uri{
							User:      ua.Name(),
							Host:      t.BindHost,
							Port:      t.BindPort,
							UriParams: sip.NewParams(),
						},
					}
				}
			}
		}

		dg.serveHandler(dWrap)

		// Check is dialog closed
		dialogCtx := dialog.Context()
		// Always try hanguping call
		ctx, cancel := context.WithTimeout(dialogCtx, 10*time.Second)
		defer cancel()

		if err := dWrap.Hangup(ctx); err != nil {
			if errors.Is(ctx.Err(), context.Canceled) {
				// Already hangup
				return
			}

			dg.log.Error().Err(err).Msg("Hanguping call failed")
			return
		}
	})

	server.OnAck(func(req *sip.Request, tx sip.ServerTransaction) {
		if err := dg.dialogServer.ReadAck(req, tx); err != nil {
			dg.log.Error().Err(err).Msg("ACK finished with error")
		}
	})

	server.OnBye(func(req *sip.Request, tx sip.ServerTransaction) {
		err := dg.dialogServer.ReadBye(req, tx)
		if errors.Is(err, sipgo.ErrDialogDoesNotExists) {
			err = dg.dialogClient.ReadBye(req, tx)
		}

		if err != nil {
			dg.log.Error().Err(err).Msg("BYE finished with error")
		}
	})

	return dg
}

func (dg *Diago) Serve(ctx context.Context, f ServeDialogFunc) error {
	server := dg.server

	dg.serveHandler = f

	// For multi transports start multi server
	if len(dg.transports) > 1 {
		errCh := make(chan error, len(dg.transports))
		for _, tran := range dg.transports {
			hostport := net.JoinHostPort(tran.BindHost, strconv.Itoa(tran.BindPort))
			go func() {
				errCh <- server.ListenAndServe(ctx, tran.Transport, hostport)
			}()
		}
		return <-errCh
	}

	tran := dg.transports[0]
	hostport := net.JoinHostPort(tran.BindHost, strconv.Itoa(tran.BindPort))
	return server.ListenAndServe(ctx, tran.Transport, hostport)
}

// Serve starts serving in background but waits server listener started before returning
func (dg *Diago) ServeBackground(ctx context.Context, f ServeDialogFunc) error {
	ch := make(chan struct{})
	ctx = context.WithValue(ctx, sipgo.ListenReadyCtxKey, sipgo.ListenReadyCtxValue(ch))

	go dg.Serve(ctx, f)

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-ch:
		return nil
	}
}

// When bridge is provided then this call will be bridged with any participant already present in bridge
// When bridge is nil, only dialing is done

// TODO
// When bridge we need to enforce same codec order
// This means SDP body can be different, but for now lets assume
// codec definition is global and codec for inbound and outbound must be same
func (dg *Diago) Dial(ctx context.Context, recipient sip.Uri, bridge *Bridge, opts sipgo.AnswerOptions) (d *DialogClientSession, err error) {
	dialogCli := dg.dialogClient

	// Now media SEdgP
	// TODO this probably needs take in account Contact header or listen addr
	ip, port, err := sipgox.FindFreeInterfaceHostPort("udp", "")
	if err != nil {
		return nil, err
	}
	laddr := &net.UDPAddr{IP: ip, Port: port}
	sess, err := sipgox.NewMediaSession(laddr)
	sess.Formats = dg.mediaConf.Formats

	if err != nil {
		return nil, err
	}

	dialog, err := dialogCli.Invite(ctx, recipient, sess.LocalSDP(), sip.NewHeader("Content-Type", "application/sdp"))
	if err != nil {
		sess.Close()
		return nil, err
	}

	d = &DialogClientSession{
		DialogClientSession: dialog,
	}

	// Set media Session
	d.Session = sess

	waitCall := func() error {
		if err := dialog.WaitAnswer(ctx, opts); err != nil {
			return err
		}

		remoteSDP := dialog.InviteResponse.Body()
		if remoteSDP == nil {
			return fmt.Errorf("no SDP in response")
		}
		if err := sess.RemoteSDP(remoteSDP); err != nil {
			return err
		}

		// Add to bridge as early media
		if bridge != nil {
			if err := bridge.AddDialogSession(d); err != nil {
				return err
			}
		}

		return dialog.Ack(ctx)
	}

	if err := waitCall(); err != nil {
		d.Close()
		return nil, err
	}

	return d, nil
}
