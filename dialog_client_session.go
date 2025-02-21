// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"context"
	"fmt"
	"strings"
	"sync/atomic"

	"github.com/emiago/diago/media"
	"github.com/emiago/diago/media/sdp"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/rs/zerolog/log"
)

// DialogClientSession represents outbound channel
type DialogClientSession struct {
	*sipgo.DialogClientSession

	DialogMedia

	onReferDialog func(referDialog *DialogClientSession)

	closed atomic.Uint32
}

func (d *DialogClientSession) Close() {
	if !d.closed.CompareAndSwap(0, 1) {
		return
	}
	d.DialogMedia.Close()
	d.DialogClientSession.Close()
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

type InviteClientOptions struct {
	Originator DialogSession
	OnResponse func(res *sip.Response) error
	// OnMediaUpdate called when media is changed. NOTE: you should not block this call
	OnMediaUpdate func(d *DialogMedia)
	OnRefer       func(referDialog *DialogClientSession)
	// OnPreInvite   func(inviteReq *sip.Request)
	// For digest authentication
	Username string
	Password string

	// Custom headers to pass. DO NOT SET THIS to nil
	Headers []sip.Header
}

// Invite sends Invite request and establishes early media.
// NOTE: You must call Ack after to acknowledge session.
// NOTE: It updates internal invite request so NOT THREAD SAFE.
// If you pass originator it will use originator to set correct from header and avoid media transcoding
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

		// Avoid transcoding if originator answered
		// If not pass full SDP, but then client should negotiate common
		if originator.DialogSIP().LoadState() >= sip.DialogStateEstablished {
			// Check ContentType
			contType := origInvite.ContentType()
			if body := origInvite.Body(); body != nil && (contType != nil && contType.Value() == "application/sdp") {
				sd := sdp.SessionDescription{}
				if err := sdp.Unmarshal(origInvite.Body(), &sd); err != nil {
					return err
				}
				md, err := sd.MediaDescription("audio")
				if err != nil {
					return err
				}

				codecs := make([]media.Codec, len(md.Formats))
				attrs := sd.Values("a")
				n, err := media.CodecsFromSDPRead(md.Formats, attrs, codecs)
				if err != nil {

					// if n == 0 {
					// 	return err
					// }
					// We will be here strict for now. We want to have correct SDP parsing
					return err
				}
				if n == 0 {
					return fmt.Errorf("no codecs found in originator SDP")
				}
				codecs = codecs[:n]

				// Avoid transcoding--
				audioCodec := media.Codec{}
				telEventCodec := media.Codec{}

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
				// TODO: Should we honor formats set on this session?
				sessCodecs := sess.Codecs[:0]
				if audioCodec.SampleRate != 0 {
					sessCodecs = append(sessCodecs, audioCodec)
				}

				if telEventCodec.SampleRate != 0 {
					sessCodecs = append(sessCodecs, telEventCodec)
				}

				if len(sessCodecs) == 0 {
					return fmt.Errorf("no codecs support found from originator")
				}
				sess.Codecs = sessCodecs
			}
		}
	}

	dialogCli := d.UA
	inviteReq.AppendHeader(&dialogCli.ContactHDR)
	inviteReq.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	inviteReq.SetBody(sess.LocalSDP())

	// We allow changing full from header, but we need to make sure it is correctly set
	if fromHDR := inviteReq.From(); fromHDR != nil {
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

	return d.waitAnswer(ctx, sipgo.AnswerOptions{
		Username:   opts.Username,
		Password:   opts.Password,
		OnResponse: opts.OnResponse,
	})
}

// InviteLate does not send SDP offer
// NOTE: call AckLate to complete negotiation
// func (d *DialogClientSession) InviteLate(ctx context.Context, opts InviteOptions) error {

// }

func (d *DialogClientSession) waitAnswer(ctx context.Context, opts sipgo.AnswerOptions) error {
	sess := d.mediaSession
	callID := d.InviteRequest.CallID().Value()

	log.Info().Str("call_id", callID).Msg("Waiting answer")
	if err := d.WaitAnswer(ctx, opts); err != nil {
		return err
	}

	remoteSDP := d.InviteResponse.Body()
	if remoteSDP == nil {
		return fmt.Errorf("no SDP in response")
	}
	if err := sess.RemoteSDP(remoteSDP); err != nil {
		return err
	}

	// Create RTP session. After this no media session configuration should be changed
	rtpSess := media.NewRTPSession(sess)
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
	if err := rtpSess.MonitorBackground(); err != nil {
		return err
	}
	return nil
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
	if err := DialogsClientCache.DialogStore(ctx, d.ID, d); err != nil {
		return err
	}
	d.OnClose(func() {
		DialogsClientCache.DialogDelete(context.Background(), d.ID)
	})
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
	return nil
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

	return d.handleMediaUpdate(req, tx)
}

func (d *DialogClientSession) readSIPInfoDTMF(req *sip.Request, tx sip.ServerTransaction) error {
	return tx.Respond(sip.NewResponseFromRequest(req, sip.StatusNotAcceptable, "Not Acceptable", nil))
}
