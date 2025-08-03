// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"context"
	"fmt"
	"strings"

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

//
// Here are many common functions built for dialog
//

func dialogRefer(ctx context.Context, d DialogSession, recipient sip.Uri, referTo sip.Uri, headers ...sip.Header) error {
	if d.DialogSIP().LoadState() != sip.DialogStateConfirmed {
		return fmt.Errorf("Can only be called on answered dialog")
	}

	req := sip.NewRequest(sip.REFER, recipient)
	// Invite request tags must be preserved but switched
	req.AppendHeader(sip.NewHeader("Refer-To", referTo.String()))

	for _, h := range headers {
		if h != nil {
			req.AppendHeader(h)
		}
	}

	res, err := d.Do(ctx, req)
	if err != nil {
		return err
	}

	if res.StatusCode != sip.StatusAccepted {
		return sipgo.ErrDialogResponse{
			Res: res,
		}
	}
	return nil
}

func dialogHandleReferNotify(d DialogSession, req *sip.Request, tx sip.ServerTransaction) {
	// TODO how to know this is refer
	contentType := req.ContentType().Value()
	// For now very basic check
	if !strings.HasPrefix(contentType, "message/sipfrag;version=2.0") {
		tx.Respond(sip.NewResponseFromRequest(req, sip.StatusBadRequest, "Bad Request", nil))
		return
	}

	frag := string(req.Body())
	if len(frag) < len("SIP/2.0 100 xx") {
		tx.Respond(sip.NewResponseFromRequest(req, sip.StatusBadRequest, "Bad Request", nil))
		return
	}

	tx.Respond(sip.NewResponseFromRequest(req, sip.StatusOK, "OK", nil))

	switch frag[:11] {
	case "SIP/2.0 100":
	case "SIP/2.0 200":
		d.Hangup(d.Context())
	}
}

func dialogHandleRefer(d DialogSession, dg *Diago, req *sip.Request, tx sip.ServerTransaction, onReferDialog func(referDialog *DialogClientSession)) {
	referTo := req.GetHeader("Refer-To")
	// https://datatracker.ietf.org/doc/html/rfc3515#section-2.4.2
	// 	An agent responding to a REFER method MUST return a 400 (Bad Request)
	//    if the request contained zero or more than one Refer-To header field
	//    values.
	log := dg.log
	if referTo == nil {
		log.Info("Received REFER without Refer-To header")
		tx.Respond(sip.NewResponseFromRequest(req, 400, "Bad Request", nil))
		return
	}

	referToUri := sip.Uri{}
	if err := sip.ParseUri(referTo.Value(), &referToUri); err != nil {
		log.Info("Received REFER bud failed to parse Refer-To uri", "error", err)
		tx.Respond(sip.NewResponseFromRequest(req, 400, "Bad Request", nil))
		return
	}

	contact := req.Contact()
	if contact == nil {
		tx.Respond(sip.NewResponseFromRequest(req, 400, "Bad Request", []byte("No Contact Header")))
		return
	}

	// TODO can we locate this more checks
	log.Info("Accepting refer")
	// emptySess := &DialogClientSession{
	// 	DialogClientSession: &sipgo.DialogClientSession{
	// 		Dialog: sipgo.Dialog{
	// 			InviteRequest: sip.NewRequest(),
	// 		},
	// 	},
	// }
	// emptySess.Init()
	// onRefDialog(&DialogClientSession{})

	tx.Respond(sip.NewResponseFromRequest(req, 202, "Accepted", nil))
	// TODO after this we could get BYE immediately, but caller would not be able
	// to take control over refer dialog

	addSipFrag := func(req *sip.Request, statusCode int, reason string) {
		req.AppendHeader(sip.NewHeader("Event", "refer"))
		req.AppendHeader(sip.NewHeader("Content-Type", "message/sipfrag;version=2.0"))
		frag := fmt.Sprintf("SIP/2.0 %d %s", statusCode, reason)
		req.SetBody([]byte(frag))
	}

	ctx := d.Context()

	notify := sip.NewRequest(sip.NOTIFY, contact.Address)

	notify100 := notify.Clone()
	addSipFrag(notify100, 100, "Trying")

	// FROM, TO, CALLID must be same to make SUBSCRIBE working
	_, err := d.Do(ctx, notify100)
	if err != nil {
		log.Info("REFER NOTIFY 100 failed to sent", "error", err)
		return
	}

	referDialog, err := dg.Invite(ctx, referToUri, InviteOptions{})
	if err != nil {
		// DO notify?
		log.Error("REFER dialog failed to dial", "error", err)
		return
	}
	// We send ref dialog to processing. After sending 200 OK this session will terminate
	// TODO this should be called before Invite started as caller needs to be notified before
	onReferDialog(referDialog)

	notify200 := notify.Clone()
	addSipFrag(notify200, 200, "OK")
	_, err = d.Do(ctx, notify200)
	if err != nil {
		log.Info("REFER NOTIFY 100 failed to sent", "error", err)
		return
	}

	// Now this dialog will receive BYE and it will terminate
	// We need to send this referDialog to control of caller
}
