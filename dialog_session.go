// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

type DialogSession interface {
	Id() string
	Context() context.Context
	Hangup(ctx context.Context) error
	Media() *DialogMedia
	DialogSIP() *sipgo.Dialog
	Do(ctx context.Context, req *sip.Request) (*sip.Response, error)
	Close() error
}

// referAnswerDeadline bounds how long a waiting dialogRefer blocks for the
// terminal sipfrag NOTIFY after the 202 Accepted. A 202 only acknowledges that
// the recipient accepted the REFER for processing; the transfer outcome arrives
// asynchronously. Package var so tests can shrink it.
var referAnswerDeadline = 30 * time.Second

// ReferFailureError is returned by a waiting Refer when a blind transfer does not
// complete successfully: either a terminal sipfrag NOTIFY carried a non-2xx
// status, or no terminal sipfrag arrived within referAnswerDeadline. It carries
// only the numeric SIP status and a short reason phrase — never Via/Contact/host
// or other routing internals — so a caller can classify the outcome (busy /
// unavailable / rejected) without risk of leaking SIP internals.
type ReferFailureError struct {
	// Status is the SIP status from the terminal sipfrag (e.g. 486). It is 0 when
	// the failure is a timeout with no terminal sipfrag received.
	Status int
	// Reason is a short, non-sensitive phrase from the sipfrag status line
	// (e.g. "Busy Here"), or "timeout"/"cancelled". Classify on Status, not this.
	Reason string
}

// Error renders the failure without any SIP routing internals.
func (e *ReferFailureError) Error() string {
	if e.Status == 0 {
		return fmt.Sprintf("refer transfer failed: %s", e.Reason)
	}
	return fmt.Sprintf("refer transfer failed: %d %s", e.Status, e.Reason)
}

// referTerminal carries a REFER outcome parsed from a sipfrag NOTIFY status
// line, from dialogHandleReferNotify to a dialogRefer waiter.
type referTerminal struct {
	status int
	reason string
}

// parseSipfragStatus parses the status code and reason phrase from a
// message/sipfrag status line of the form "SIP/2.0 <code> <reason>" (the shape
// sendNotify writes). Only the first line is the status line. It returns
// ok=false when the line is malformed or the code is not a valid SIP status, so
// an untrusted NOTIFY body can never be mistaken for a terminal outcome.
func parseSipfragStatus(frag string) (status int, reason string, ok bool) {
	line := frag
	if i := strings.IndexAny(frag, "\r\n"); i >= 0 {
		line = frag[:i]
	}
	const prefix = "SIP/2.0 "
	if !strings.HasPrefix(line, prefix) {
		return 0, "", false
	}
	rest := strings.TrimSpace(line[len(prefix):])
	codeStr := rest
	if sp := strings.IndexByte(rest, ' '); sp >= 0 {
		codeStr = rest[:sp]
		reason = strings.TrimSpace(rest[sp+1:])
	}
	code, err := strconv.Atoi(codeStr)
	if err != nil || code < 100 || code > 699 {
		return 0, "", false
	}
	return code, reason, true
}

// parseReferNotifyEventID extracts the RFC 3515 subscription id from a REFER
// NOTIFY's Event header value of the form "refer;id=<n>". sipgo exposes no typed
// Event header, so the generic value is parsed here. Peers MAY omit ;id, so
// hasID=false is a normal case.
func parseReferNotifyEventID(req *sip.Request) (id uint32, hasID bool) {
	h := req.GetHeader("Event")
	if h == nil {
		return 0, false
	}
	// The value is "<event-type>[;param=value]*"; scan the params for id=.
	for _, part := range strings.Split(h.Value(), ";") {
		part = strings.TrimSpace(part)
		const idPrefix = "id="
		if !strings.HasPrefix(part, idPrefix) {
			continue
		}
		n, err := strconv.ParseUint(strings.TrimSpace(part[len(idPrefix):]), 10, 32)
		if err != nil {
			return 0, false
		}
		return uint32(n), true
	}
	return 0, false
}

//
// Here are many common functions built for dialog
//

