// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync/atomic"

	"github.com/emiago/diago/media"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

var (
	ErrClientEarlyMedia = errors.New("Early media detected")
)

// DialogClientSession represents outbound channel
type DialogClientSession struct {
	*sipgo.DialogClientSession

	DialogMedia

	onReferDialog func(referDialog *DialogClientSession)

	closed atomic.Uint32
}

func (d *DialogClientSession) Close() error {
	if !d.closed.CompareAndSwap(0, 1) {
		return nil
	}
	e1 := d.DialogMedia.Close()
	e2 := d.DialogClientSession.Close()
	return errors.Join(e1, e2)
}

func (d *DialogClientSession) Id() string {
	return d.ID
}

func (d *DialogClientSession) Hangup(ctx context.Context) error {
	return d.Bye(ctx)
}

func (d *DialogClientSession) FromUser() string {
	return d.InviteRequest.From().Address.User
}

func (d *DialogClientSession) ToUser() string {
	return d.InviteRequest.To().Address.User
}

func (d *DialogClientSession) DialogSIP() *sipgo.Dialog {
	return &d.Dialog
}

func (d *DialogClientSession) RemoteContact() *sip.ContactHeader {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.remoteContactUnsafe()
}

func (d *DialogClientSession) remoteContactUnsafe() *sip.ContactHeader {
	if d.lastInvite != nil {
		// Invite update can change contact
		return d.lastInvite.Contact()
	}
	return d.InviteResponse.Contact()
}

// InviteClientOptions is passed on dialog client Invite with extra control over dialog
type InviteClientOptions struct {
	Originator DialogSession
	OnResponse func(res *sip.Response) error
	// OnMediaUpdate called when media is changed.
	// NOTE: you should not block this call as it blocks response processing.
	OnMediaUpdate func(d *DialogMedia)
	OnRefer       func(referDialog *DialogClientSession)
	// For digest authentication
	Username string
	Password string

	// Custom headers to pass. DO NOT SET THIS to nil
	Headers []sip.Header
	// Stop on early media. ErrClientEarlyMedia will be returned
	EarlyMediaDetect bool
}

// WithAnonymousCaller sets from user Anonymous per RFC
func (o *InviteClientOptions) WithAnonymousCaller() {
	o.Headers = append(o.Headers, &sip.FromHeader{
		DisplayName: "Anonymous",
		Address:     sip.Uri{User: "anonymous", Host: "anonymous.invalid"},
		Params:      sip.NewParams(),
	})
}

// WithCaller allows simpler way modifying caller
func (o *InviteClientOptions) WithCaller(displayName string, callerID string, host string) {
	o.Headers = append(o.Headers, &sip.FromHeader{
		DisplayName: displayName,
		Address:     sip.Uri{User: callerID, Host: host},
		Params:      sip.NewParams(),
	})
}

