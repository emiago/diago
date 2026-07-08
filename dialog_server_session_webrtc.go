package diago

import (
	"context"
	"errors"

	"github.com/emiago/diago/media"
	"github.com/emiago/diago/media/sdp"
	"github.com/emiago/diago/mediawebrtc"
	"github.com/emiago/sipgo/sip"
	"github.com/pion/webrtc/v3"
)

type AnswerWebrtcOptions struct {
	// OnMediaUpdate triggers when media update happens. It is blocking func, so make sure you exit
	OnMediaUpdate func(d *DialogWebrtc)

	// OnRefer is called on successfull REFER handling
	//
	// It creates new dialog (NewDialog) on which you need to call Invite() and Ack()
	// Any error from invite, ack or other processing should be returned for correct Notify handling
	//
	// NOTE: IT is SCOPED to handler and exiting handler will Close/Terminate this dialog!
	OnRefer func(referDialog *DialogClientSession) error
	// Codecs that will be used
	Codecs []media.Codec

	WebrtcConfig webrtc.Configuration
}

func (d *DialogServerSession) AnswerWebrtc(opts AnswerWebrtcOptions) (*DialogWebrtc, error) {
	m := &DialogWebrtc{
		log: sip.DefaultLogger().With("call_id", d.InviteRequest.CallID().Value()),
	}

	if len(opts.Codecs) == 0 {
		opts.Codecs = d.mediaConf.Codecs
	}

	d.OnState(func(s sip.DialogState) {
		if s == sip.DialogStateEnded {
			m.Close()
		}
	})

	return m, d.answerWebrtc(m, d.InviteRequest.Body(), opts)
}

func (d *DialogServerSession) answerWebrtc(m *DialogWebrtc, sdpBody []byte, opts AnswerWebrtcOptions) error {
	sess := &mediawebrtc.MediaSession{
		Codecs: opts.Codecs,
	}
	if err := sess.Init(opts.WebrtcConfig); err != nil {
		return err
	}

	err := func() error {
		if err := sess.RemoteSDP(context.TODO(), sdpBody, false); err != nil {
			return err
		}

		localSDP, err := sess.LocalSDP(context.TODO(), true)
		if err != nil {
			return err
		}

		if err := d.RespondSDP(localSDP); err != nil {
			return err
		}

		if err := sess.Finalize(context.TODO()); err != nil {
			return err
		}
		return nil
	}()

	if err != nil {
		return errors.Join(err, sess.Close())
	}

	m.OnClose(func() error {
		return sess.Close()
	})

	m.mediaSession = sess
	m.registerDialogCallbacks(&d.dialogCallbacks, opts.OnMediaUpdate)

	// Make this faster access for now
	m.RTPPacketReader = m.mediaSession.RTPPacketReader
	m.RTPPacketWriter = m.mediaSession.RTPPacketWriter
	return nil
}

func sdReadAddress(sd sdp.SessionDescription) string {
	ci, _ := sd.ConnectionInformation()
	if ci.IP == nil {
		return ""
	}

	return ci.IP.String()
}