// dialogRefer sends a REFER and returns once it is accepted. When wait is true it
// additionally blocks for the terminal sipfrag NOTIFY (bounded by
// referAnswerDeadline) and reports the real transfer outcome as a
// *ReferFailureError, rather than reporting the 202 as success. Callers that
// supply an OnNotify callback observe the outcome asynchronously instead and pass
// wait=false.
func dialogRefer(ctx context.Context, d DialogSession, recipient sip.Uri, referTo sip.Uri, refferedBy sip.Uri, wait bool, headers ...sip.Header) error {
	if d.DialogSIP().LoadState() != sip.DialogStateConfirmed {
		return fmt.Errorf("can only be called on answered dialog")
	}

	// Register the waiter BEFORE sending so a terminal sipfrag NOTIFY that races
	// ahead of the park below is buffered on it, not lost. Unregister on every
	// return path so a late NOTIFY for this finished attempt finds no waiter and
	// cannot be inherited by a later attempt on the same still-live dialog.
	var waiter *referWaiter
	if wait {
		waiter = d.Media().beginReferAttempt()
		defer d.Media().endReferAttempt(waiter)
	}

	req := sip.NewRequest(sip.REFER, recipient)
	// Invite request tags must be preserved but switched
	req.AppendHeader(sip.NewHeader("Refer-To", uri2Header(referTo)))
	req.AppendHeader(sip.NewHeader("Referred-By", uri2Header(refferedBy)))
	for _, h := range headers {
		if h == nil {
			return fmt.Errorf("refer header is nil")
		}

		switch h.Name() {
		// Avoid duplicates
		case "Refer-To", "refer-to", "Referred-By", "reffered-by":
			req.ReplaceHeader(h)
			continue
		default:
			req.AppendHeader(h)
		}
	}

	res, err := d.Do(ctx, req)
	if err != nil {
		return err
	}

	// Record the sent REFER's CSeq (assigned by the dialog during Do) so a NOTIFY
	// carrying an RFC 3515 Event id can be correlated to THIS attempt. Peers that
	// omit the id fall back to single-in-flight correlation.
	if waiter != nil {
		if cseq := req.CSeq(); cseq != nil {
			d.Media().setReferAttemptCSeq(waiter, cseq.SeqNo)
		}
	}

	if res.StatusCode != sip.StatusAccepted {
		return sipgo.ErrDialogResponse{
			Res: res,
		}
	}

	if waiter == nil {
		return nil
	}

	// The 202 only says the REFER was accepted for processing — the real outcome
	// arrives asynchronously as a terminal sipfrag NOTIFY handled by
	// dialogHandleReferNotify. Block for it so the caller learns the truthful
	// result instead of a premature success.
	waitCtx, cancel := context.WithTimeout(ctx, referAnswerDeadline)
	defer cancel()

	select {
	case term := <-waiter.ch:
		if term.status < 300 {
			return nil
		}
		return &ReferFailureError{Status: term.status, Reason: term.reason}
	case <-waitCtx.Done():
		// Distinguish a genuine answer-supervision timeout from a cancelled parent
		// context (the call was torn down under us) for diagnostic accuracy. Both
		// classify as a Status-0 failure.
		reason := "timeout"
		if ctx.Err() != nil {
			reason = "cancelled"
		}
		return &ReferFailureError{Status: 0, Reason: reason}
	}
}

func dialogHandleReferNotify(d DialogSession, req *sip.Request, tx sip.ServerTransaction) {
	// TODO how to know this is refer
	contentType := req.ContentType().Value()
	// For now very basic check
	if !strings.HasPrefix(contentType, "message/sipfrag") {
		tx.Respond(sip.NewResponseFromRequest(req, sip.StatusBadRequest, "Bad Request", nil))
		return
	}

	frag := string(req.Body())
	if len(frag) < len("SIP/2.0 100 xx") {
		tx.Respond(sip.NewResponseFromRequest(req, sip.StatusBadRequest, "Bad Request", nil))
		return
	}

	// A sipfrag whose status line does not parse is not a usable outcome. Rejecting
	// it here extends the short-body guard above rather than letting a malformed
	// body through as status 0.
	code, reason, ok := parseSipfragStatus(frag)
	if !ok {
		tx.Respond(sip.NewResponseFromRequest(req, sip.StatusBadRequest, "Bad Request", nil))
		return
	}

	tx.Respond(sip.NewResponseFromRequest(req, sip.StatusOK, "OK", nil))

	// TODO: We need find better way to store this on refer.
	// best case this would be dialogSIP
	med := d.Media()
	med.mu.Lock()
	onNot := med.onReferNotify
	med.mu.Unlock()

	// Only a final sipfrag is a transfer outcome; 1xx is progress. Delivering just
	// the terminal keeps the waiter's single-slot mailbox from being occupied by a
	// 100 Trying and dropping the real result.
	if code >= 200 {
		id, hasID := parseReferNotifyEventID(req)
		med.deliverReferResult(id, hasID, referTerminal{status: code, reason: reason})
	}

	if onNot != nil {
		onNot(code)
	} else if code >= 200 {
		d.Hangup(context.TODO())
	}
}