// Invite sends Invite request and establishes [early] media.
//
// Early Media Detect:
// - It RETURNS ErrClientEarlyMedia if remote answers with 183 Session in Progress
// - Media is negotiated and setuped
// - You need to call WaitAnswer() if you want to proceed with answering call
//
// Normal Answer with 200 OK (SDP)
// - You MUST call Ack() after to acknowledge session.
//
// Errors:
// - sipgo.ErrDialogResponse
// - ErrClientEarlyMedia
//
// NOTE: It updates internal invite request so NOT THREAD SAFE.
// If you pass originator it will use originator to set correct from header and avoid media transcoding
//
// Experimental: Note API may have changes
func (d *DialogClientSession) Invite(ctx context.Context, opts InviteClientOptions) error {
	sess := d.mediaSession
	inviteReq := d.InviteRequest
	originator := opts.Originator

	for _, h := range opts.Headers {
		inviteReq.AppendHeader(h)
	}

	if originator != nil {
		// In case originator then:
		// - check do we support this media formats by conf
		// - if we do, then filter and pass to dial endpoint filtered
		origInvite := originator.DialogSIP().InviteRequest
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

		// Avoid transcoding if originator present
		// Check ContentType and body present
		contType := origInvite.ContentType()
		if body := origInvite.Body(); body != nil && (contType != nil && contType.Value() == "application/sdp") {
			// apply remote SDP
			if err := sess.RemoteSDP(body); err != nil {
				return fmt.Errorf("failed to apply originator sdp: %w", err)
			}
			// We do not want originator to be remote side, but we want to apply codec filtering
			sess.SetRemoteAddr(&net.UDPAddr{})

			// Now to totally remove transcoding a chance. Leave only one codec of different types
			audioCodec := media.Codec{}
			telEventCodec := media.Codec{}

			codecs := sess.CommonCodecs()
			if len(codecs) == 0 { // No negotiation yet happened
				codecs = sess.Codecs
			}

			for _, c := range codecs {
				// TODO refactor this
				if strings.HasPrefix(c.Name, "telephone-event") {
					if telEventCodec.SampleRate == 0 {
						telEventCodec = c
					}
					continue
				}

				if audioCodec.SampleRate == 0 {
					audioCodec = c
				}
			}

			// TODO: DO we need to be thread safe here?
			// In this case we want to rewrite what should be Offered in our SDP
			// NOTE: Generally this would require Session Fork, but for now we avoid this extra step.
			sessCodecs := sess.Codecs[:0]
			if audioCodec.SampleRate != 0 {
				sessCodecs = append(sessCodecs, audioCodec)
			}

			// TODO: should we only match telephone event with same sampling rate?
			if telEventCodec.SampleRate != 0 {
				sessCodecs = append(sessCodecs, telEventCodec)
			}

			if len(sessCodecs) == 0 {
				return fmt.Errorf("no codecs support found from originator")
			}
			sess.Codecs = sessCodecs
		}
	}

	dialogCli := d.UA
	inviteReq.AppendHeader(&dialogCli.ContactHDR)
	inviteReq.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	inviteReq.SetBody(sess.LocalSDP())

	// We allow changing full from header, but we need to make sure it is correctly set
	// If users specify 'tag' parameter it is assumed that they know what they do
	if fromHDR := inviteReq.From(); fromHDR != nil && !fromHDR.Params.Has("tag") {
		fromHDR.Params["tag"] = sip.GenerateTagN(16)
	}

	// Build here request
	client := d.UA.Client
	if err := sipgo.ClientRequestBuild(client, inviteReq); err != nil {
		return err
	}

	// This only gets called after session established
	d.onMediaUpdate = opts.OnMediaUpdate
	// reuse UDP listener
	// Problem if listener is unspecified IP sipgo will not map this to listener
	// Code below only works if our bind host is specified
	// For now let SIPgo create 1 UDP connection and it will reuse it
	// via := inviteReq.Via()
	// if via.Host == "" {
	// }
	err := d.DialogClientSession.Invite(ctx, func(c *sipgo.Client, req *sip.Request) error {
		// Do nothing
		return nil
	})
	if err != nil {
		// sess.Close()
		return err
	}
	ansOpts := sipgo.AnswerOptions{
		Username:   opts.Username,
		Password:   opts.Password,
		OnResponse: opts.OnResponse,
	}

	if opts.EarlyMediaDetect {
		return d.waitAnswerEarly(ctx, ansOpts)
	}

	return d.waitAnswer(ctx, ansOpts)
}

// WaitAnswer waits dialog on answer. It should only be used if you have error Invite but still want to continue
// ex. ErrClientEarlyMedia was returned but you want to proceed with answering
func (d *DialogClientSession) WaitAnswer(ctx context.Context, opts sipgo.AnswerOptions) error {
	return d.waitAnswer(ctx, opts)
}

func (d *DialogClientSession) waitAnswerEarly(ctx context.Context, opts sipgo.AnswerOptions) error {
	sess := d.mediaSession
	onResps := opts.OnResponse

	// Add early media check
	opts.OnResponse = func(res *sip.Response) error {
		// https://datatracker.ietf.org/doc/html/rfc3261#section-8.1.3.2
		// 		UAC MUST treat any provisional response different than 100 that it
		//    does not recognize as 183 (Session Progress).
		// Check any existing
		if onResps != nil {
			if err := onResps(res); err != nil {
				return err
			}
		}

		// handle 183 Session Progress early media
		if res.StatusCode != sip.StatusSessionInProgress {
			return nil
		}

		if cont := res.ContentType(); cont == nil || cont.Value() != "application/sdp" {
			return nil
		}

		remoteSDP := res.Body()
		if remoteSDP == nil {
			return nil
		}

		if err := sess.RemoteSDP(remoteSDP); err != nil {
			return err
		}

		rtpSess := media.NewRTPSession(sess)
		d.mu.Lock()
		d.initRTPSessionUnsafe(sess, rtpSess)
		d.onCloseUnsafe(func() error {
			return rtpSess.Close()
		})
		d.mu.Unlock()

		// Must be called after reader and writer setup due to race
		if err := rtpSess.MonitorBackground(); err != nil {
			return err
		}

		return ErrClientEarlyMedia
	}
	return d.waitAnswer(ctx, opts)
}

func (d *DialogClientSession) waitAnswer(ctx context.Context, opts sipgo.AnswerOptions) error {
	if err := d.DialogClientSession.WaitAnswer(ctx, opts); err != nil {
		return err
	}

	if err := d.applyRemoteSDP(); err != nil {
		// Terminate call. Call must be ACK before doing BYE
		if err := d.Ack(ctx); err != nil {
			return errors.Join(err, d.Ack(ctx))
		}
		return errors.Join(err, d.Bye(ctx))
	}
	return nil
}

