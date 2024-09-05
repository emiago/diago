// SPDX-License-Identifier: MPL-2.0
// Copyright (C) 2024 Emir Aganovic

package diago

import (
	"context"
	"fmt"
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

	// mu sync.Mutex We will reuse lock from Media
	// lastInvite is actual last invite sent by remote REINVITE
	// We do not use sipgo as this needs mutex but also keeping original invite
	lastInvite *sip.Request

	contactHDR sip.ContactHeader
}

func (d *DialogServerSession) Id() string {
	return d.ID
}

func (d *DialogServerSession) Close() {
	d.DialogMedia.Close()
	d.DialogServerSession.Close()
}

func (d *DialogServerSession) FromUser() string {
	return d.InviteRequest.From().Address.User
}

// User that was dialed
func (d *DialogServerSession) ToUser() string {
	return d.InviteRequest.To().Address.User
}

func (d *DialogServerSession) Progress() error {
	return d.Respond(sip.StatusTrying, "Trying", nil)
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

	if d.lastInvite != nil {
		return d.lastInvite.Contact()
	}
	return d.InviteRequest.Contact()
}

func (d *DialogServerSession) Respond(statusCode sip.StatusCode, reason string, body []byte, headers ...sip.Header) error {
	// TODO fix this on dialog srv
	headers = append(headers, &d.contactHDR)
	return d.DialogServerSession.Respond(statusCode, reason, body, headers...)
}

func (d *DialogServerSession) RespondSDP(body []byte) error {
	headers := []sip.Header{sip.NewHeader("Content-Type", "application/sdp")}
	headers = append(headers, &d.contactHDR)
	return d.DialogServerSession.Respond(200, "OK", body, headers...)
}

func (d *DialogServerSession) Answer() error {
	// TODO, lot of here settings need to come from TU. or TU must copy before shipping
	// We may have this settings
	// - Codecs
	// - RTP port ranges

	// For now we keep things global and hardcoded
	// Codecs are ulaw,alaw
	// RTP port range is not set

	// Now media SETUP
	// ip, port, err := sipgox.FindFreeInterfaceHostPort("udp", "")
	// if err != nil {
	// 	return err
	// }

	sess, err := d.createMediaSession()
	if err != nil {
		return err
	}

	rtpSess := media.NewRTPSession(sess)
	return d.AnswerWithSession(sess, rtpSess)
}

// AnswerWithSession. Not final API. It allows answering with custom media and rtpSess
func (d *DialogServerSession) AnswerWithSession(sess *media.MediaSession, rtpSess *media.RTPSession) error {
	sdp := d.InviteRequest.Body()
	if sdp == nil {
		return fmt.Errorf("no sdp present in INVITE")
	}

	if err := sess.RemoteSDP(sdp); err != nil {
		return err
	}

	d.InitMediaSession(
		sess,
		media.NewRTPPacketReaderSession(rtpSess),
		media.NewRTPPacketWriterSession(rtpSess),
	)
	// Must be called after media and reader writer is setup
	rtpSess.MonitorBackground()

	if err := d.RespondSDP(sess.LocalSDP()); err != nil {
		return err
	}

	// Wait ACK
	// If we do not wait ACK, hanguping call will fail as ACK can be delayed when we are doing Hangup
	for {
		select {
		case <-time.After(10 * time.Second):
			return fmt.Errorf("no ACK received")
		case state := <-d.State():
			if state == sip.DialogStateConfirmed {
				return nil
			}
		}
	}
}

func (d *DialogServerSession) Hangup(ctx context.Context) error {
	return d.Bye(ctx)
}

func (d *DialogServerSession) ReInvite(ctx context.Context) error {
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

func (d *DialogServerSession) handleReInvite(req *sip.Request, tx sip.ServerTransaction) {
	if err := d.ReadRequest(req, tx); err != nil {
		tx.Respond(sip.NewResponseFromRequest(req, sip.StatusBadRequest, err.Error(), nil))
		return
	}

	d.mu.Lock()
	defer d.mu.Unlock()
	d.lastInvite = req

	if err := d.sdpReInviteUnsafe(req.Body()); err != nil {
		tx.Respond(sip.NewResponseFromRequest(req, sip.StatusRequestTerminated, err.Error(), nil))
		return
	}

	tx.Respond(sip.NewResponseFromRequest(req, sip.StatusOK, "OK", nil))
}

func (d *DialogServerSession) readSIPInfoDTMF(req *sip.Request, tx sip.ServerTransaction) {
	tx.Respond(sip.NewResponseFromRequest(req, sip.StatusNotAcceptable, "Not Acceptable", nil))
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