func dialogHandleRefer(d DialogSession, dg *Diago, req *sip.Request, tx sip.ServerTransaction, onReferDialog OnReferDialogFunc) error {
	// https://datatracker.ietf.org/doc/html/rfc3515#section-2.4.2
	// 	An agent responding to a REFER method MUST return a 400 (Bad Request)
	//    if the request contained zero or more than one Refer-To header field
	//    values.
	log := dg.log
	referTo := req.GetHeader("Refer-To")
	if referTo == nil {
		log.Info("Received REFER without Refer-To header")
		return tx.Respond(sip.NewResponseFromRequest(req, 400, "Bad Request", nil))
	}

	referToUri := sip.Uri{}
	headerParams := sip.NewParams()

	_, err := sip.ParseAddressValue(referTo.Value(), &referToUri, &headerParams)
	if err != nil {
		log.Info("Received REFER but failed to parse Refer-To uri", "error", err)
		return tx.Respond(sip.NewResponseFromRequest(req, 400, "Bad Request", nil))
	}

	contact := req.Contact()
	if contact == nil {
		log.Info("Received REFER but no Contact Header present")
		return tx.Respond(sip.NewResponseFromRequest(req, 400, "Bad Request", []byte("No Contact Header")))
	}

	// TODO can we locate this more checks
	log.Info("Accepting refer")
	if err := tx.Respond(sip.NewResponseFromRequest(req, 202, "Accepted", nil)); err != nil {
		return fmt.Errorf("failed to send 202 Accepted")
	}

	// This transcation can now terminate?
	return dialogReferInvite(d, dg, referToUri, contact.Address, onReferDialog, req)
}

