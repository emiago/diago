// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emiago/diago/media"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

type ServeDialogFunc func(d *DialogServerSession)

type Diago struct {
	ua         *sipgo.UserAgent
	client     *sipgo.Client
	server     *sipgo.Server
	transports []Transport

	serveHandler ServeDialogFunc

	auth      sipgo.DigestAuth
	mediaConf MediaConfig

	log *slog.Logger

	cache DialogCachePool
}

// We can extend this WithClientOptions, WithServerOptions

type DiagoOption func(dg *Diago)

func WithAuth(auth sipgo.DigestAuth) DiagoOption {
	return func(dg *Diago) {
		dg.auth = auth
	}
}

type Transport struct {
	ID string

	// Transport must be udp,tcp or ws, or even forcing v4 like udp4, tcp4
	Transport string
	network   string // network will keep original transport value

	BindHost string
	BindPort int
	bindIP   net.IP

	ExternalHost string // SIP signaling and media external addr
	ExternalPort int

	// MediaExternalIP changes SDP IP, by default it tries to use external host if it is IP defined
	MediaExternalIP net.IP
	mediaBindIP     net.IP

	// In case TLS protocol
	TLSConf *tls.Config

	RewriteContact bool

	client *sipgo.Client
}

func WithTransport(t Transport) DiagoOption {
	return func(dg *Diago) {
		t.bindIP = net.ParseIP(t.BindHost)
		t.mediaBindIP = t.bindIP
		if t.bindIP != nil && t.bindIP.IsUnspecified() {
			network := "ip4"
			if t.bindIP.To4() == nil {
				network = "ip6"
			}
			var err error
			t.mediaBindIP, _, err = sip.ResolveInterfacesIP(network, nil)
			if err != nil {
				dg.log.Error("failed to resolve real IP", "error", err)
			}
		}

		if t.ExternalHost == "" {
			t.ExternalHost = t.BindHost
			// External host should match media IP
			if t.mediaBindIP != nil {
				t.ExternalHost = t.mediaBindIP.String()
			}
		}

		if t.ExternalPort == 0 {
			t.ExternalPort = t.BindPort
		}

		// Resolve unspecified IP for contact hdr
		extIp := net.ParseIP(t.ExternalHost)
		if t.ExternalHost == "" || (extIp != nil && extIp.IsUnspecified()) {
			// Use the mediaIP
			extIp = t.mediaBindIP
		}

		if t.MediaExternalIP == nil && t.ExternalHost != "" {
			// try to use external host as external media IP
			if extIp != nil && !extIp.IsUnspecified() {
				t.MediaExternalIP = extIp
			}
		}

		t.Transport = sip.NetworkToLower(t.Transport)
		t.network = t.Transport
		t.Transport = strings.TrimSuffix(t.Transport, "4") // udp4, tcp4
		t.Transport = strings.TrimSuffix(t.Transport, "6") // udp6, tcp6

		// we want to handle SIP networking better per each transport
		t.client = dg.createClient(t)
		dg.transports = append(dg.transports, t)

		dg.log.Debug("Loaded transport", "t", t)
	}
}

type MediaConfig struct {
	Codecs []media.Codec

	// Used internally
	bindIP     net.IP
	externalIP net.IP

	// TODO, For now it is global on media package
	// RTPPortStart int
	// RTPPortEnd   int
}

func WithMediaConfig(conf MediaConfig) DiagoOption {
	return func(dg *Diago) {
		dg.mediaConf = conf
	}
}

// WithServer allows providing custom server handle. Consider still it needs to use same UA as diago
func WithServer(srv *sipgo.Server) DiagoOption {
	return func(dg *Diago) {
		dg.server = srv
	}
}

// WithClient allows providing custom client handle. Consider still it needs to use same UA as diago
func WithClient(client *sipgo.Client) DiagoOption {
	return func(dg *Diago) {
		dg.client = client
	}
}

func WithLogger(l *slog.Logger) DiagoOption {
	return func(dg *Diago) {
		dg.log = l
	}
}

