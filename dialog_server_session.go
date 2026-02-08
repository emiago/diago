// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	mrand "math/rand/v2"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/emiago/diago/media"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// DialogServerSession represents inbound channel
type DialogServerSession struct {
	*sipgo.DialogServerSession

	// MediaSession *media.MediaSession
	DialogMedia

	onReferDialog func(referDialog *DialogClientSession)

	mediaConf MediaConfig
	closed    atomic.Uint32
}

func (d *DialogServerSession) Id() string {
	return d.ID
}

func (d *DialogServerSession) Close() error {
	if !d.closed.CompareAndSwap(0, 1) {
		return nil
	}
	e1 := d.DialogMedia.Close()
	e2 := d.DialogServerSession.Close()
	return errors.Join(e1, e2)
}

func (d *DialogServerSession) FromUser() string {
	return d.InviteRequest.From().Address.User
}

// User that was dialed
func (d *DialogServerSession) ToUser() string {
	return d.InviteRequest.To().Address.User
}

func (d *DialogServerSession) Transport() string {
	return d.InviteRequest.Transport()
}

func (d *DialogServerSession) Trying() error {
	return d.Respond(sip.StatusTrying, "Trying", nil)
}

// Progress sends 100 trying.
//
// Deprecated: Use Trying. It will change behavior to 183 Sesion Progress in future releases
func (d *DialogServerSession) Progress() error {
	return d.Respond(sip.StatusTrying, "Trying", nil)
}

// ProgressMedia sends 183 Session Progress and creates early media
//
// Experimental: Naming of API might change
func (d *DialogServerSession) ProgressMedia() error {
	return d.ProgressMediaOptions(ProgressMediaOptions{})
}

type ProgressMediaOptions struct {
	// Codecs that will be used
	Codecs []media.Codec

	// RTPNAT exposes MediaSession property
	RTPNAT int
}

func (d *DialogServerSession) ProgressMediaOptions(opt ProgressMediaOptions) error {
	d.updateMediaConf(opt.Codecs, opt.RTPNAT)
	if err := d.initMediaSessionFromConf(d.mediaConf); err != nil {
		return err
	}
	rtpSess := media.NewRTPSession(d.mediaSession)
	if err := d.setupRTPSession(rtpSess); err != nil {
		return err
	}

	headers := []sip.Header{sip.NewHeader("Content-Type", "application/sdp")}
	body := rtpSess.Sess.LocalSDP()
	if err := d.DialogServerSession.Respond(183, "Session Progress", body, headers...); err != nil {
		return err
	}
	return rtpSess.MonitorBackground()
}

func (d *DialogServerSession) Ringing() error {
	return d.Respond(sip.StatusRinging, "Ringing", nil)
}

func (d *DialogServerSession) DialogSIP() *sipgo.Dialog {
	return &d.Dialog
}

func (d *DialogServerSession) RemoteContact() *sip.ContactHeader {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.remoteContactTarget != nil {
		return d.remoteContactTarget
	}
	return d.InviteRequest.Contact()
}

func (d *DialogServerSession) RespondSDP(body []byte) error {
	headers := []sip.Header{sip.NewHeader("Content-Type", "application/sdp")}
	return d.DialogServerSession.Respond(200, "OK", body, headers...)
}

// Answer creates media session and answers
// After this new AudioReader and AudioWriter are created for audio manipulation
// NOTE: Not final API
func (d *DialogServerSession) Answer() error {
	// Media Exists as early
	if d.mediaSession != nil {
		// This will now block until ACK received with 64*T1 as max.
		if err := d.RespondSDP(d.mediaSession.LocalSDP()); err != nil {
			return err
		}
		return nil
	}

	if err := d.initMediaSessionFromConf(d.mediaConf); err != nil {
		return err
	}

	rtpSess := media.NewRTPSession(d.mediaSession)
	return d.answerSession(rtpSess)
}

type AnswerOptions struct {
	// OnMediaUpdate triggers when media update happens. It is blocking func, so make sure you exit
	OnMediaUpdate func(d *DialogMedia)
	OnRefer       func(referDialog *DialogClientSession)
	// Codecs that will be used
	Codecs []media.Codec

	// RTPNAT is media.MediaSession.RTPNAT
	// Check media.RTPNAT... options
	RTPNAT int
}

