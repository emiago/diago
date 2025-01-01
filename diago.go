// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"strconv"
	"time"

	"github.com/emiago/diago/media"
	"github.com/emiago/diago/media/sdp"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
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

	auth      sipgo.DigestAuth
	mediaConf MediaConfig

	log zerolog.Logger
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

	// Transport must be udp,tcp or ws.
	Transport string
	BindHost  string
	BindPort  int
	bindIP    net.IP

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
				dg.log.Error().Err(err).Msg("failed to resolve real IP")
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

		// we want to handle SIP networking better per each transport
		t.client = dg.createClient(t)
		dg.transports = append(dg.transports, t)
	}
}

type MediaConfig struct {
	Formats sdp.Formats

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

func WithLogger(l zerolog.Logger) DiagoOption {
	return func(dg *Diago) {
		dg.log = l
	}
}

// NewDiago construct b2b user agent that will act as server and client
func NewDiago(ua *sipgo.UserAgent, opts ...DiagoOption) *Diago {
	dg := &Diago{
		ua:  ua,
		log: log.Logger,
		serveHandler: func(d *DialogServerSession) {
			fmt.Println("Serve Handler not implemented")
		},
		transports: []Transport{},
		mediaConf: MediaConfig{
			Formats: sdp.NewFormats(sdp.FORMAT_TYPE_ULAW, sdp.FORMAT_TYPE_ALAW, sdp.FORMAT_TYPE_TELEPHONE_EVENT),
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
	server.OnInvite(func(req *sip.Request, tx sip.ServerTransaction) {
		// What if multiple server transports?
		id, err := sip.UASReadRequestDialogID(req)
		if err == nil {
			dg.handleReInvite(req, tx, id)
			return
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
			dg.log.Error().Err(err).Msg("Handling new INVITE failed")
			return
		}

		// TODO authentication
		dWrap := &DialogServerSession{
			DialogServerSession: dialog,
			DialogMedia:         DialogMedia{},
			// TODO we may actually just build media session with this conf here
			mediaConf: MediaConfig{
				Formats:    dg.mediaConf.Formats,
				bindIP:     tran.mediaBindIP,
				externalIP: tran.MediaExternalIP,
			},
		}

		defer dWrap.Close()

		DialogsServerCache.DialogStore(dWrap.Context(), dWrap.ID, dWrap)
		defer func() {
			// TODO: have better context
			DialogsServerCache.DialogDelete(context.Background(), dWrap.ID)
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
				return
			}

			dg.log.Error().Err(err).Msg("Hanguping call failed")
			return
		}
	})

	server.OnCancel(func(req *sip.Request, tx sip.ServerTransaction) {
		// INVITE transaction should be terminated by transaction layer and 200 response will be sent
		// In case of stateless proxy this we would need to forward
		tx.Respond(sip.NewResponseFromRequest(req, sip.StatusCallTransactionDoesNotExists, "Call/Transaction Does Not Exist", nil))
	})

	server.OnAck(func(req *sip.Request, tx sip.ServerTransaction) {
		d, err := MatchDialogServer(req)
		if err != nil {
			// Normally ACK will be received if some out of dialog request is received or we responded negatively
			// tx.Respond(sip.NewResponseFromRequest(req, sip.StatusBadRequest, err.Error(), nil))
			return
		}

		if err := d.ReadAck(req, tx); err != nil {
			dg.log.Error().Err(err).Msg("ACK finished with error")
			// Do not respond bad request as client will DOS on any non 2xx response
			return
		}
	})

	server.OnBye(func(req *sip.Request, tx sip.ServerTransaction) {
		sd, cd, err := MatchDialog(req)
		if err != nil {
			if errors.Is(err, sipgo.ErrDialogDoesNotExists) {
				tx.Respond(sip.NewResponseFromRequest(req, sip.StatusCallTransactionDoesNotExists, err.Error(), nil))
				return

			}
			tx.Respond(sip.NewResponseFromRequest(req, sip.StatusBadRequest, err.Error(), nil))
			return
		}

		// Respond to BYE
		// Terminate our media processing
		// As user may stuck in playing or reading media, this unblocks that goroutine
		if cd != nil {
			if err := cd.ReadBye(req, tx); err != nil {
				dg.log.Error().Err(err).Msg("failed to read bye for client dialog")
			}

			cd.DialogMedia.Close()
			return
		}

		if err := sd.ReadBye(req, tx); err != nil {
			dg.log.Error().Err(err).Msg("failed to read bye for server dialog")
		}
		sd.DialogMedia.Close()
	})

	server.OnInfo(func(req *sip.Request, tx sip.ServerTransaction) {
		// Handle DTMF out of band
		if req.ContentType().Value() != "application/dtmf-relay" {
			tx.Respond(sip.NewResponseFromRequest(req, sip.StatusNotAcceptable, "Not Acceptable", nil))
			return
		}

		sd, cd, err := MatchDialog(req)
		if err != nil {
			if errors.Is(err, sipgo.ErrDialogDoesNotExists) {
				tx.Respond(sip.NewResponseFromRequest(req, sip.StatusCallTransactionDoesNotExists, err.Error(), nil))
				return

			}
			tx.Respond(sip.NewResponseFromRequest(req, sip.StatusBadRequest, err.Error(), nil))
			return
		}

		if cd != nil {
			cd.readSIPInfoDTMF(req, tx)
			return
		}
		sd.readSIPInfoDTMF(req, tx)

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

	})

	// TODO deal with OPTIONS more correctly
	// For now leave it for keep alive
	dg.server.OnOptions(func(req *sip.Request, tx sip.ServerTransaction) {
		res := sip.NewResponseFromRequest(req, sip.StatusOK, "OK", nil)
		if err := tx.Respond(res); err != nil {
			log.Error().Err(err).Msg("OPTIONS 200 failed to respond")
		}
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

func (dg *Diago) handleReInvite(req *sip.Request, tx sip.ServerTransaction, id string) {
	ctx := context.TODO()
	// No Error means we have ID
	s, err := DialogsServerCache.DialogLoad(ctx, id)
	if err != nil {
		id, err := sip.UACReadRequestDialogID(req)
		if err != nil {
			tx.Respond(sip.NewResponseFromRequest(req, sip.StatusBadRequest, "Bad Request", nil))
			return

		}
		// No Error means we have ID
		s, err := DialogsClientCache.DialogLoad(ctx, id)
		if err != nil {
			tx.Respond(sip.NewResponseFromRequest(req, sip.StatusCallTransactionDoesNotExists, "Call/Transaction Does Not Exist", nil))
			return
		}

		s.handleReInvite(req, tx)
		return
	}

	s.handleReInvite(req, tx)
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

				log.Info().Str("addr", addr).Str("protocol", tran.Transport).Msg("Listening on transport")
			}))

			if tran.TLSConf != nil {
				errCh <- server.ListenAndServeTLS(ctx, tran.Transport, hostport, tran.TLSConf)
				return
			}
			errCh <- server.ListenAndServe(ctx, tran.Transport, hostport)
		}(i, tran)
	}

	// Returns first error
	return <-errCh
	// }

	// tran := dg.transports[0]
	// hostport := net.JoinHostPort(tran.BindHost, strconv.Itoa(tran.BindPort))
	// log.Info().Str("addr", hostport).Str("protocol", tran.Transport).Msg("Listening on transport")
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
			dg.log.Info().Msg("Network ready")
		}
	}
	return nil
}

