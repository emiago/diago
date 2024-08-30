// SPDX-License-Identifier: MPL-2.0
// Copyright (C) 2024 Emir Aganovic

package diago

import (
	"context"
	"sync"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// DialogClientSession represents outbound channel
type DialogClientSession struct {
	*sipgo.DialogClientSession

	// MediaSession *media.MediaSession // For normal media
	mu sync.Mutex

	// lastInvite is actual last invite sent by remote REINVITE
	// We do not use sipgo as this needs mutex but also keeping original invite
	lastInvite *sip.Request

	DialogMedia
}

func (d *DialogClientSession) Close() {
	if d.mediaSession != nil {
		d.mediaSession.Close()
	}

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

	if d.lastInvite != nil {
		// Invite update can change contact
		return d.lastInvite.Contact()
	}
	return d.InviteResponse.Contact()
}

// ReInvite sends new invite based on current media session
func (d *DialogClientSession) ReInvite(ctx context.Context) error {
	sdp := d.mediaSession.LocalSDP()
	contact := d.RemoteContact()
	req := sip.NewRequest(sip.INVITE, contact.Address)
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

func (d *DialogClientSession) handleReInvite(req *sip.Request, tx sip.ServerTransaction) {
	if err := d.ReadRequest(req, tx); err != nil {
		tx.Respond(sip.NewResponseFromRequest(req, sip.StatusBadRequest, err.Error(), nil))
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	d.lastInvite = req

	if err := d.sdpReInvite(req.Body()); err != nil {
		tx.Respond(sip.NewResponseFromRequest(req, sip.StatusRequestTerminated, err.Error(), nil))
		return
	}

	tx.Respond(sip.NewResponseFromRequest(req, sip.StatusOK, "OK", nil))
}

func (d *DialogClientSession) readSIPInfoDTMF(req *sip.Request, tx sip.ServerTransaction) {

}