// AnswerOptions allows to answer dialog with options
// Experimental
//
// NOTE: API may change
func (d *DialogServerSession) AnswerOptions(opt AnswerOptions) error {
	d.mu.Lock()
	d.onReferDialog = opt.OnRefer
	d.onMediaUpdate = opt.OnMediaUpdate
	d.mu.Unlock()

	// If media exists as early, only respond 200
	if d.mediaSession != nil {
		// Check do codecs match
		if err := d.RespondSDP(d.mediaSession.LocalSDP()); err != nil {
			return err
		}
		return nil
	}

	d.updateMediaConf(opt.Codecs, opt.RTPNAT)
	if err := d.initMediaSessionFromConf(d.mediaConf); err != nil {
		return err
	}
	rtpSess := media.NewRTPSession(d.mediaSession)
	return d.answerSession(rtpSess)
}

func (d *DialogServerSession) updateMediaConf(codecs []media.Codec, rtpNAT int) {
	// Let override of formats
	conf := &d.mediaConf
	if codecs != nil {
		conf.Codecs = codecs
	}
	conf.rtpNAT = rtpNAT
}

// answerSession. It allows answering with custom RTP Session.
// NOTE: Not final API
func (d *DialogServerSession) answerSession(rtpSess *media.RTPSession) error {
	// TODO: Use setupRTPSession
	sess := rtpSess.Sess
	sdp := d.InviteRequest.Body()
	if sdp == nil {
		return fmt.Errorf("no sdp present in INVITE")
	}

	if err := sess.RemoteSDP(sdp); err != nil {
		return err
	}

	d.mu.Lock()
	d.initRTPSessionUnsafe(sess, rtpSess)
	// Close RTP session
	// d.onCloseUnsafe(func() error {
	// 	return rtpSess.Close()
	// })
	d.mu.Unlock()

	// This will now block until ACK received with 64*T1 as max.
	// How to let caller to cancel this?
	if err := d.RespondSDP(sess.LocalSDP()); err != nil {
		return err
	}

	if err := sess.Finalize(); err != nil {
		return err
	}
	// fmt.Println("--------SErver finalized")

	// Must be called after media and reader writer is setup
	return rtpSess.MonitorBackground()
}

func (d *DialogServerSession) setupRTPSession(rtpSess *media.RTPSession) error {
	sess := rtpSess.Sess
	sdp := d.InviteRequest.Body()
	if sdp == nil {
		return fmt.Errorf("no sdp present in INVITE")
	}

	if err := sess.RemoteSDP(sdp); err != nil {
		return err
	}

	d.mu.Lock()
	d.initRTPSessionUnsafe(sess, rtpSess)
	// Close RTP session
	// d.onCloseUnsafe(func() error {
	// 	return rtpSess.Close()
	// })
	d.mu.Unlock()
	return nil
}

// AnswerLate does answer with Late offer.
func (d *DialogServerSession) AnswerLate() error {
	if err := d.initMediaSessionFromConf(d.mediaConf); err != nil {
		return err
	}
	sess := d.mediaSession
	rtpSess := media.NewRTPSession(sess)
	localSDP := sess.LocalSDP()

	d.mu.Lock()
	d.initRTPSessionUnsafe(sess, rtpSess)
	// Close RTP session
	// d.onCloseUnsafe(func() error {
	// 	return rtpSess.Close()
	// })
	d.mu.Unlock()

	// This will now block until ACK received with 64*T1 as max.
	// How to let caller to cancel this?
	if err := d.RespondSDP(localSDP); err != nil {
		return err
	}
	// Must be called after media and reader writer is setup
	return rtpSess.MonitorBackground()
}

func (d *DialogServerSession) ReadAck(req *sip.Request, tx sip.ServerTransaction) error {
	// Check do we have some session
	err := func() error {
		d.mu.Lock()
		defer d.mu.Unlock()
		sess := d.mediaSession
		if sess == nil {
			return nil
		}
		contentType := req.ContentType()
		if contentType == nil {
			return nil
		}
		body := req.Body()
		if body != nil && contentType.Value() == "application/sdp" {
			// This is Late offer response
			if err := sess.RemoteSDP(body); err != nil {
				return err
			}

			// Finalize session
			if err := sess.Finalize(); err != nil {
				return nil
			}
		}
		return nil
	}()
	if err != nil {
		e := d.Hangup(d.Context())
		return errors.Join(err, e)
	}

	return d.DialogServerSession.ReadAck(req, tx)
}

