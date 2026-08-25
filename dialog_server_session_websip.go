// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2026, Emir Aganovic

package diago

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"

	"github.com/emiago/diago/media"
	"github.com/emiago/sipgo/sip"
	websip "github.com/emiago/websip"
	"github.com/pion/ice/v4"
)

type AnswerWebsipOptions struct {
	Codecs []media.Codec

	WebrtcConfig media.MediaSessionWebrtcConfig
	// OnICEStateChange must not block.
	OnICEStateChange func(ice.ConnectionState)
}

type DialogWebsipServerSession struct {
	*websip.DialogServer
	dialogWebsipMedia
	mediaConf MediaConfig

	stateMu   sync.Mutex
	answering bool
	answered  bool
	cancelled bool
}

func newDialogWebsipServerSession(dialog *websip.DialogServer, conf MediaConfig) *DialogWebsipServerSession {
	return &DialogWebsipServerSession{DialogServer: dialog, mediaConf: conf}
}

func (d *DialogWebsipServerSession) Id() string { return d.ID() }

func (d *DialogWebsipServerSession) FromUser() string {
	if from := d.InviteRequest.From(); from != nil {
		return from.Address.User
	}
	return ""
}

func (d *DialogWebsipServerSession) ToUser() string {
	if to := d.InviteRequest.To(); to != nil {
		return to.Address.User
	}
	return ""
}

// Answer consumes the INVITE SDP offer and establishes direct
// ICE + DTLS-SRTP media. Websip does not wait for an ACK.
func (d *DialogWebsipServerSession) Answer(options AnswerWebsipOptions) (*DialogWebrtc, error) {
	remoteSDP := d.InviteRequest.Body()
	if !isSDPMessage(d.InviteRequest.ContentType(), remoteSDP) {
		return nil, fmt.Errorf("Websip INVITE has no application/sdp offer")
	}
	d.stateMu.Lock()
	if d.answering || d.answered {
		d.stateMu.Unlock()
		return nil, fmt.Errorf("Websip dialog answer is already in progress")
	}
	if d.cancelled || d.State() == websip.DialogTerminated {
		d.stateMu.Unlock()
		return nil, websip.ErrClosed
	}
	d.answering = true
	d.stateMu.Unlock()
	defer func() {
		d.stateMu.Lock()
		d.answering = false
		d.stateMu.Unlock()
	}()

	conf, err := prepareWebrtcConfig(options.WebrtcConfig)
	if err != nil {
		return nil, err
	}
	mediaConf := cloneWebsipMediaConfig(d.mediaConf)
	mediaConf.update(options.Codecs, 0)
	sess := &media.MediaSessionWebrtc{Codecs: slices.Clone(mediaConf.Codecs)}
	if err = sess.Init(d.Context(), conf, options.OnICEStateChange); err != nil {
		return nil, err
	}
	if err = sess.RemoteSDP(d.Context(), remoteSDP, false); err != nil {
		return nil, errors.Join(err, sess.Close())
	}
	localSDP, err := sess.LocalSDP(d.Context(), true)
	if err != nil {
		return nil, errors.Join(err, sess.Close())
	}

	d.stateMu.Lock()
	if d.cancelled || d.State() == websip.DialogTerminated {
		d.stateMu.Unlock()
		return nil, errors.Join(websip.ErrClosed, sess.Close())
	}
	err = d.DialogServer.Answer(localSDP)
	if err == nil {
		d.answered = true
	}
	d.stateMu.Unlock()
	if err != nil {
		return nil, errors.Join(err, sess.Close())
	}

	med := &DialogWebrtc{}
	if err = finalizeWebrtcMedia(d.Context(), sess, med); err != nil {
		return nil, errors.Join(err, sess.Close(), d.cleanupAnsweredCall())
	}
	if err = d.attach(med); err != nil {
		return nil, errors.Join(err, d.cleanupAnsweredCall())
	}
	return med, nil
}

func (d *DialogWebsipServerSession) cleanupAnsweredCall() error {
	ctx, cancel := context.WithTimeout(context.Background(), webrtcFailureCleanupTimeout)
	defer cancel()
	return d.DialogServer.Hangup(ctx)
}

func (d *DialogWebsipServerSession) Hangup(ctx context.Context) error {
	return d.DialogServer.Hangup(ctx)
}

func (d *DialogWebsipServerSession) Close() error {
	return errors.Join(d.closeMedia(), d.DialogServer.Close())
}

func (d *DialogWebsipServerSession) handleRequest(tx *websip.Transaction) {
	if tx == nil || tx.Request == nil {
		return
	}
	switch tx.Request.Method {
	case sip.BYE:
		_ = errors.Join(tx.Respond(sip.StatusOK, "OK", nil), d.Close())
	case sip.CANCEL:
		d.handleCancel(tx)
	case sip.ACK:
		// ACK is tolerated for gateway compatibility but is not required.
	default:
		_ = tx.Respond(sip.StatusNotImplemented, "Not Implemented", nil)
	}
}

func (d *DialogWebsipServerSession) handleCancel(tx *websip.Transaction) {
	d.stateMu.Lock()
	defer d.stateMu.Unlock()
	if d.answered || d.State() == websip.DialogEstablished {
		_ = tx.Respond(sip.StatusConflict, "Conflict", nil)
		return
	}
	if d.cancelled {
		_ = tx.Respond(sip.StatusOK, "OK", nil)
		return
	}
	d.cancelled = true
	responseErr := tx.Respond(sip.StatusOK, "OK", nil)
	inviteErr := d.DialogServer.Respond(sip.StatusRequestTerminated, "Request Terminated", nil)
	_ = errors.Join(responseErr, inviteErr, d.closeMedia())
}
