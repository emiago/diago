// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2026, Emir Aganovic

package diago

import (
	"context"
	"errors"
	"fmt"
	"slices"

	"github.com/emiago/diago/media"
	"github.com/emiago/sipgo/sip"
	"github.com/pion/ice/v4"
)

// AnswerWebrtcOptions configures an inbound SIP call using diago's direct
// ICE + DTLS-SRTP media stack. A temporary DTLS certificate is generated when
// the configuration does not provide one.
type AnswerWebrtcOptions struct {
	OnRefer func(*DialogClientSession) error
	Codecs  []media.Codec

	WebrtcConfig media.MediaSessionWebrtcConfig
	// OnICEStateChange observes ICE transport state. It must not block.
	OnICEStateChange func(ice.ConnectionState)
}

// AnswerWebrtc consumes the WebRTC SDP offer, sends a SIP 200 answer and
// completes ICE/DTLS-SRTP negotiation before returning.
func (d *DialogServerSession) AnswerWebrtc(opts AnswerWebrtcOptions) (*DialogWebrtc, error) {
	remoteSDP := d.InviteRequest.Body()
	if remoteSDP == nil {
		return nil, fmt.Errorf("no SDP present in INVITE")
	}
	conf, err := prepareWebrtcConfig(opts.WebrtcConfig)
	if err != nil {
		return nil, err
	}
	codecs := opts.Codecs
	if len(codecs) == 0 {
		codecs = d.mediaConf.Codecs
	}
	sess := &media.MediaSessionWebrtc{Codecs: slices.Clone(codecs)}
	if err = sess.Init(d.Context(), conf, opts.OnICEStateChange); err != nil {
		return nil, err
	}
	med := &DialogWebrtc{}
	answered := false
	d.OnState(func(state sip.DialogState) {
		if state == sip.DialogStateEnded {
			_ = med.Close()
		}
	})

	err = func() error {
		if err = sess.RemoteSDP(d.Context(), remoteSDP, false); err != nil {
			return err
		}
		localSDP, err := sess.LocalSDP(d.Context(), true)
		if err != nil {
			return err
		}
		if err = d.RespondSDP(localSDP); err != nil {
			return err
		}
		answered = true
		if err = sess.Finalize(d.Context()); err != nil {
			return err
		}
		rtpSess := media.NewRTPSessionWebrtc(sess)
		med.init(sess, rtpSess)
		if err = rtpSess.MonitorBackground(); err != nil {
			return err
		}
		return nil
	}()
	if err != nil {
		cleanupErr := sess.Close()
		if answered {
			cleanupCtx, cancel := context.WithTimeout(context.Background(), webrtcFailureCleanupTimeout)
			defer cancel()
			cleanupErr = errors.Join(cleanupErr, d.Hangup(cleanupCtx))
		}
		return nil, errors.Join(err, cleanupErr)
	}

	med.registerDialogCallbacks(&d.dialogCallbacks)
	d.dialogCallbacks.mu.Lock()
	d.onReferDialog = opts.OnRefer
	d.onClose = append(d.onClose, med.Close)
	d.dialogCallbacks.mu.Unlock()
	return med, nil
}