func (d *DialogClientSession) applyRemoteSDP() error {
	sess := d.mediaSession
	remoteSDP := d.InviteResponse.Body()
	if remoteSDP == nil {
		return fmt.Errorf("no SDP in response")
	}

	// Apply SDP on existing (Early) media if it exists
	if err := d.checkEarlyMedia(remoteSDP); err != errNoRTPSession {
		return err
	}

	if err := sess.RemoteSDP(remoteSDP); err != nil {
		return err
	}

	// Create RTP session. After this no media session configuration should be changed
	rtpSess := media.NewRTPSession(sess)
	d.mu.Lock()
	d.initRTPSessionUnsafe(sess, rtpSess)
	d.onCloseUnsafe(func() error {
		return rtpSess.Close()
	})
	d.mu.Unlock()

	// Must be called after reader and writer setup due to race
	return rtpSess.MonitorBackground()
}

// Ack acknowledgeds media
// Before Ack normally you want to setup more stuff like bridging
func (d *DialogClientSession) Ack(ctx context.Context) error {
	return d.ack(ctx, nil)
}

// AckLate sends ACK with media. Use this in combination with late(delay) offer
// func (d *DialogClientSession) AckLate(ctx context.Context) error {
// 	return d.ack(ctx, d.mediaSession.LocalSDP())
// }

func (d *DialogClientSession) ack(ctx context.Context, body []byte) error {
	inviteRequest := d.InviteRequest
	recipient := &inviteRequest.Recipient
	if contact := d.InviteResponse.Contact(); contact != nil {
		recipient = &contact.Address
	}
	ackRequest := sip.NewRequest(
		sip.ACK,
		*recipient.Clone(),
	)

	if body != nil {
		// This is delayed offer
		ackRequest.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
		ackRequest.SetBody(body)
	}

	if err := d.DialogClientSession.WriteAck(ctx, ackRequest); err != nil {
		return err
	}

	// Now dialog is established and can be add into store
	// if err := DialogsClientCache.DialogStore(ctx, d.ID, d); err != nil {
	// 	return err
	// }
	// d.OnClose(func() error {
	// 	return DialogsClientCache.DialogDelete(context.Background(), d.ID)
	// })
	return nil
}

// ReInvite sends new invite based on current media session
func (d *DialogClientSession) ReInvite(ctx context.Context) error {
	d.mu.Lock()
	sdp := d.mediaSession.LocalSDP()
	contact := d.remoteContactUnsafe()
	d.mu.Unlock()
	req := sip.NewRequest(sip.INVITE, contact.Address)
	req.AppendHeader(d.InviteRequest.Contact())
	req.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	req.SetBody(sdp)

	res, err := d.Do(ctx, req)
	if err != nil {
		return err
	}

	if !res.IsSuccess() {
		return sipgo.ErrDialogResponse{
			Res: res,
		}
	}

	cont := res.Contact()
	if cont == nil {
		return fmt.Errorf("no contact header present")
	}

	ack := sip.NewRequest(sip.ACK, cont.Address)
	return d.WriteRequest(ack)
}

// Refer tries todo refer (blind transfer) on call
// TODO: not complete
func (d *DialogClientSession) Refer(ctx context.Context, referTo sip.Uri) error {
	cont := d.InviteResponse.Contact()
	return dialogRefer(ctx, d, cont.Address, referTo)
}

func (d *DialogClientSession) handleReferNotify(req *sip.Request, tx sip.ServerTransaction) {
	dialogHandleReferNotify(d, req, tx)
}

func (d *DialogClientSession) handleRefer(dg *Diago, req *sip.Request, tx sip.ServerTransaction) {
	d.mu.Lock()
	onRefDialog := d.onReferDialog
	d.mu.Unlock()
	if onRefDialog == nil {
		tx.Respond(sip.NewResponseFromRequest(req, sip.StatusNotAcceptable, "Not Acceptable", nil))
		return
	}

	dialogHandleRefer(d, dg, req, tx, onRefDialog)
}

func (d *DialogClientSession) handleReInvite(req *sip.Request, tx sip.ServerTransaction) error {
	if err := d.ReadRequest(req, tx); err != nil {
		return tx.Respond(sip.NewResponseFromRequest(req, sip.StatusBadRequest, "Bad Request - "+err.Error(), nil))
	}

	return d.handleMediaUpdate(req, tx, d.InviteRequest.Contact())
}

func (d *DialogClientSession) readSIPInfoDTMF(req *sip.Request, tx sip.ServerTransaction) error {
	return tx.Respond(sip.NewResponseFromRequest(req, sip.StatusNotAcceptable, "Not Acceptable", nil))
}
