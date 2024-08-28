// SPDX-License-Identifier: MPL-2.0
// Copyright (C) 2024 Emir Aganovic

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
	"github.com/emiago/sipgox"
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

	ExternalHost string // SIP signaling and media external addr
	ExternalPort int
	// ExternalMediaAddr string // External media addr

	// In case TLS protocol
	TLSConf *tls.Config
}

func WithTransport(t Transport) DiagoOption {
	return func(dg *Diago) {
		if t.ExternalHost == "" {
			t.ExternalHost = t.BindHost
			// If unspecified IP???
		}
		if t.ExternalPort == 0 {
			t.ExternalPort = t.BindPort
		}

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

func WithServer(srv *sipgo.Server) DiagoOption {
	return func(dg *Diago) {
		dg.server = srv
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
			Formats: sdp.NewFormats(sdp.FORMAT_TYPE_ULAW, sdp.FORMAT_TYPE_ALAW),
		},
	}

	for _, o := range opts {
		o(dg)
	}

	if dg.client == nil {
		dg.client, _ = sipgo.NewClient(ua)
	}

	if dg.server == nil {
		dg.server, _ = sipgo.NewServer(ua)
	}

	if len(dg.transports) == 0 {
		dg.transports = append(dg.transports, Transport{
			Transport:    "udp",
			BindHost:     "127.0.0.1",
			BindPort:     5060,
			ExternalHost: "127.0.0.1",
			ExternalPort: 5060,
		})
	}

	// Create our default contact hdr
	contactHDR := dg.getContactHDR("")

	// dg.dialogServer = sipgo.NewDialogServer(dg.client, contactHDR)
	// dg.dialogClient = sipgo.NewDialogClient(dg.client, contactHDR)

	server := dg.server
	server.OnInvite(func(req *sip.Request, tx sip.ServerTransaction) {
		// What if multiple server transports?
		id, err := sip.UASReadRequestDialogID(req)
		if err == nil {
			dg.handleReInvite(req, tx, id)
			return
		}

		// Proceed as new call
		dialogUA := sipgo.DialogUA{
			Client:     dg.client,
			ContactHDR: contactHDR,
		}

		dialog, err := dialogUA.ReadInvite(req, tx)
		if err != nil {
			dg.log.Error().Err(err).Msg("Handling new INVITE failed")
			return
		}

		// Check is this dialog in cache
		DialogsServerCache.Load(dialog.ID)

		// if dialog.LoadState() == sip.DialogStateConfirmed {
		// 	// This is probably REINVITE for media path update
		// }

		// dialog.Close
		// We will close dialog with our wrapper below

		// TODO authentication
		// TODO media and SDP
		dWrap := &DialogServerSession{
			DialogServerSession: dialog,
			DialogMedia:         DialogMedia{},

			// contactHDR: dg.getContactHDR(req.Transport()),
			// formats:    dg.mediaConf.Formats,
		}
		dg.initServerSession(dWrap)
		defer dWrap.Close()

		DialogsServerCache.Store(dWrap.ID, dWrap)
		defer DialogsServerCache.Delete(dWrap.ID)

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
		d, err := MatchDialogServer(req)
		if err != nil {
			// if errors.Is(err, sipgo.ErrDialogDoesNotExists) {
			// 	_, err := MatchDialogClient(req)
			// 	if errors.Is(err, sipgo.ErrDialogDoesNotExists) {
			// 		tx.Respond(sip.NewResponseFromRequest(req, sip.StatusCallTransactionDoesNotExists, err.Error(), nil))
			// 		return
			// 	}

			// 	if err != nil {
			// 		// Security? When to answer this?
			// 		tx.Respond(sip.NewResponseFromRequest(req, sip.StatusBadRequest, err.Error(), nil))
			// 		return
			// 	}
			// 	return
			// }

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
		d, err := MatchDialogServer(req)
		if err != nil {
			if errors.Is(err, sipgo.ErrDialogDoesNotExists) {
				cd, err := MatchDialogClient(req)
				if err != nil {
					// Security? When to answer this?
					tx.Respond(sip.NewResponseFromRequest(req, sip.StatusBadRequest, err.Error(), nil))
					return
				}

				if err := cd.ReadBye(req, tx); err != nil {
					tx.Respond(sip.NewResponseFromRequest(req, sip.StatusBadRequest, err.Error(), nil))
				}
			}

			tx.Respond(sip.NewResponseFromRequest(req, sip.StatusBadRequest, err.Error(), nil))
			return
		}

		if err := d.ReadBye(req, tx); err != nil {
			tx.Respond(sip.NewResponseFromRequest(req, sip.StatusBadRequest, err.Error(), nil))
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
	// No Error means we have ID
	val, ok := DialogsServerCache.Load(id)
	if !ok {
		id, err := sip.UACReadRequestDialogID(req)
		if err != nil {
			tx.Respond(sip.NewResponseFromRequest(req, sip.StatusBadRequest, "Bad Request", nil))
			return

		}
		// No Error means we have ID
		val, ok := DialogsClientCache.Load(id)
		if !ok {
			tx.Respond(sip.NewResponseFromRequest(req, sip.StatusCallTransactionDoesNotExists, "Call/Transaction Does Not Exist", nil))
			return
		}

		s := val.(*DialogClientSession)
		s.handleReInvite(req, tx)
		return
	}

	s := val.(*DialogServerSession)
	s.handleReInvite(req, tx)
}

func (dg *Diago) initServerSession(d *DialogServerSession) {
	d.contactHDR = dg.getContactHDR(d.InviteRequest.Transport())
	d.formats = dg.mediaConf.Formats
}

func (dg *Diago) Serve(ctx context.Context, f ServeDialogFunc) error {
	server := dg.server
	dg.serveHandler = f

	// For multi transports start multi server
	if len(dg.transports) > 1 {
		errCh := make(chan error, len(dg.transports))
		for _, tran := range dg.transports {
			hostport := net.JoinHostPort(tran.BindHost, strconv.Itoa(tran.BindPort))
			go func(tran Transport) {
				if tran.TLSConf != nil {
					errCh <- server.ListenAndServeTLS(ctx, tran.Transport, hostport, tran.TLSConf)
					return
				}

				errCh <- server.ListenAndServe(ctx, tran.Transport, hostport)
			}(tran)
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

type InviteOptions struct {
	OnResponse func(res *sip.Response) error

	// For digest authentication
	Username string
	Password string

	Headers []sip.Header
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
// TODO:
// - transcoding will not be allowed -> error will be returned
func (dg *Diago) InviteBridge(ctx context.Context, recipient sip.Uri, bridge *Bridge, opts InviteOptions) (d *DialogClientSession, err error) {
	transport := "udp"
	if recipient.UriParams != nil {
		if t := recipient.UriParams["transport"]; t != "" {
			transport = t
		}
	}

	dialogCli := sipgo.DialogUA{
		Client:     dg.client,
		ContactHDR: dg.getContactHDR(transport),
	}

	// Now media SEdgP
	// TODO this probably needs take in account Contact header or listen addr
	ip, port, err := sipgox.FindFreeInterfaceHostPort(transport, "")
	if err != nil {
		return nil, err
	}
	laddr := &net.UDPAddr{IP: ip, Port: port}
	sess, err := media.NewMediaSession(laddr)
	if err != nil {
		return nil, err
	}
	sess.Formats = dg.mediaConf.Formats

	dialHDRS := append(opts.Headers, sip.NewHeader("Content-Type", "application/sdp"))

	// Are we bridging?
	if bridge != nil {
		if omed := bridge.Originator; omed != nil {
			// In case originator then:
			// - check do we support this media formats by conf
			// - if we do, then filter and pass to dial endpoint filtered
			inviteReq := omed.DialogSIP().InviteRequest
			fromHDROrig := inviteReq.From()
			fromHDR := sip.FromHeader{
				DisplayName: fromHDROrig.DisplayName,
				Address:     *fromHDROrig.Address.Clone(),
				Params:      fromHDROrig.Params.Clone(),
			}
			fromHDR.Params["tag"] = sip.GenerateTagN(16)

			// From header should be preserved from originator
			dialHDRS = append(dialHDRS, &fromHDR)

			sd := sdp.SessionDescription{}
			if err := sdp.Unmarshal(inviteReq.Body(), &sd); err != nil {
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

	rtpSess := media.NewRTPSession(sess)
	rtpSess.MonitorBackground()

	dialog, err := dialogCli.Invite(ctx, recipient, sess.LocalSDP(), dialHDRS...)
	if err != nil {
		sess.Close()
		return nil, err
	}

	d = &DialogClientSession{
		DialogClientSession: dialog,
	}

	// Set media Session
	d.mu.Lock()
	d.mediaSession = sess
	d.RTPPacketReader = media.NewRTPPacketReaderSession(rtpSess)
	d.RTPPacketWriter = media.NewRTPPacketWriterSession(rtpSess)
	d.mu.Unlock()

	waitCall := func() error {
		log.Info().Msg("Waiting answer")
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

func (dg *Diago) getContactHDR(transport string) sip.ContactHeader {
	// Find contact hdr matching transport
	tran := dg.transports[0]

	for _, t := range dg.transports[1:] {
		if sip.NetworkToLower(transport) == t.Transport {
			tran = t
		}
	}

	scheme := "sip"
	if tran.TLSConf != nil {
		scheme = "sips"
	}
	return sip.ContactHeader{
		DisplayName: "", // TODO
		Address: sip.Uri{
			Scheme:    scheme,
			User:      dg.ua.Name(),
			Host:      tran.ExternalHost,
			Port:      tran.ExternalPort,
			UriParams: sip.NewParams(),
			Headers:   sip.NewParams(),
		},
	}
}

type RegisterRequest struct {
	RegisterURI sip.Uri
	sipgox.RegisterOptions
}

func (d *Diago) Register(ctx context.Context, req RegisterRequest) error {
	if len(d.transports) == 0 {
		return fmt.Errorf("No transports defined")
	}
	t := d.transports[0]
	contHDR := sip.ContactHeader{
		Address: sip.Uri{
			Host: t.ExternalHost,
			Port: t.ExternalPort,
		},
	}

	// client := d.client
	registerCtx := sipgox.NewRegisterTransaction(
		d.log,
		d.client,
		req.RegisterURI,
		contHDR,
		req.RegisterOptions,
	)

	if err := registerCtx.Register(ctx); err != nil {
		return err
	}

	return registerCtx.QualifyLoop(ctx)
}

// InviteWebrtc dials endpoint with webrtc stack
func (dg *Diago) InviteWebrtc(ctx context.Context, recipient sip.Uri, opts sipgo.AnswerOptions) (d *DialogClientSession, err error) {

	return nil, fmt.Errorf("not implemented yet")
}