// NewDiago construct b2b user agent that will act as server and client
func NewDiago(ua *sipgo.UserAgent, opts ...DiagoOption) *Diago {
	dg := &Diago{
		ua:  ua,
		log: slog.Default(),
		serveHandler: func(d *DialogServerSession) {
			fmt.Println("Serve Handler not implemented")
		},
		transports: []Transport{},
		mediaConf: MediaConfig{
			Codecs: []media.Codec{media.CodecAudioUlaw, media.CodecAudioAlaw, media.CodecTelephoneEvent8000},
		},

		cache: DialogCachePool{
			client: &dialogCacheMap[*DialogClientSession]{sync.Map{}},
			server: &dialogCacheMap[*DialogServerSession]{sync.Map{}},
		},
	}

	for _, o := range opts {
		o(dg)
	}

	if len(dg.transports) == 0 {
		tran := Transport{
			Transport: "udp",
			BindHost:  "127.0.0.1",
			BindPort:  5060,
		}
		WithTransport(tran)(dg)
	}

	if dg.server == nil {
		dg.server, _ = sipgo.NewServer(ua)
	}
	server := dg.server

	errHandler := func(f func(req *sip.Request, tx sip.ServerTransaction) error) sipgo.RequestHandler {
		return func(req *sip.Request, tx sip.ServerTransaction) {
			if err := f(req, tx); err != nil {
				dg.log.Error("Failed to handle request", "error", err, "req.method", req.Method.String())
				return
			}
			// Termination gracefull will be done by sipgo now
		}
	}

	server.OnInvite(errHandler(func(req *sip.Request, tx sip.ServerTransaction) error {
		// What if multiple server transports?
		id, err := sip.UASReadRequestDialogID(req)
		if err == nil {
			return dg.handleReInvite(req, tx, id)
		}

		tran, _ := dg.getTransport(req.Transport())

		// Proceed as new call
		dialogUA := sipgo.DialogUA{
			Client:         dg.getClient(&tran),
			RewriteContact: tran.RewriteContact,
		}
		dg.contactHDRFromTransport(tran, &dialogUA.ContactHDR)

		dialog, err := dialogUA.ReadInvite(req, tx)
		if err != nil {
			return fmt.Errorf("handling new INVITE failed: %w", err)
		}

		// TODO authentication
		dWrap := &DialogServerSession{
			DialogServerSession: dialog,
			DialogMedia:         DialogMedia{},
			// TODO we may actually just build media session with this conf here
			mediaConf: MediaConfig{
				Codecs:     dg.mediaConf.Codecs,
				bindIP:     tran.mediaBindIP,
				externalIP: tran.MediaExternalIP,
			},
		}

		defer closeAndLog(dWrap, "closing dialog server returned error")

		if err := dg.cache.server.DialogStore(dWrap.Context(), dWrap.ID, dWrap); err != nil {
			return fmt.Errorf("failed to store server dialog: %w", err)
		}
		defer func() {
			// TODO: have better context
			if err := dg.cache.server.DialogDelete(context.Background(), dWrap.ID); err != nil {
				dg.log.Error("Failed to delete server dialog", "error", err)
			}
		}()

		dg.serveHandler(dWrap)

		// Check is dialog closed
		dialogCtx := dialog.Context()
		// Always try hanguping call
		ctx, cancel := context.WithTimeout(dialogCtx, 10*time.Second)
		defer cancel()

		if err := dWrap.Hangup(ctx); err != nil {
			if errors.Is(ctx.Err(), context.Canceled) {
				// Already hangup
				return nil
			}

			return fmt.Errorf("hanguping call failed: %w", err)
		}
		return nil
	}))

	server.OnCancel(func(req *sip.Request, tx sip.ServerTransaction) {
		// INVITE transaction should be terminated by transaction layer and 200 response will be sent
		// In case of stateless proxy this we would need to forward
		tx.Respond(sip.NewResponseFromRequest(req, sip.StatusCallTransactionDoesNotExists, "Call/Transaction Does Not Exist", nil))
	})

	server.OnAck(errHandler(func(req *sip.Request, tx sip.ServerTransaction) error {
		d, err := dg.cache.MatchDialogServer(req)
		if err != nil {
			// Normally ACK will be received if some out of dialog request is received or we responded negatively
			// tx.Respond(sip.NewResponseFromRequest(req, sip.StatusBadRequest, err.Error(), nil))
			return err
		}

		return d.ReadAck(req, tx)
	}))

	server.OnBye(errHandler(func(req *sip.Request, tx sip.ServerTransaction) error {
		sd, cd, err := dg.cache.MatchDialog(req)
		if err != nil {
			if errors.Is(err, sipgo.ErrDialogDoesNotExists) {
				return tx.Respond(sip.NewResponseFromRequest(req, sip.StatusCallTransactionDoesNotExists, err.Error(), nil))

			}
			return tx.Respond(sip.NewResponseFromRequest(req, sip.StatusBadRequest, err.Error(), nil))
		}

		// Respond to BYE
		// Terminate our media processing
		// As user may stuck in playing or reading media, this unblocks that goroutine
		if cd != nil {
			defer closeAndLog(&cd.DialogMedia, "failed to close client media")

			return cd.ReadBye(req, tx)
		}

		defer closeAndLog(&sd.DialogMedia, "failed to close server media")
		return sd.ReadBye(req, tx)
	}))

	server.OnInfo(errHandler(func(req *sip.Request, tx sip.ServerTransaction) error {
		// Handle DTMF out of band
		if req.ContentType().Value() != "application/dtmf-relay" {
			return tx.Respond(sip.NewResponseFromRequest(req, sip.StatusNotAcceptable, "Not Acceptable", nil))
		}

		sd, cd, err := dg.cache.MatchDialog(req)
		if err != nil {
			if errors.Is(err, sipgo.ErrDialogDoesNotExists) {
				return tx.Respond(sip.NewResponseFromRequest(req, sip.StatusCallTransactionDoesNotExists, err.Error(), nil))

			}
			return tx.Respond(sip.NewResponseFromRequest(req, sip.StatusBadRequest, err.Error(), nil))
		}

		if cd != nil {
			return cd.readSIPInfoDTMF(req, tx)

		}
		return sd.readSIPInfoDTMF(req, tx)

		// 		INFO sips:sipgo@127.0.0.1:5443 SIP/2.0
		// Via: SIP/2.0/WSS df7jal23ls0d.invalid;branch=z9hG4bKhzJuRuWp4pLmTAbrIg7MUGofWdV1u577;rport
		// From: "IVR Webrtc"<sips:ivr.699c4b45-c800-4891-8133-fded5b26f942.579938@localhost:6060>;tag=foSxtEhHq9QOSeSdgJCC
		// To: <sip:playback@localhost>;tag=f814097f-467a-46ad-be0a-76c2a1225378
		// Contact: "IVR Webrtc"<sips:ivr.699c4b45-c800-4891-8133-fded5b26f942.579938@df7jal23ls0d.invalid;rtcweb-breaker=no;click2call=no;transport=wss>;+g.oma.sip-im;language="en,fr"
		// Call-ID: 047c3631-e85a-27d2-8f69-4de6e0391253
		// CSeq: 29586 INFO
		// Content-Type: application/dtmf-relay
		// Content-Length: 22
		// Max-Forwards: 70
		// User-Agent: IM-client/OMA1.0 sipML5-v1.2016.03.04

		// Signal=8
		// Duration=120

	}))

	// TODO deal with OPTIONS more correctly
	// For now leave it for keep alive
	dg.server.OnOptions(errHandler(func(req *sip.Request, tx sip.ServerTransaction) error {
		res := sip.NewResponseFromRequest(req, sip.StatusOK, "OK", nil)
		return tx.Respond(res)
	}))

	dg.server.OnRefer(func(req *sip.Request, tx sip.ServerTransaction) {
		sd, cd, err := dg.cache.MatchDialog(req)
		if err != nil {
			if errors.Is(err, sipgo.ErrDialogDoesNotExists) {
				tx.Respond(sip.NewResponseFromRequest(req, sip.StatusCallTransactionDoesNotExists, err.Error(), nil))
				return

			}
			tx.Respond(sip.NewResponseFromRequest(req, sip.StatusBadRequest, err.Error(), nil))
			return
		}
		if cd != nil {
			cd.handleRefer(dg, req, tx)
			return

		}
		// TODO server
		sd.handleRefer(dg, req, tx)
	})

	dg.server.OnNotify(func(req *sip.Request, tx sip.ServerTransaction) {
		// THIS should match now subscribtion instead dialog
		sd, cd, err := dg.cache.MatchDialog(req)
		if err != nil {
			if errors.Is(err, sipgo.ErrDialogDoesNotExists) {
				tx.Respond(sip.NewResponseFromRequest(req, sip.StatusCallTransactionDoesNotExists, err.Error(), nil))
				return

			}
			tx.Respond(sip.NewResponseFromRequest(req, sip.StatusBadRequest, err.Error(), nil))
			return
		}

		if cd != nil {
			cd.handleReferNotify(req, tx)
			return
		}
		sd.handleReferNotify(req, tx)
	})
	// server.OnRefer(func(req *sip.Request, tx sip.ServerTransaction) {
	// 	d, err := MatchDialogServer(req)
	// 	if err != nil {
	// 		// Security? When to answer this?
	// 		tx.Respond(sip.NewResponseFromRequest(req, sip.StatusBadRequest, err.Error(), nil))
	// 		return
	// 	}
	// })

	return dg
}