func dialogReferInvite(d DialogSession, dg *Diago, referToUri sip.Uri, remoteTarget sip.Uri, onReferDialog OnReferDialogFunc, referReq *sip.Request) error {

	// TODO after this we could get BYE immediately, but caller would not be able
	// to take control over refer dialog

	// REFER State reasons summary
	/* | Reason | Meaning | Should Retry? | Automatic Retry? |
	   |--------|---------|---------------|------------------|
	   | **noresource** | Success or final failure | No (completed) | No |
	   | **rejected** | Policy denied | Maybe later | No |
	   | **deactivated** | Feature disabled | Yes, after retry-after | No |
	   | **probation** | Temporary failure | Yes, after retry-after | No |
	   | **timeout** | Request timed out | Maybe | No |
	   | **giveup** | Referee gave up | Maybe | No |	 */

	type subState struct {
		state      string
		reason     string
		expires    int
		retryAfter int
	}

	// TODO: Multiple Refers require having ID
	referID := ""

	sendNotify := func(ctx context.Context, statusCode int, reason string, sub subState) error {
		req := sip.NewRequest(sip.NOTIFY, remoteTarget)

		referParams := sip.NewParams()
		referParams.Add("refer", "")
		if referID != "" {
			referParams.Add("id", referID)
		}

		stateParams := sip.NewParams()
		stateParams.Add(sub.state, "")
		if sub.reason != "" {
			stateParams.Add("reason", sub.reason)
		}
		if sub.retryAfter > 0 {
			stateParams.Add("retry-after", strconv.Itoa(sub.retryAfter))
		}

		if sub.expires > 0 {
			stateParams.Add("expires", strconv.Itoa(sub.expires))
		}

		req.AppendHeader(sip.NewHeader("Event", referParams.ToString(';')))
		req.AppendHeader(sip.NewHeader("Subscription-State", stateParams.ToString(';')))
		req.AppendHeader(sip.NewHeader("Content-Type", "message/sipfrag;version=2.0"))
		frag := fmt.Sprintf("SIP/2.0 %d %s", statusCode, reason)
		req.SetBody([]byte(frag))

		res, err := d.Do(ctx, req)
		if err != nil {
			return err
		}
		if res.StatusCode != 200 {
			return fmt.Errorf("notify received non 200 response. code=%d", res.StatusCode)
		}
		return nil
	}

	ctx := d.Context()

	// TODO  Mutliple Refers require IDs in NOTIFY event header
	// https://datatracker.ietf.org/doc/html/rfc3515#section-2.4.6

	// FROM, TO, CALLID must be same to make SUBSCRIBE working

	// onReferRequest(referToUri, )

	// Check is this REFER RFC 3892 compatible
	// https://datatracker.ietf.org/doc/html/rfc3892#autoid-3
	// if referredBy != nil {
	// 	opts.Headers = append(opts.Headers, sip.HeaderClone(referredBy))
	// }

	referDialog, err := dg.NewDialog(referToUri, NewDialogOptions{})
	if err != nil {
		return err
	}
	defer referDialog.Close()

	if h := referReq.GetHeader("referred-by"); h != nil {
		referDialog.InviteRequest.AppendHeader(sip.HeaderClone(h))
	}
	if h := referReq.GetHeader("replaces"); h != nil {
		referDialog.InviteRequest.AppendHeader(sip.HeaderClone(h))
	}

	// 	The final NOTIFY sent in response to a REFER MUST indicate
	//    the subscription has been "terminated" with a reason of "noresource".
	//    (The resource being subscribed to is the state of the referenced
	//    request).
	referDialog.OnState(func(s sip.DialogState) {
		if s == sip.DialogStateConfirmed || s == sip.DialogStateEnded {
			if err := sendNotify(ctx, 200, "OK", subState{
				state:  "terminated",
				reason: "noresource",
			}); err != nil {
				dg.log.Info("REFER Notify failed for 200", "error", err)
			}
		}
	})

	if err := sendNotify(ctx, 100, "Trying", subState{state: "active", expires: 60}); err != nil {
		// log.Info("REFER NOTIFY 100 failed to sent", "error", err)
		return fmt.Errorf("refer NOTIFY 100 failed to sent : %w", err)
	}

	// We send ref dialog to processing. After sending 200 OK this session will terminate
	if err := onReferDialog(referDialog); err != nil {
		// DO notify?
		dg.log.Info("OnReferDialog handling failed with", "error", err)
		var resErr *sipgo.ErrDialogResponse
		if errors.As(err, &resErr) {
			return sendNotify(ctx, resErr.Res.StatusCode, resErr.Res.Reason, subState{
				state:  "terminated",
				reason: "noresource",
			})
		}

		// If call failed to be established but not yet confirmed
		state := referDialog.LoadState()
		if state == 0 || state == sip.DialogStateEstablished {
			return sendNotify(ctx, 400, "Bad Request", subState{
				state:  "terminated",
				reason: "noresource",
			})
		}
		return err
	}
	// Now this dialog will receive BYE and it will terminate
	// We need to send this referDialog to control of caller
	return nil
}

type OnReferTransactionFunc func(referTransaction ReferTransaction) error

type ReferTransaction struct {
	d            DialogSession
	Refer        *sip.Request
	referTo      sip.Uri
	tx           sip.ServerTransaction
	dg           *Diago
	remoteTarget sip.Uri
}

