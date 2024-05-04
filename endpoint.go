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
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

type ServeDialogFunc func(d *DialogServerSession)

type Endpoint struct {
	ua         *sipgo.UserAgent
	client     *sipgo.Client
	server     *sipgo.Server
	transports []EndpointTransport

	serveHandler ServeDialogFunc

	dialogServer *sipgo.DialogServer
	dialogClient *sipgo.DialogClient

	auth sipgo.DigestAuth

	log zerolog.Logger
}

// We can extend this WithClientOptions, WithServerOptions

type EndpointOption func(tu *Endpoint)

func WithTransactioUserClientOptions(opts ...sipgo.ClientOption) EndpointOption {
	return func(tu *Endpoint) {
		// TODO remove error here
		cli, err := sipgo.NewClient(tu.ua)
		if err != nil {
			panic(err)
		}

		tu.client = cli
	}
}

func WithTransactioUserServerOptions(opts ...sipgo.ServerOption) EndpointOption {
	return func(tu *Endpoint) {
		// TODO remove error here
		srv, err := sipgo.NewServer(tu.ua)
		if err != nil {
			panic(err)
		}

		tu.server = srv
	}
}

func WithTransactioUserAuth(auth sipgo.DigestAuth) EndpointOption {
	return func(tu *Endpoint) {
		tu.auth = auth
	}
}

type EndpointTransport struct {
	Transport string
	BindHost  string
	BindPort  int

	ExternalAddr string // SIP signaling and media external addr
	// ExternalMediaAddr string // External media addr

	// In case TLS protocol
	TLSConf *tls.Config
}

func WithEndpointTransport(t EndpointTransport) EndpointOption {
	return func(tu *Endpoint) {
		tu.transports = append(tu.transports, t)
	}
}

// NewEndpoint is SIP user agent that accepts or dials new dialog via SIP.
func NewEndpoint(ua *sipgo.UserAgent, opts ...EndpointOption) *Endpoint {
	client, _ := sipgo.NewClient(ua)
	server, _ := sipgo.NewServer(ua)

	tu := &Endpoint{
		ua:     ua,
		client: client,
		server: server,
		log:    log.Logger,
		serveHandler: func(d *DialogServerSession) {
			fmt.Println("Serve Handler not implemented")
		},
		transports: []EndpointTransport{},
	}

	for _, o := range opts {
		o(tu)
	}

	if len(tu.transports) == 0 {
		tu.transports = append(tu.transports, EndpointTransport{
			Transport: "udp",
			BindHost:  "127.0.0.1",
			BindPort:  5060,
		})
	}

	transport := tu.transports[0]
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

	tu.dialogServer = sipgo.NewDialogServer(tu.client, contactHDR)
	tu.dialogClient = sipgo.NewDialogClient(tu.client, contactHDR)

	// Setup our dialog
	server.OnInvite(func(req *sip.Request, tx sip.ServerTransaction) {
		// What if multiple server transports?

		dialog, err := tu.dialogServer.ReadInvite(req, tx)
		if err != nil {
			tu.log.Error().Err(err).Msg("Handling new INVITE failed")
			return
		}
		defer dialog.Close()

		// TODO authentication
		// TODO media and SDP
		dWrap := &DialogServerSession{
			DialogServerSession: dialog,
			DialogMedia:         DialogMedia{},

			contactHDR: contactHDR,
		}

		// Find contact hdr matching transport
		if len(tu.transports) > 1 {
			for _, t := range tu.transports {
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

		tu.serveHandler(dWrap)

		// Check is dialog closed
		dialogCtx := dialog.Context()
		select {
		case <-dialogCtx.Done():
			return
		default:
		}

		// Always try hanguping call
		ctx, cancel := context.WithTimeout(dialogCtx, 10*time.Second)
		defer cancel()

		if err := dWrap.Hangup(ctx); err != nil {
			if errors.Is(ctx.Err(), context.Canceled) {
				// Already hangup
				return
			}

			tu.log.Error().Err(err).Msg("Hanguping call failed")
			return
		}
	})

	server.OnAck(func(req *sip.Request, tx sip.ServerTransaction) {
		tu.dialogServer.ReadAck(req, tx)
	})

	server.OnBye(func(req *sip.Request, tx sip.ServerTransaction) {
		err := tu.dialogServer.ReadBye(req, tx)
		if errors.Is(err, sipgo.ErrDialogDoesNotExists) {
			err = tu.dialogClient.ReadBye(req, tx)
		}

		if err != nil {
			tu.log.Error().Err(err).Msg("Bye finished with error")
		}
	})

	return tu
}

// Serve main function for passing callback for handling new dialog session for inbound call
func (tu *Endpoint) Serve(ctx context.Context, f ServeDialogFunc) error {
	server := tu.server

	tu.serveHandler = f

	// For multi transports start multi server
	if len(tu.transports) > 1 {
		errCh := make(chan error, len(tu.transports))
		for _, tran := range tu.transports {
			hostport := net.JoinHostPort(tran.BindHost, strconv.Itoa(tran.BindPort))
			go func() {
				errCh <- server.ListenAndServe(ctx, tran.Transport, hostport)
			}()
		}
		return <-errCh
	}

	tran := tu.transports[0]
	hostport := net.JoinHostPort(tran.BindHost, strconv.Itoa(tran.BindPort))
	return server.ListenAndServe(ctx, tran.Transport, hostport)
}

// Serve starts serving in background but waits server listener started before returning
func (tu *Endpoint) ServeBackground(ctx context.Context, f ServeDialogFunc) error {
	ch := make(chan struct{})
	ctx = context.WithValue(ctx, sipgo.ListenReadyCtxKey, sipgo.ListenReadyCtxValue(ch))

	go tu.Serve(ctx, f)

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
func (tu *Endpoint) Dial(ctx context.Context, recipient sip.Uri, bridge *Bridge, opts sipgo.AnswerOptions) (d *DialogClientSession, err error) {
	dialogCli := tu.dialogClient

	// Now media SETUP
	// TODO this probably needs take in account Contact header or listen addr
	ip, port, err := sipgox.FindFreeInterfaceHostPort("udp", "")
	if err != nil {
		return nil, err
	}
	laddr := &net.UDPAddr{IP: ip, Port: port}
	sess, err := sipgox.NewMediaSession(laddr)
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