func (dg *Diago) handleReInvite(req *sip.Request, tx sip.ServerTransaction, id string) error {
	ctx := context.TODO()
	// No Error means we have ID
	s, err := dg.cache.server.DialogLoad(ctx, id)
	if err != nil {
		id, err := sip.UACReadRequestDialogID(req)
		if err != nil {
			dg.log.Info("Reinvite failed to read request dialog ID", "error", err)
			return tx.Respond(sip.NewResponseFromRequest(req, sip.StatusBadRequest, "Bad Request", nil))

		}
		// No Error means we have ID
		s, err := dg.cache.client.DialogLoad(ctx, id)
		if err != nil {
			return tx.Respond(sip.NewResponseFromRequest(req, sip.StatusCallTransactionDoesNotExists, "Call/Transaction Does Not Exist", nil))
		}

		return s.handleReInvite(req, tx)
	}

	return s.handleReInvite(req, tx)
}

func (dg *Diago) Serve(ctx context.Context, f ServeDialogFunc) error {
	return dg.serve(ctx, f, func() {})
}

func (dg *Diago) serve(ctx context.Context, f ServeDialogFunc, readyCh func()) error {
	server := dg.server
	dg.HandleFunc(f)

	errCh := make(chan error, len(dg.transports))
	for i, tran := range dg.transports {
		hostport := net.JoinHostPort(tran.BindHost, strconv.Itoa(tran.BindPort))

		go func(i int, tran Transport) {
			// Update transport
			ctx = context.WithValue(ctx, sipgo.ListenReadyCtxKey, sipgo.ListenReadyFuncCtxValue(func(network, addr string) {
				// This now fixes port for empheral binding
				// Alternative to use is tp.GetListenPort but it squashes networks
				_, port, _ := sip.ParseAddr(addr)
				if tran.BindPort == 0 {
					tran.BindPort = port
					tran.ExternalPort = port
					tran.client = dg.createClient(tran)
					dg.transports[i] = tran
				}
				readyCh()

				dg.log.Info("Listening on transport", "addr", addr, "protocol", tran.network)
			}))

			if tran.TLSConf != nil {
				errCh <- server.ListenAndServeTLS(ctx, tran.network, hostport, tran.TLSConf)
				return
			}
			errCh <- server.ListenAndServe(ctx, tran.network, hostport)
		}(i, tran)
	}

	// Returns first error
	return <-errCh
	// }

	// tran := dg.transports[0]
	// hostport := net.JoinHostPort(tran.BindHost, strconv.Itoa(tran.BindPort))
	// return server.ListenAndServe(ctx, tran.Transport, hostport)
}