func (r *ReferTransaction) Accept(ctx context.Context, opts InviteClientOptions) (*DialogClientSession, error) {
	dg := r.dg
	req := r.Refer
	referToUri := r.referTo

	log := dg.log
	log.Info("Accepting refer")
	if err := r.tx.Respond(sip.NewResponseFromRequest(req, 202, "Accepted", nil)); err != nil {
		return nil, fmt.Errorf("failed to send 202 Accepted")
	}

	referDialog, err := dg.NewDialog(referToUri, NewDialogOptions{})
	if err != nil {
		return nil, err
	}
	// defer referDialog.Close()

	// 	The final NOTIFY sent in response to a REFER MUST indicate
	//    the subscription has been "terminated" with a reason of "noresource".
	//    (The resource being subscribed to is the state of the referenced
	//    request).

	err = func() error {
		if h := req.GetHeader("refered-by"); h != nil {
			opts.Headers = append(opts.Headers, sip.HeaderClone(h))
		}
		if h := req.GetHeader("replaces"); h != nil {
			opts.Headers = append(opts.Headers, sip.HeaderClone(h))
		}

		if err := referDialog.Invite(ctx, opts); err != nil {
			return err
		}

		dg.log.Info("OnReferDialog handling failed with", "error", err)
		var resErr *sipgo.ErrDialogResponse
		if errors.As(err, &resErr) {
			return r.sendNotify(ctx, resErr.Res.StatusCode, resErr.Res.Reason, subState{
				state:  "terminated",
				reason: "noresource",
			})
		}

		// If call failed to be established but not yet confirmed
		state := referDialog.LoadState()
		if state == 0 || state == sip.DialogStateEstablished {
			return r.sendNotify(ctx, 400, "Bad Request", subState{
				state:  "terminated",
				reason: "noresource",
			})
		}
		return r.sendNotify(ctx, 200, "OK", subState{
			state:  "terminated",
			reason: "noresource",
		})
	}()
	if err != nil {
		r.sendNotify(ctx, 200, "OK", subState{
			state:  "terminated",
			reason: "noresource",
		})

		referDialog.Close()
		return nil, err
	}
	return referDialog, nil
}

type subState struct {
	state      string
	reason     string
	expires    int
	retryAfter int
}

func (r *ReferTransaction) sendNotify(ctx context.Context, statusCode int, reason string, sub subState) error {
	req := sip.NewRequest(sip.NOTIFY, r.remoteTarget)

	referParams := sip.NewParams()
	referParams.Add("refer", "")

	// TODO: Multiple Refers require having ID
	referID := ""
	if referID != "" {
		referParams.Add("id", referID)
	}

	stateParams := sip.NewParams()
	stateParams.Add(sub.state, "")
	if sub.reason != "" {
		stateParams.Add("reason", sub.reason)
	}
	if sub.retryAfter > 0 {
		stateParams.Add("retry-after", strconv.Itoa(sub.retryAfter))
	}

	if sub.expires > 0 {
		stateParams.Add("expires", strconv.Itoa(sub.expires))
	}

	req.AppendHeader(sip.NewHeader("Event", referParams.ToString(';')))
	req.AppendHeader(sip.NewHeader("Subscription-State", stateParams.ToString(';')))
	req.AppendHeader(sip.NewHeader("Content-Type", "message/sipfrag;version=2.0"))
	frag := fmt.Sprintf("SIP/2.0 %d %s", statusCode, reason)
	req.SetBody([]byte(frag))

	res, err := r.d.Do(ctx, req)
	if err != nil {
		return err
	}
	if res.StatusCode != 200 {
		return fmt.Errorf("notify received non 200 response. code=%d", res.StatusCode)
	}
	return nil
}

func dialogHandleReferTransaction(d DialogSession, dg *Diago, req *sip.Request, tx sip.ServerTransaction, onReferDialog OnReferTransactionFunc) error {
	log := dg.log
	referTo := req.GetHeader("Refer-To")
	if referTo == nil {
		log.Info("Received REFER without Refer-To header")
		return tx.Respond(sip.NewResponseFromRequest(req, 400, "Bad Request", nil))
	}

	referToUri := sip.Uri{}
	headerParams := sip.NewParams()

	_, err := sip.ParseAddressValue(referTo.Value(), &referToUri, &headerParams)
	if err != nil {
		log.Info("Received REFER but failed to parse Refer-To uri", "error", err)
		return tx.Respond(sip.NewResponseFromRequest(req, 400, "Bad Request", nil))
	}

	contact := req.Contact()
	if contact == nil {
		log.Info("Received REFER but no Contact Header present")
		return tx.Respond(sip.NewResponseFromRequest(req, 400, "Bad Request", []byte("No Contact Header")))
	}

	rtx := ReferTransaction{
		dg:           dg,
		tx:           tx,
		referTo:      referToUri,
		remoteTarget: contact.Address,
		Refer:        req,
	}
	return onReferDialog(rtx)
}
