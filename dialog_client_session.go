// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
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
	// TODO check state of call

	req := sip.NewRequest(sip.REFER, d.InviteResponse.Contact().Address)
	// UASRequestBuild(req, d.InviteResponse)

	// Invite request tags must be preserved but switched
	req.AppendHeader(sip.NewHeader("Refer-To", referTo.String()))

	res, err := d.Do(ctx, req)
	if err != nil {
		return err
	}

	if res.StatusCode != sip.StatusAccepted {
		return sipgo.ErrDialogResponse{
			Res: res,
		}
	}

	// d.waitNotify = make(chan error)
	return d.Hangup(ctx)

	// // There is now implicit subscription
	// select {
	// case e := <-d.waitNotify:
	// 	return e
	// case <-d.Context().Done():
	// 	return d.Context().Err()
	// }
}

func (d *DialogClientSession) handleRefer(dg *Diago, req *sip.Request, tx sip.ServerTransaction) {
	d.mu.Lock()
	onRefDialog := d.onReferDialog
	d.mu.Unlock()
	if onRefDialog == nil {
		tx.Respond(sip.NewResponseFromRequest(req, sip.StatusNotAcceptable, "Not Acceptable", nil))
		return
	}

	referTo := req.GetHeader("Refer-To")
	// https://datatracker.ietf.org/doc/html/rfc3515#section-2.4.2
	// 	An agent responding to a REFER method MUST return a 400 (Bad Request)
	//    if the request contained zero or more than one Refer-To header field
	//    values.
	if referTo == nil {
		dg.log.Info().Msg("Received REFER without Refer-To header")
		tx.Respond(sip.NewResponseFromRequest(req, 400, "Bad Request", nil))
		return
	}

	referToUri := sip.Uri{}
	if err := sip.ParseUri(referTo.Value(), &referToUri); err != nil {
		dg.log.Info().Err(err).Msg("Received REFER bud failed to parse Refer-To uri")
		tx.Respond(sip.NewResponseFromRequest(req, 400, "Bad Request", nil))
		return
	}

	// TODO can we locate this more checks
	dg.log.Info().Msg("Accepting refer")
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

	addSipFrag := func(req *sip.Request, statusCode sip.StatusCode, reason string) {
		req.AppendHeader(sip.NewHeader("Event", "refer"))
		req.AppendHeader(sip.NewHeader("Content-Type", "message/sipfrag;version=2.0"))
		frag := fmt.Sprintf("SIP/2.0 %d %s", statusCode, reason)
		req.SetBody([]byte(frag))
	}

	ctx := d.Context()
	contact := req.Contact()
	notify := sip.NewRequest(sip.NOTIFY, contact.Address)

	notify100 := notify.Clone()
	addSipFrag(notify100, 100, "Trying")

	// FROM, TO, CALLID must be same to make SUBSCRIBE working
	_, err := d.Do(ctx, notify100)
	if err != nil {
		dg.log.Info().Err(err).Msg("REFER NOTIFY 100 failed to sent")
		return
	}
	// if res.StatusCode != 200 {
	// 	dg.log.Info().Int("res", int(res.StatusCode)).Msg("REFER NOTIFY 100 non 200 resposne")
	// 	return
	// }

	referDialog, err := dg.Invite(ctx, referToUri, InviteOptions{})
	if err != nil {
		// DO notify?
		dg.log.Error().Err(err).Msg("REFER dialog failed to dial")
		return
	}
	// We send ref dialog to processing. After sending 200 OK this session will terminate
	// TODO this should be called before Invite started as caller needs to be notified before
	onRefDialog(referDialog)

	notify200 := notify.Clone()
	addSipFrag(notify200, 200, "OK")
	_, err = d.Do(ctx, notify200)
	if err != nil {
		dg.log.Info().Err(err).Msg("REFER NOTIFY 100 failed to sent")
		return
	}

	// Now this dialog will receive BYE and it will terminate
	// We need to send this referDialog to control of caller

	// if res.StatusCode != 200 {
	// 	dg.log.Info().Int("res", int(res.StatusCode)).Msg("REFER NOTIFY 100 non 200 resposne")
	// 	return
	// }

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