// Serve starts serving in background but waits server listener started before returning
func (dg *Diago) ServeBackground(ctx context.Context, f ServeDialogFunc) error {
	readyCh := make(chan struct{}, len(dg.transports))
	ready := func() {
		readyCh <- struct{}{}
	}
	chErr := make(chan error, 1)

	go func() {
		chErr <- dg.serve(ctx, f, ready)
	}()

	for range dg.transports {
		select {
		case err := <-chErr:
			return err
		case <-readyCh:
			dg.log.Info("Network ready")
		}
	}
	return nil
}

// HandleFunc registers you handler function for dialog. Must be called before serving request
func (dg *Diago) HandleFunc(f ServeDialogFunc) {
	dg.serveHandler = f
}

type InviteOptions struct {
	Originator DialogSession
	OnResponse func(res *sip.Response) error
	Transport  string
	// For digest authentication
	Username string
	Password string
	// Custom headers to pass. DO NOT SET THIS to nil
	Headers []sip.Header
}

// Invite makes outgoing call leg and waits for answer.
// If you want to bridge call then use helper InviteBridge
func (dg *Diago) Invite(ctx context.Context, recipient sip.Uri, opts InviteOptions) (d *DialogClientSession, err error) {
	d, err = dg.NewDialog(recipient, NewDialogOptions{Transport: opts.Transport})
	if err != nil {
		return nil, err
	}

	if err := d.Invite(ctx, InviteClientOptions{
		Originator: opts.Originator,
		OnResponse: opts.OnResponse,
		Headers:    opts.Headers,
		Username:   opts.Username,
		Password:   opts.Password,
	}); err != nil {
		d.Close()
		return nil, err
	}

	if err := d.Ack(ctx); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

// InviteBridge makes outgoing call leg and does bridging.
// Outgoing session will be added into bridge on answer
// If bridge has Originator (first participant) it will be used for creating outgoing call leg as in B2BUA
// When bridge is provided then this call will be bridged with any participant already present in bridge
func (dg *Diago) InviteBridge(ctx context.Context, recipient sip.Uri, bridge *Bridge, opts InviteOptions) (d *DialogClientSession, err error) {
	d, err = dg.NewDialog(recipient, NewDialogOptions{})
	if err != nil {
		return nil, err
	}

	// Keep things compatible
	if opts.Originator == nil {
		opts.Originator = bridge.Originator
	}

	if err := d.Invite(ctx, InviteClientOptions{
		Originator: opts.Originator,
		OnResponse: opts.OnResponse,
		Headers:    opts.Headers,
		Username:   opts.Username,
		Password:   opts.Password,
	}); err != nil {
		d.Close()
		return nil, err
	}

	// Do bridging now
	if err := bridge.AddDialogSession(d); err != nil {
		d.Close()
		return nil, err
	}

	if err := d.Ack(ctx); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

type NewDialogOptions struct {
	// Transport or protocol that should be used
	Transport string
	// TransportID matches diago transport by ID instead protocol
	TransportID string

	// Codecs []media.Codec
}

// NewDialog creates a new client dialog session after you can perform dialog Invite
func (dg *Diago) NewDialog(recipient sip.Uri, opts NewDialogOptions) (d *DialogClientSession, err error) {
	transport := opts.Transport
	if transport == "" && recipient.UriParams != nil {
		if t := recipient.UriParams["transport"]; t != "" {
			transport = t
			delete(recipient.UriParams, "transport")
		}

	}
	tran, exists := dg.findTransport(transport, opts.TransportID)
	if !exists {
		return nil, fmt.Errorf("transport %s does not exists", transport)
	}
	// set now found transport
	transport = tran.Transport

	// TODO: remove this alloc of UA each time
	client := dg.getClient(&tran)
	dialogUA := sipgo.DialogUA{
		Client:         client,
		RewriteContact: tran.RewriteContact,
	}
	dg.contactHDRFromTransport(tran, &dialogUA.ContactHDR)

	inviteReq := sip.NewRequest(sip.INVITE, recipient)
	inviteReq.SetTransport(sip.NetworkToUpper(transport))

	d = &DialogClientSession{
		DialogClientSession: &sipgo.DialogClientSession{
			UA: &dialogUA,
			Dialog: sipgo.Dialog{
				InviteRequest: inviteReq,
			},
		},
	}
	d.Init()

	// Create media
	// TODO explicit media format passing
	mediaConf := MediaConfig{
		Codecs:     dg.mediaConf.Codecs,
		bindIP:     tran.mediaBindIP,
		externalIP: tran.MediaExternalIP,
	}

	// if opts.Codecs != nil {
	// 	mediaConf.Codecs = opts.Codecs
	// }

	if err := d.initMediaSessionFromConf(mediaConf); err != nil {
		return nil, err
	}

	// This should be run on ACK
	d.OnState(func(s sip.DialogState) {
		if s != sip.DialogStateConfirmed {
			return
		}

		// Now dialog is established and can be add into store
		if err := dg.cache.client.DialogStore(context.Background(), d.ID, d); err != nil {
			dg.log.Error("Failed to store in dialog cache", "error", err)
		}
	})

	d.OnClose(func() error {
		return dg.cache.client.DialogDelete(context.Background(), d.ID)
	})
	return d, nil
}

func (dg *Diago) contactHDRFromTransport(tran Transport, contact *sip.ContactHeader) {
	// Find contact hdr matching transport
	scheme := "sip"
	if tran.TLSConf != nil {
		scheme = "sips"
	}

	contact.DisplayName = "" //TODO
	contact.Address = sip.Uri{
		Scheme:    scheme,
		User:      dg.ua.Name(),
		Host:      tran.ExternalHost,
		Port:      tran.ExternalPort,
		UriParams: sip.NewParams(),
		Headers:   sip.NewParams(),
	}
}

func (dg *Diago) getClient(tran *Transport) *sipgo.Client {
	if dg.client != nil {
		// Use global one if exists
		return dg.client
	}

	return tran.client
}

func (dg *Diago) getTransport(transport string) (Transport, bool) {
	if transport == "" {
		return dg.transports[0], true
	}
	for _, t := range dg.transports {
		if sip.NetworkToLower(transport) == t.Transport {
			return t, true
		}
	}
	return Transport{}, false
}

func (dg *Diago) findTransport(transport string, id string) (Transport, bool) {
	if transport != "" {
		return dg.getTransport(transport)
	}

	if id != "" {
		for _, t := range dg.transports {
			if id == t.ID {
				return t, true
			}
		}
		return Transport{}, false
	}

	return dg.getTransport("udp")
}

type RegisterOptions struct {
	// Digest auth
	Username  string
	Password  string
	ProxyHost string

	// Expiry is for Expire header
	Expiry time.Duration
	// Retry interval is interval before next Register is sent
	RetryInterval time.Duration
	AllowHeaders  []string

	// Useragent default will be used on what is provided as NewUA()
	// UserAgent         string
	// UserAgentHostname string
}

// Register will create register transaction and keep registration ongoing until error is hit.
// For more granular control over registraions user RegisterTransaction
func (dg *Diago) Register(ctx context.Context, recipient sip.Uri, opts RegisterOptions) error {
	t, err := dg.RegisterTransaction(ctx, recipient, opts)
	if err != nil {
		return err
	}

	if err := t.Register(ctx); err != nil {
		return err
	}

	// Unregister
	defer func() {
		ctx, _ := context.WithTimeout(context.Background(), 5*time.Second)
		err := t.Unregister(ctx)
		if err != nil {
			dg.log.Error("Failed to unregister", "error", err)
			return
		}
		dg.log.Debug("Unregister successfull")
	}()

	return t.QualifyLoop(ctx)
}

// Register transaction creates register transaction object that can be used for Register Unregister requests
func (dg *Diago) RegisterTransaction(ctx context.Context, recipient sip.Uri, opts RegisterOptions) (*RegisterTransaction, error) {
	// Make our client reuse address
	transport := recipient.UriParams["transport"]
	if transport == "" {
		transport = "udp"
	}
	tran, exists := dg.getTransport(transport)
	if !exists {
		return nil, fmt.Errorf("transport=%s does not exists", transport)
	}

	contactHDR := sip.ContactHeader{}
	dg.contactHDRFromTransport(tran, &contactHDR)

	// client, err := sipgo.NewClient(dg.ua,
	// 	sipgo.WithClientHostname(contactHDR.Address.Host),
	// 	// sipgo.WithClientPort(lport),
	// 	sipgo.WithClientNAT(), // add rport support
	// )
	// if err != nil {
	// 	return nil, err
	// }
	client := dg.getClient(&tran)
	return newRegisterTransaction(client, recipient, contactHDR, opts), nil
}

func (dg *Diago) createClient(tran Transport) (client *sipgo.Client) {
	ua := dg.ua
	// When transport is not binding to specific IP
	hostIP := tran.bindIP
	if hostIP != nil {
		if hostIP.IsUnspecified() && tran.mediaBindIP != nil {
			hostIP = tran.mediaBindIP
		}
	}

	hostname := ""
	if hostIP != nil {
		hostname = hostIP.String()
	}

	bindPort := 0
	if tran.Transport == "udp" {
		// Forcing port here makes more problem when listener is not started
		// ex register and then serve
		// We check that user started to listen port
		ports := ua.TransportLayer().ListenPorts("udp")
		if len(ports) > 0 {
			bindPort = tran.BindPort
		}
	}

	cli, err := sipgo.NewClient(ua,
		sipgo.WithClientNAT(),
		sipgo.WithClientHostname(hostname),
		sipgo.WithClientPort(bindPort),
	)
	if err != nil {
		dg.log.Error("Failed to create transport client", "error", err)
		cli, _ = sipgo.NewClient(ua) // Make some defaut
	}
	return cli
}

func (dg *Diago) DialogCacheServer() DialogCache[*DialogServerSession] {
	return dg.cache.server
}

func (dg *Diago) DialogCacheClient() DialogCache[*DialogClientSession] {
	return dg.cache.client
}
