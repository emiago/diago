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
	"sync"
	"sync/atomic"
	"time"

	"github.com/emiago/diago/media"
	"github.com/emiago/diago/media/sdp"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

type OnReferDialogFunc func(referDialog *DialogClientSession) error

// DialogServerSession represents inbound channel
type DialogServerSession struct {
	*sipgo.DialogServerSession

	// MediaSession *media.MediaSession
	DialogMedia

	onReferDialog OnReferDialogFunc

	mediaConf MediaConfig

	// sessionTimers is the RFC 4028 policy stamped from dg.sessionTimers at
	// construction. It is consumed by the answer path.
	sessionTimers SessionTimerPolicy
	// timerAnswer holds the negotiated timer outcome for this answer, nil until a
	// timer-supporting peer is negotiated. Guarded by d.mu.
	timerAnswer *timerAnswer
	// timerOnce guards the refresh loop and watchdog launch so a re-answer or an
	// inbound refresh can never spawn a second goroutine.
	timerOnce sync.Once
	// watchdog is the armed peer-refresh watchdog, set when the peer is the
	// refresher and reset on an inbound refresh re-INVITE. Guarded by d.mu.
	watchdog *peerRefreshWatchdog

	closed atomic.Uint32
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

// statusSessionIntervalTooSmall is the RFC 4028 §6 rejection for an offered
// Session-Expires below the Min-SE floor. sipgo carries no constant for it.
const statusSessionIntervalTooSmall = 422

// errSessionIntervalTooSmall aborts the answer when the offered Session-Expires
// is below the Min-SE floor. A 422 has already been sent to the peer.
var errSessionIntervalTooSmall = errors.New("session interval too small")

// timerAnswer is the negotiated RFC 4028 outcome for a single answer: the
// headers to append to the 200 OK and the parameters for the refresh loop or the
// peer-refresh watchdog. It is computed before RespondSDP and consumed once the
// 200 OK is sent.
type timerAnswer struct {
	headers     []sip.Header
	interval    time.Duration
	weRefresh   bool
	armWatchdog bool
	expiry      time.Duration
}

// buildTimerAnswer is the pure UAS answer-path decision for RFC 4028 session
// timers. Given the offered INVITE and our policy it returns the headers to add
// to the 200 OK (or the Min-SE header for a 422) and the response status. It
// keeps every SIP-transaction dependency out so the decision is unit testable
// without a PBX.
//
//   - timers disabled, or the peer offered no Session-Expires (timer-unaware) →
//     no headers, status 200, nil answer: a graceful no-op, never a rejection.
//   - offered Session-Expires below the floor → status 422 plus Min-SE, nil
//     answer, so no media, no loop and no watchdog.
//   - otherwise → Session-Expires with the honored refresher plus Supported:
//     timer (never Require: timer), and an answer that either refreshes or arms
//     the watchdog.
func buildTimerAnswer(invite *sip.Request, policy SessionTimerPolicy) (ta *timerAnswer, hdrs []sip.Header, status int) {
	if !policy.Enabled {
		return nil, nil, sip.StatusOK
	}

	offer, ok := parseSessionTimerOffer(invite)
	if !ok {
		// Timer-unaware peer: answer normally with no timer headers, start nothing.
		return nil, nil, sip.StatusOK
	}

	dec := negotiate(offer.SE, offer.MinSE, offer.Refresher, policy, refresherUAS)

	if dec.BelowFloor {
		// RFC 4028 §6: reject below-floor and echo our Min-SE, establishing nothing.
		return nil, []sip.Header{sip.NewHeader("Min-SE", secondsOf(dec.Negotiated))}, statusSessionIntervalTooSmall
	}

	seValue := secondsOf(dec.Negotiated)
	if dec.Refresher != "" {
		seValue += ";refresher=" + dec.Refresher
	}
	hdrs = []sip.Header{
		sip.NewHeader("Session-Expires", seValue),
		sip.NewHeader("Supported", "timer"),
	}
	// We refresh when we are the elected refresher, otherwise the peer refreshes
	// and we arm a watchdog to bound a peer that stops refreshing.
	return &timerAnswer{
		headers:     hdrs,
		interval:    dec.Interval,
		weRefresh:   dec.WeRefresh,
		armWatchdog: !dec.WeRefresh,
		expiry:      dec.Negotiated,
	}, hdrs, sip.StatusOK
}

// prepareSessionTimers negotiates RFC 4028 session timers for the answer. On a
// below-floor offer it sends 422 with Min-SE and returns
// errSessionIntervalTooSmall so the caller aborts before any media, loop or
// watchdog. Otherwise it stashes the timer headers, which RespondSDP appends to
// the 200 OK, along with the launch parameters. A timer-unaware peer or a
// disabled policy leaves d.timerAnswer nil, a graceful no-op.
func (d *DialogServerSession) prepareSessionTimers() error {
	ta, hdrs, status := buildTimerAnswer(d.InviteRequest, d.sessionTimers)
	if status == statusSessionIntervalTooSmall {
		if err := d.Respond(statusSessionIntervalTooSmall, "Session Interval Too Small", nil, hdrs...); err != nil {
			return err
		}
		return errSessionIntervalTooSmall
	}
	if ta == nil {
		return nil
	}
	d.mu.Lock()
	d.timerAnswer = ta
	d.mu.Unlock()
	return nil
}

// launchSessionTimers starts the RFC 4028 timer for this dialog exactly once.
// When we are the elected refresher it runs the refresh loop, re-INVITEing at
// half the negotiated interval. Otherwise the peer refreshes and it arms a
// watchdog that hangs up on a missed refresh. Both run on d.Context() so dialog
// teardown cancels them, and the grace comes from the policy via watchdogGrace.
func (d *DialogServerSession) launchSessionTimers(ta *timerAnswer) {
	d.timerOnce.Do(func() {
		switch {
		case ta.weRefresh:
			// Fire and forget: the loop's lifecycle is the dialog context, so it exits
			// on Close or Bye. Its escalation error is not propagated here because
			// there is no per-dialog error channel on the answer path, and a stalled
			// refresh is independently bounded by the peer's own session timer.
			go sessionRefreshLoop(d.Context(), ta.interval, d.ReInvite) //nolint:errcheck // see comment above
		case ta.armWatchdog:
			w := armPeerRefreshWatchdog(d.Context(), ta.expiry, watchdogGrace(d.sessionTimers), d.Hangup)
			d.mu.Lock()
			d.watchdog = w
			d.mu.Unlock()
		}
	})
}

func (d *DialogServerSession) RespondSDP(body []byte) error {
	headers := []sip.Header{sip.NewHeader("Content-Type", "application/sdp")}

	d.mu.Lock()
	ta := d.timerAnswer
	d.mu.Unlock()
	if ta != nil {
		headers = append(headers, ta.headers...)
	}

	if err := d.DialogServerSession.Respond(200, "OK", body, headers...); err != nil {
		return err
	}

	// The 200 OK established the session, so start the refresh loop or arm the
	// peer-refresh watchdog now, exactly once and on the dialog context so
	// teardown cancels it.
	if ta != nil {
		d.launchSessionTimers(ta)
	}
	return nil
}

// Answer creates media session and answers
// After this new AudioReader and AudioWriter are created for audio manipulation
// NOTE: Not final API
func (d *DialogServerSession) Answer() error {
	// RFC 4028: negotiate session timers before the 200 OK. A below-floor offer is
	// rejected here with 422 and aborts the answer. The timer headers are stashed
	// for RespondSDP, which launches the loop or watchdog once the 200 OK is sent.
	if err := d.prepareSessionTimers(); err != nil {
		return err
	}

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

	// OnRefer is called on successfull REFER handling
	//
	// It creates new dialog (NewDialog) on which you need to call Invite() and Ack()
	// Any error from invite, ack or other processing should be returned for correct Notify handling
	//
	// NOTE: IT is SCOPED to handler and exiting handler will Close/Terminate this dialog!
	OnRefer func(referDialog *DialogClientSession) error
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

	// RFC 4028: mirror Answer and negotiate session timers before the 200 OK.
	if err := d.prepareSessionTimers(); err != nil {
		return err
	}

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
		// The 200 OK answers the offer we sent on the re-INVITE.
		ms.RemoteSDPIsAnswer = true
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
// It blocks until the transfer outcome is known: the terminal sipfrag NOTIFY, or
// a 30s deadline. A transfer that did not succeed returns *ReferFailureError.
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
	Headers []sip.Header
	// OnNotify sends notify status code.
	// Setting this returns from refer as soon as it is accepted, leaving the
	// transfer outcome to the callback.
	OnNotify func(statusCode int)
}

// ReferOptions sends a REFER. With OnNotify set it returns once the REFER is
// accepted and reports outcomes to the callback. Without one it blocks for the
// transfer outcome and returns *ReferFailureError if it did not succeed.
func (d *DialogServerSession) ReferOptions(ctx context.Context, referTo sip.Uri, opts ReferServerOptions) error {
	d.mu.Lock()
	cont := d.remoteContactUnsafe()
	if opts.OnNotify != nil {
		d.onReferNotify = opts.OnNotify
	}
	d.mu.Unlock()
	return dialogRefer(ctx, d, cont.Address, referTo, d.InviteResponse.Contact().Address, opts.OnNotify == nil, opts.Headers...)
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

	// RFC 4028: an inbound refresh re-INVITE resets the active timer. When the
	// peer is the refresher its watchdog is rearmed. When we refresh, the loop's
	// ticker self-rearms each tick, so no reset is needed. A plain media-update
	// re-INVITE carries no Session-Expires and is a no-op here.
	d.resetSessionTimerOnRefresh(req)

	return d.handleMediaUpdate(req, tx, d.InviteResponse.Contact())
}

// resetSessionTimerOnRefresh rearms the peer-refresh watchdog when req is an RFC
// 4028 refresh re-INVITE. It is a no-op when timers are disabled, when the
// request carries no Session-Expires, or when we are the refresher and therefore
// have no watchdog armed.
func (d *DialogServerSession) resetSessionTimerOnRefresh(req *sip.Request) {
	if !d.sessionTimers.Enabled {
		return
	}
	if _, ok := parseSessionTimerOffer(req); !ok {
		return
	}
	d.mu.Lock()
	w := d.watchdog
	d.mu.Unlock()
	if w != nil {
		w.Reset()
	}
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

func (d *DialogServerSession) Hold(ctx context.Context) error {
	m := d.MediaSession().Fork()
	m.Mode = sdp.ModeSendonly
	if err := d.reInviteMediaSession(ctx, m); err != nil {
		return err
	}
	return nil
}

func (d *DialogServerSession) Unhold(ctx context.Context) error {
	m := d.MediaSession().Fork()
	m.Mode = sdp.ModeSendrecv
	if err := d.reInviteMediaSession(ctx, m); err != nil {
		return err
	}
	return nil
}