// HandleFunc registers you handler function for dialog. Must be called before serving request
func (dg *Diago) HandleFunc(f ServeDialogFunc) {
	dg.serveHandler = f
}

type InviteOptions struct {
	OnResponse   func(res *sip.Response) error
	OnRTPSession func(rtpSess *media.RTPSession)
	// For digest authentication
	Username string
	Password string

	// Custom headers to pass. DO NOT SET THIS to nil
	Headers     []sip.Header
	TransportID string
}

// func (o InviteOptions) SetCaller(displayName string, callerID string) {
// 	o.Headers = append(o.Headers, &sip.FromHeader{
// 		DisplayName: displayName,
// 		Address:     sip.Uri{User: callerID, Host: },
// 	})
// }

// Sets from user to RFC anonymous
func (o *InviteOptions) SetAnonymousCaller() {
	o.Headers = append(o.Headers, &sip.FromHeader{
		DisplayName: "Anonymous",
		Address:     sip.Uri{User: "anonymous", Host: "anonymous.invalid"},
		Params:      sip.NewParams(),
	})
}

func (o *InviteOptions) SetCaller(displayName string, callerID string) {
	o.Headers = append(o.Headers, &sip.FromHeader{
		DisplayName: displayName,
		Address:     sip.Uri{User: callerID, Host: ""},
		Params:      sip.NewParams(),
	})
}

