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
	"github.com/pion/ice/v4"
)

// InviteWebrtcOptions configures a SIP call using diago's direct ICE +
// DTLS-SRTP media stack. A temporary DTLS certificate is generated when the
// configuration does not provide one.
type InviteWebrtcOptions struct {
	OnResponse func(*sip.Response) error
	OnRefer    OnReferDialogFunc
	Username   string
	Password   string
	Headers    []sip.Header
	// Stop after WebRTC media is established from a 183 Session Progress
	// response. InviteWebrtc returns the usable media together with
	// ErrClientEarlyMedia; call WaitAnswerWebrtc to finish the call.
	EarlyMediaDetect bool

	WebrtcConfig media.MediaSessionWebrtcConfig
	// OnICEStateChange observes ICE transport state. It must not block.
	OnICEStateChange func(ice.ConnectionState)
}

// InviteWebrtc sends an SDP offer, completes ICE/DTLS-SRTP negotiation and
// returns encoded audio RTP access for the established SIP dialog.
//
// When EarlyMediaDetect is enabled and a 183 response contains SDP, the method
// establishes the WebRTC transport and returns it with ErrClientEarlyMedia.
// The caller can use the returned media immediately, then call
// WaitAnswerWebrtc to wait for the final response and ACK it.
func (d *DialogClientSession) InviteWebrtc(ctx context.Context, opts InviteWebrtcOptions) (*DialogWebrtc, error) {
	conf, err := prepareWebrtcConfig(opts.WebrtcConfig)
	if err != nil {
		return nil, err
	}
	sess := &media.MediaSessionWebrtc{Codecs: slices.Clone(d.mediaConfig.Codecs)}
	if err = sess.Init(ctx, conf, opts.OnICEStateChange); err != nil {
		return nil, err
	}
	med := &DialogWebrtc{}
	answered := false
	acked := false

	d.Dialog.OnState(func(state sip.DialogState) {
		if state == sip.DialogStateEnded {
			_ = med.Close()
		}
	})

	err = func() error {
		localSDP, err := sess.LocalSDP(ctx, false)
		if err != nil {
			return err
		}
		for _, header := range opts.Headers {
			if header == nil {
				return fmt.Errorf("invite header is nil")
			}
			d.InviteRequest.AppendHeader(header)
		}
		if err = d.doInvite(ctx, localSDP); err != nil {
			return err
		}
		answerOpts := sipgo.AnswerOptions{
			OnResponse: opts.OnResponse,
			Username:   opts.Username,
			Password:   opts.Password,
		}
		if opts.EarlyMediaDetect {
			onResponse := answerOpts.OnResponse
			answerOpts.OnResponse = func(res *sip.Response) error {
				if onResponse != nil {
					if err := onResponse(res); err != nil {
						return err
					}
				}
				if res.StatusCode != sip.StatusSessionInProgress {
					return nil
				}
				contentType := res.ContentType()
				if contentType == nil || contentType.Value() != "application/sdp" || res.Body() == nil {
					return nil
				}
				if err := setupWebrtcMedia(ctx, sess, med, res.Body()); err != nil {
					return err
				}
				return ErrClientEarlyMedia
			}
		}
		if err = d.DialogClientSession.WaitAnswer(ctx, answerOpts); err != nil {
			return err
		}
		answered = true
		remoteSDP := d.InviteResponse.Body()
		if remoteSDP == nil {
			return fmt.Errorf("no SDP in response")
		}
		if err = sess.RemoteSDP(ctx, remoteSDP, true); err != nil {
			return err
		}
		if err = d.Ack(ctx); err != nil {
			return err
		}
		acked = true
		return finalizeWebrtcMedia(ctx, sess, med)
	}()
	if err != nil {
		if errors.Is(err, ErrClientEarlyMedia) && med.MediaSession() != nil {
			d.registerWebrtcDialogCallbacks(med, opts.OnRefer)
			return med, err
		}
		cleanupErr := sess.Close()
		if answered {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), webrtcFailureCleanupTimeout)
			defer cancel()
			if !acked {
				cleanupErr = errors.Join(cleanupErr, d.Ack(cleanupCtx))
			}
			cleanupErr = errors.Join(cleanupErr, d.Hangup(cleanupCtx))
		}
		return nil, errors.Join(err, cleanupErr)
	}

	d.registerWebrtcDialogCallbacks(med, opts.OnRefer)
	return med, nil
}

func setupWebrtcMedia(ctx context.Context, sess *media.MediaSessionWebrtc, med *DialogWebrtc, remoteSDP []byte) error {
	if err := sess.RemoteSDP(ctx, remoteSDP, true); err != nil {
		return err
	}
	return finalizeWebrtcMedia(ctx, sess, med)
}

func finalizeWebrtcMedia(ctx context.Context, sess *media.MediaSessionWebrtc, med *DialogWebrtc) error {
	if err := sess.Finalize(ctx); err != nil {
		return err
	}
	rtpSess := media.NewRTPSessionWebrtc(sess)
	med.init(sess, rtpSess)
	if err := rtpSess.MonitorBackground(); err != nil {
		return errors.Join(err, rtpSess.Close())
	}
	return nil
}

func (d *DialogClientSession) registerWebrtcDialogCallbacks(med *DialogWebrtc, onRefer OnReferDialogFunc) {
	med.registerDialogCallbacks(&d.dialogCallbacks)
	d.dialogCallbacks.mu.Lock()
	d.onReferDialog = onRefer
	d.onClose = append(d.onClose, med.Close)
	d.dialogCallbacks.mu.Unlock()
}

// WaitAnswerWebrtc continues an InviteWebrtc call that returned
// ErrClientEarlyMedia. The early WebRTC transport remains active while this
// method waits for the final response; on success it sends the ACK.
func (d *DialogClientSession) WaitAnswerWebrtc(ctx context.Context, med *DialogWebrtc, opts sipgo.AnswerOptions) error {
	if med == nil || med.MediaSession() == nil {
		return fmt.Errorf("dialog WebRTC media is not initialized")
	}
	if err := d.DialogClientSession.WaitAnswer(ctx, opts); err != nil {
		return err
	}
	return d.Ack(ctx)
}