func (d *DialogServerSession) Hangup(ctx context.Context) error {
	state := d.LoadState()
	fmt.Println("Calling hangup", d.ID)
	if state >= sip.DialogStateConfirmed {
		return d.Bye(ctx)
	}
	return d.Respond(sip.StatusTemporarilyUnavailable, "Temporarly unavailable", nil)
}

func (d *DialogServerSession) ReInvite(ctx context.Context) error {
	sdp := d.mediaSession.LocalSDP()
	contact := d.RemoteContact()
	req := sip.NewRequest(sip.INVITE, contact.Address)
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
		return fmt.Errorf("reinvite: no contact header present")
	}

	ack := sip.NewRequest(sip.ACK, cont.Address)
	return d.WriteRequest(ack)
}

// reInviteMediaSession updates with full new media session
// media MUST BE Forked
func (d *DialogServerSession) reInviteMediaSession(ctx context.Context, ms *media.MediaSession) error {
	sdp := ms.LocalSDP()

	// NOTE: we do not change original invite request
	d.mu.Lock()
	contact := d.remoteContactUnsafe()
	d.mu.Unlock()

	req := sip.NewRequest(sip.INVITE, contact.Address)
	req.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	req.SetBody(sdp)

	res, err := d.reInviteDo(ctx, req)
	if err != nil {
		return err
	}

	// Save new remote target contact and update media
	return func() error {
		d.mu.Lock()
		defer d.mu.Unlock()
		d.remoteContactTarget = res.Contact()

		remoteSDP := res.Body()
		if err := ms.RemoteSDP(remoteSDP); err != nil {
			return fmt.Errorf("sdp update media remote SDP applying failed: %w", err)
		}
		return d.mediaUpdateUnsafe(ms)
	}()
}

func (d *DialogServerSession) reInviteDo(ctx context.Context, req *sip.Request) (*sip.Response, error) {

	for {
		res, err := d.Do(ctx, req.Clone())
		if err != nil {
			return nil, err
		}

		if !res.IsSuccess() {
			// https://datatracker.ietf.org/doc/html/rfc3261#section-14.1
			// If a UAC receives a 491 response to a re-INVITE, it SHOULD start a
			//    timer with a value T chosen as follows:
			//       1. If the UAC is the owner of the Call-ID of the dialog ID
			//          (meaning it generated the value), T has a randomly chosen value
			//          between 2.1 and 4 seconds in units of 10 ms.

			//       2. If the UAC is not the owner of the Call-ID of the dialog ID, T
			//          has a randomly chosen value of between 0 and 2 seconds in units
			//          of 10 ms.

			if res.StatusCode == sip.StatusRequestPending {
				select {
				case <-time.After(time.Duration(2000+mrand.IntN(200)*10) * time.Millisecond):
					continue
				case <-ctx.Done():
					return nil, ctx.Err()
				}
			}

			return nil, sipgo.ErrDialogResponse{
				Res: res,
			}
		}

		// Now do ACK on new Contact
		if err := d.ack(ctx, res.Contact().Address, nil); err != nil {
			return res, err
		}

		return res, nil
	}
}

func (d *DialogServerSession) ack(ctx context.Context, remoteTarget sip.Uri, body []byte) error {
	// inviteRequest := d.InviteRequest
	// recipient := &inviteRequest.Recipient
	// if contact := d.InviteResponse.Contact(); contact != nil {
	// 	recipient = &contact.Address
	// }
	ackRequest := sip.NewRequest(
		sip.ACK,
		remoteTarget,
	)

	if body != nil {
		// This is delayed offer
		ackRequest.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
		ackRequest.SetBody(body)
	}

	if err := d.DialogServerSession.WriteRequest(ackRequest); err != nil {
		return err
	}

	// if err := d.DialogServerSession.WriteAck(ctx, ackRequest); err != nil {
	// 	return err
	// }

	// Now dialog is established and can be add into store
	// if err := DialogsClientCache.DialogStore(ctx, d.ID, d); err != nil {
	// 	return err
	// }
	// d.OnClose(func() error {
	// 	return DialogsClientCache.DialogDelete(context.Background(), d.ID)
	// })
	return nil
}

