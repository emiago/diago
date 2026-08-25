// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2026, Emir Aganovic

package diago

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/emiago/diago/media"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	websip "github.com/emiago/websip"
	"github.com/pion/ice/v4"
)

type InviteWebsipOptions struct {
	OnResponse func(*sip.Response) error
	Headers    []sip.Header

	WebrtcConfig media.MediaSessionWebrtcConfig
	// OnICEStateChange must not block.
	OnICEStateChange func(ice.ConnectionState)
}

type DialogWebsipClientSession struct {
	*websip.DialogClient
	dialogWebsipMedia
	mediaConfig MediaConfig
}

func newDialogWebsipClientSession(dialog *websip.DialogClient, conf MediaConfig) *DialogWebsipClientSession {
	return &DialogWebsipClientSession{DialogClient: dialog, mediaConfig: conf}
}

func (d *DialogWebsipClientSession) Id() string { return d.ID() }

func (d *DialogWebsipClientSession) FromUser() string {
	if from := d.InviteRequest.From(); from != nil {
		return from.Address.User
	}
	return ""
}

func (d *DialogWebsipClientSession) ToUser() string {
	if to := d.InviteRequest.To(); to != nil {
		return to.Address.User
	}
	return ""
}

// Invite establishes direct ICE + DTLS-SRTP media from the final 2xx SDP
// response. Provisional responses never create media and ACK is not sent.
func (d *DialogWebsipClientSession) Invite(ctx context.Context, options InviteWebsipOptions) (*DialogWebrtc, error) {
	conf, err := prepareWebrtcConfig(options.WebrtcConfig)
	if err != nil {
		return nil, err
	}
	sess := &media.MediaSessionWebrtc{Codecs: slices.Clone(d.mediaConfig.Codecs)}
	if err = sess.Init(ctx, conf, options.OnICEStateChange); err != nil {
		return nil, err
	}
	localSDP, err := sess.LocalSDP(ctx, false)
	if err != nil {
		return nil, errors.Join(err, sess.Close())
	}
	requestOptions := make([]websip.RequestOption, 0, len(options.Headers))
	for _, header := range options.Headers {
		if header == nil {
			return nil, errors.Join(fmt.Errorf("invite header is nil"), sess.Close())
		}
		requestOptions = append(requestOptions, websip.WithHeader(header))
	}
	response, err := d.DialogClient.Invite(ctx, localSDP, requestOptions...)
	if err != nil {
		return nil, errors.Join(err, sess.Close())
	}
	if options.OnResponse != nil {
		if err = options.OnResponse(response); err != nil {
			return nil, errors.Join(err, sess.Close(), d.cleanupAnsweredCall())
		}
	}
	if response.StatusCode < 200 || response.StatusCode >= 300 {
		return nil, errors.Join(sipgo.ErrDialogResponse{Res: response}, sess.Close())
	}
	if !isSDPMessage(response.ContentType(), response.Body()) {
		return nil, errors.Join(fmt.Errorf("Websip INVITE response has no application/sdp answer"), sess.Close(), d.cleanupAnsweredCall())
	}
	if err = sess.RemoteSDP(ctx, response.Body(), true); err != nil {
		return nil, errors.Join(err, sess.Close(), d.cleanupAnsweredCall())
	}
	med := &DialogWebrtc{}
	if err = finalizeWebrtcMedia(ctx, sess, med); err != nil {
		return nil, errors.Join(err, sess.Close(), d.cleanupAnsweredCall())
	}
	if err = d.attach(med); err != nil {
		return nil, errors.Join(err, d.cleanupAnsweredCall())
	}
	return med, nil
}

func (d *DialogWebsipClientSession) cleanupAnsweredCall() error {
	if d.State() != websip.DialogEstablished {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), webrtcFailureCleanupTimeout)
	defer cancel()
	return d.DialogClient.Hangup(ctx)
}

func (d *DialogWebsipClientSession) Hangup(ctx context.Context) error {
	return d.DialogClient.Hangup(ctx)
}

func (d *DialogWebsipClientSession) Close() error {
	return errors.Join(d.closeMedia(), d.DialogClient.Close())
}

func (d *DialogWebsipClientSession) handleRequest(tx *websip.Transaction) {
	if tx == nil || tx.Request == nil {
		return
	}
	switch tx.Request.Method {
	case sip.BYE:
		_ = errors.Join(tx.Respond(sip.StatusOK, "OK", nil), d.Close())
	case sip.CANCEL:
		_ = tx.Respond(sip.StatusConflict, "Conflict", nil)
	case sip.ACK:
		// ACK is tolerated for gateway compatibility but is not part of the
		// DiagoWebsip lifecycle.
	default:
		_ = tx.Respond(sip.StatusNotImplemented, "Not Implemented", nil)
	}
}