// Invite makes outgoing call leg and waits for answer.
// If you want to bridge call then use helper InviteBridge
func (dg *Diago) Invite(ctx context.Context, recipient sip.Uri, opts InviteOptions) (d *DialogClientSession, err error) {
	return dg.InviteBridge(ctx, recipient, nil, opts)
}

// InviteBridge makes outgoing call leg and does bridging.
// Outgoing session will be added into bridge on answer
// If bridge has Originator (first participant) it will be used for creating outgoing call leg as in B2BUA
// When bridge is provided then this call will be bridged with any participant already present in bridge
func (dg *Diago) InviteBridge(ctx context.Context, recipient sip.Uri, bridge *Bridge, opts InviteOptions) (d *DialogClientSession, err error) {
	transport := "udp"
	if recipient.UriParams != nil {
		if t := recipient.UriParams["transport"]; t != "" {
			transport = t
		}
	}

	tran, exists := dg.findTransport(opts.TransportID, transport)
	if !exists {
		return nil, fmt.Errorf("transport %s does not exists", transport)
	}

	// TODO: remove this alloc of UA each time
	client := dg.getClient(&tran)
	dialogCli := sipgo.DialogUA{
		Client:         client,
		RewriteContact: tran.RewriteContact,
	}
	dg.contactHDRFromTransport(tran, &dialogCli.ContactHDR)

	d = &DialogClientSession{}

	// Create media
	// TODO explicit media format passing
	mediaConf := MediaConfig{
		Formats:    dg.mediaConf.Formats,
		bindIP:     tran.mediaBindIP,
		externalIP: tran.MediaExternalIP,
	}
	if err := d.initMediaSessionFromConf(mediaConf); err != nil {
		return nil, err
	}
	sess := d.mediaSession

	inviteReq := sip.NewRequest(sip.INVITE, recipient)
	inviteReq.SetTransport(sip.NetworkToUpper(transport))

	for _, h := range opts.Headers {
		inviteReq.AppendHeader(h)
	}

	// Are we bridging?
	if bridge != nil {
		if omed := bridge.Originator; omed != nil {
			// In case originator then:
			// - check do we support this media formats by conf
			// - if we do, then filter and pass to dial endpoint filtered
			origInvite := omed.DialogSIP().InviteRequest
			if fromHDR := inviteReq.From(); fromHDR == nil {
				// From header should be preserved from originator
				fromHDROrig := origInvite.From()
				f := sip.FromHeader{
					DisplayName: fromHDROrig.DisplayName,
					Address:     *fromHDROrig.Address.Clone(),
					Params:      fromHDROrig.Params.Clone(),
				}
				inviteReq.AppendHeader(&f)
			}

			sd := sdp.SessionDescription{}
			if err := sdp.Unmarshal(origInvite.Body(), &sd); err != nil {
				return nil, err
			}
			md, err := sd.MediaDescription("audio")
			if err != nil {
				return nil, err
			}

			// Check do we support this formats, and filter first that we support
			// Limiting to one format we remove need for transcoding
			singleFormat := ""
		outloop:
			for _, f := range md.Formats {
				for _, sf := range dg.mediaConf.Formats {
					if f == sf {
						singleFormat = f
						break outloop
					}
				}
			}

			if singleFormat == "" {
				return nil, fmt.Errorf("no audio media is supported from originator")
			}

			// Safe to update until we start using in rtp session
			sess.Formats = []string{singleFormat}

			// Unless caller is customizing response handling we want to answer caller on
			// callerState := omed.DialogSIP().LoadState()
			// if opts.OnResponse == nil {
			// 	opts.OnResponse = func(res *sip.Response) error {
			// 		if res.StatusCode == 200 {
			// 			switch om := omed.(type) {
			// 			case *DialogClientSession:
			// 			case *DialogServerSession:
			// 				return om.answerWebrtc([]string{})
			// 			}
			// 		}
			// 		return nil
			// 	}
			// }
		}
	}

	inviteReq.AppendHeader(&dialogCli.ContactHDR)
	inviteReq.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	inviteReq.SetBody(sess.LocalSDP())

	// We allow changing full from header, but we need to make sure it is correctly set
	if fromHDR := inviteReq.From(); fromHDR != nil {
		fromHDR.Params["tag"] = sip.GenerateTagN(16)
		if fromHDR.Address.Host == "" { // IN case caller is set but not hostname
			fromHDR.Address.Host = dg.ua.Hostname()
		}
	}

	// Build here request
	if err := sipgo.ClientRequestBuild(client, inviteReq); err != nil {
		return nil, err
	}

	// reuse UDP listener
	// Problem if listener is unspecified IP sipgo will not map this to listener
	// Code below only works if our bind host is specified
	// For now let SIPgo create 1 UDP connection and it will reuse it
	// via := inviteReq.Via()
	// if via.Host == "" {
	// }
	dialog, err := dialogCli.WriteInvite(ctx, inviteReq, func(c *sipgo.Client, req *sip.Request) error {
		// Do nothing
		return nil
	})
	if err != nil {
		sess.Close()
		return nil, err
	}
	d.DialogClientSession = dialog

	waitCall := func(ctx context.Context) error {
		callID := inviteReq.CallID().Value()
		log.Info().Str("call_id", callID).Msg("Waiting answer")
		answO := sipgo.AnswerOptions{
			Username:   opts.Username,
			Password:   opts.Password,
			OnResponse: opts.OnResponse,
		}

		if err := dialog.WaitAnswer(ctx, answO); err != nil {
			return err
		}

		remoteSDP := dialog.InviteResponse.Body()
		if remoteSDP == nil {
			return fmt.Errorf("no SDP in response")
		}
		if err := sess.RemoteSDP(remoteSDP); err != nil {
			return err
		}

		// Create RTP session. After this no media session configuration should be changed
		rtpSess := media.NewRTPSession(sess)
		if opts.OnRTPSession != nil {
			opts.OnRTPSession(rtpSess)
		}

		d.mu.Lock()
		d.initRTPSessionUnsafe(sess, rtpSess)
		d.onCloseUnsafe(func() {
			if err := rtpSess.Close(); err != nil {
				log.Error().Err(err).Msg("Closing session")
			}
		})
		d.mu.Unlock()
		log.Debug().Str("laddr", sess.Laddr.String()).Str("raddr", sess.Raddr.String()).Msg("RTP Session setuped")

		// Must be called after reader and writer setup due to race
		rtpSess.MonitorBackground()

		// Add to bridge as early media. This may need to be moved earlier to handlin ringback tones
		// but normally callee should not send any other media before receving ack.
		if bridge != nil {
			if err := bridge.AddDialogSession(d); err != nil {
				return err
			}
		}

		if err := dialog.Ack(ctx); err != nil {
			return err
		}

		if err := DialogsClientCache.DialogStore(ctx, d.ID, d); err != nil {
			return err
		}
		d.OnClose(func() {
			DialogsClientCache.DialogDelete(context.Background(), d.ID)
		})
		return nil
	}

	if err := waitCall(ctx); err != nil {
		d.Close()
		return nil, err
	}

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

func (dg *Diago) findTransport(id string, transport string) (Transport, bool) {
	if id != "" {
		for _, t := range dg.transports {
			if id == t.ID {
				return t, true
			}
		}
		return Transport{}, false
	}

	return dg.getTransport(transport)
}

type RegisterOptions struct {
	// Digest auth
	Username string
	Password string
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
			log.Error().Err(err).Msg("Fail to unregister")
			return
		}
		dg.log.Debug().Msg("Unregister successfull")
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
		dg.log.Error().Err(err).Msg("Failed to create transport client")
		cli, _ = sipgo.NewClient(ua) // Make some defaut
	}
	return cli
}
