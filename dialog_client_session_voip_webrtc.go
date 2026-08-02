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
)

// InviteVoipWebrtcOptions configures a SIP call using diago's direct ICE +
// DTLS-SRTP media stack. A temporary DTLS certificate is generated when the
// configuration does not provide one.
type InviteVoipWebrtcOptions struct {
	OnResponse func(*sip.Response) error
	OnRefer    OnReferDialogFunc
	Username   string
	Password   string
	Headers    []sip.Header
	// Stop after WebRTC media is established from a 183 Session Progress
	// response. InviteVoipWebrtc returns the usable media together with
	// ErrClientEarlyMedia; call WaitAnswerVoipWebrtc to finish the call.
	EarlyMediaDetect bool

	WebrtcConfig media.MediaSessionWebrtcConfig
}

// InviteVoipWebrtc sends an SDP offer, completes ICE/DTLS-SRTP negotiation and
// returns encoded audio RTP access for the established SIP dialog.
//
// When EarlyMediaDetect is enabled and a 183 response contains SDP, the method
// establishes the WebRTC transport and returns it with ErrClientEarlyMedia.
// The caller can use the returned media immediately, then call
// WaitAnswerVoipWebrtc to wait for the final response and ACK it.
func (d *DialogClientSession) InviteVoipWebrtc(ctx context.Context, opts InviteVoipWebrtcOptions) (*DialogVoipWebrtc, error) {
	conf, err := prepareVoipWebrtcConfig(opts.WebrtcConfig)
	if err != nil {
		return nil, err
	}
	sess := &media.MediaSessionWebrtc{Codecs: slices.Clone(d.mediaConfig.Codecs)}
	if err = sess.Init(ctx, conf); err != nil {
		return nil, err
	}
	med := &DialogVoipWebrtc{}

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
				if err := setupVoipWebrtcMedia(ctx, sess, med, res.Body()); err != nil {
					return err
				}
				return ErrClientEarlyMedia
			}
		}
		if err = d.DialogClientSession.WaitAnswer(ctx, answerOpts); err != nil {
			return err
		}
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
		return finalizeVoipWebrtcMedia(ctx, sess, med)
	}()
	if err != nil {
		if errors.Is(err, ErrClientEarlyMedia) && med.MediaSession() != nil {
			d.registerVoipWebrtcDialogCallbacks(med, opts.OnRefer)
			return med, err
		}
		return nil, errors.Join(err, sess.Close())
	}

	d.registerVoipWebrtcDialogCallbacks(med, opts.OnRefer)
	return med, nil
}

func setupVoipWebrtcMedia(ctx context.Context, sess *media.MediaSessionWebrtc, med *DialogVoipWebrtc, remoteSDP []byte) error {
	if err := sess.RemoteSDP(ctx, remoteSDP, true); err != nil {
		return err
	}
	return finalizeVoipWebrtcMedia(ctx, sess, med)
}

func finalizeVoipWebrtcMedia(ctx context.Context, sess *media.MediaSessionWebrtc, med *DialogVoipWebrtc) error {
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

func (d *DialogClientSession) registerVoipWebrtcDialogCallbacks(med *DialogVoipWebrtc, onRefer OnReferDialogFunc) {
	d.dialogCallbacks.mu.Lock()
	d.onReferDialog = onRefer
	d.onClose = append(d.onClose, med.Close)
	d.dialogCallbacks.mu.Unlock()
}

// WaitAnswerVoipWebrtc continues an InviteVoipWebrtc call that returned
// ErrClientEarlyMedia. The early WebRTC transport remains active while this
// method waits for the final response; on success it sends the ACK.
func (d *DialogClientSession) WaitAnswerVoipWebrtc(ctx context.Context, med *DialogVoipWebrtc, opts sipgo.AnswerOptions) error {
	if med == nil || med.MediaSession() == nil {
		return fmt.Errorf("dialog WebRTC media is not initialized")
	}
	if err := d.DialogClientSession.WaitAnswer(ctx, opts); err != nil {
		return err
	}
	return d.Ack(ctx)
}