func (d *DialogServerSession) remoteContactUnsafe() *sip.ContactHeader {
	if d.remoteContactTarget != nil {
		// Invite update can change contact
		return d.remoteContactTarget
	}
	return d.InviteRequest.Contact()
}

// Refer tries todo refer (blind transfer) on call. For more control use ReferOptions
//
// NOTE: It is expected that after calling this you are hanguping call to send BYE
func (d *DialogServerSession) Refer(ctx context.Context, referTo sip.Uri, headers ...sip.Header) error {
	// cont := d.InviteRequest.Contact()
	// return dialogRefer(ctx, d, cont.Address, referTo, headers...)
	return d.ReferOptions(ctx, referTo, ReferServerOptions{
		Headers: headers,
	})
}

type ReferServerOptions struct {
	Headers  []sip.Header
	OnNotify func(statusCode int)
}

func (d *DialogServerSession) ReferOptions(ctx context.Context, referTo sip.Uri, opts ReferServerOptions) error {
	d.mu.Lock()
	cont := d.remoteContactUnsafe()
	if opts.OnNotify != nil {
		d.onReferNotify = opts.OnNotify
	}
	d.mu.Unlock()
	return dialogRefer(ctx, d, cont.Address, referTo, d.InviteResponse.Contact().Address, opts.Headers...)
}

func (d *DialogServerSession) handleReferNotify(req *sip.Request, tx sip.ServerTransaction) {
	dialogHandleReferNotify(d, req, tx)
}

func (d *DialogServerSession) handleRefer(dg *Diago, req *sip.Request, tx sip.ServerTransaction) {
	d.mu.Lock()
	onRefDialog := d.onReferDialog
	d.mu.Unlock()
	if onRefDialog == nil {
		tx.Respond(sip.NewResponseFromRequest(req, sip.StatusNotAcceptable, "Not Acceptable", nil))
		return
	}

	dialogHandleRefer(d, dg, req, tx, onRefDialog)
}

func (d *DialogServerSession) handleReInvite(req *sip.Request, tx sip.ServerTransaction) error {
	// Check is current pending dialog
	if state := d.LoadState(); state == sip.DialogStateEstablished {
		// RFC 3261 §14.2 — UAS Behavior
		// If a UAS receives an INVITE request for an existing dialog while another INVITE transaction is in progress, it MUST return a 491 (Request Pending) response to the new INVITE.”
		return tx.Respond(sip.NewResponseFromRequest(req, sip.StatusRequestPending, "Request Pending", nil))
	}

	// NOTE: Calling ReadRequest increases remote CSEQ.
	// We should not call this until dialog is confirmed, otherwise any intermidiate response
	// will have wrong CSEQ
	if err := d.ReadRequest(req, tx); err != nil {
		if errors.Is(err, sipgo.ErrDialogInvalidCseq) {
			// https://datatracker.ietf.org/doc/html/rfc3261#section-14.2
			// 			A UAS that receives a second INVITE before it sends the final
			//    response to a first INVITE with a lower CSeq sequence number on the
			//    same dialog MUST return a 500 (Server Internal Error)  response to the
			//    second INVITE and MUST include a Retry-After header field with a
			//    randomly chosen value of between 0 and 10 seconds.
			res := sip.NewResponseFromRequest(req, sip.StatusInternalServerError, "Internal Server Error", nil)
			res.AppendHeader(sip.NewHeader("Retry-After", strconv.Itoa(rand.IntN(10))))
			return tx.Respond(res)
		}

		return tx.Respond(sip.NewResponseFromRequest(req, sip.StatusBadRequest, err.Error(), nil))
	}

	return d.handleMediaUpdate(req, tx, d.InviteResponse.Contact())
}

func (d *DialogServerSession) readSIPInfoDTMF(req *sip.Request, tx sip.ServerTransaction) error {
	return tx.Respond(sip.NewResponseFromRequest(req, sip.StatusNotAcceptable, "Not Acceptable", nil))
	// if err := d.ReadRequest(req, tx); err != nil {
	// 	tx.Respond(sip.NewResponseFromRequest(req, sip.StatusBadRequest, "Bad Request", nil))
	// 	return
	// }

	// Parse this
	//Signal=1
	// Duration=160
	// reader := bytes.NewReader(req.Body())

	// for {

	// }
}
