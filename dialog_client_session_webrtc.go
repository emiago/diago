package diago

import (
	"context"
	"errors"
	"fmt"

	"github.com/emiago/diago/media"
	mediasdp "github.com/emiago/diago/media/sdp"
	"github.com/emiago/diago/mediawebrtc"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/pion/webrtc/v3"
)

type InviteWebrtcOptions struct {
	Originator DialogSession
	OnResponse func(res *sip.Response) error
	// OnMediaUpdate called when media is changed.
	// NOTE: you should not block this call as it blocks response processing.
	OnMediaUpdate func(d *DialogWebrtc)
	// OnRefer is called on successfull REFER handling
	//
	// It creates new dialog (NewDialog) on which you need to call Invite() and Ack()
	// Any error from invite, ack or other processing should be returned for correct Notify handling
	//
	// NOTE: IT is SCOPED to handler and exiting handler will Close/Terminate this dialog!
	// OnRefer OnReferDialogFunc
	// For digest authentication
	Username string
	Password string

	// Custom headers to pass. DO NOT SET THIS to nil
	Headers []sip.Header
	// Stop on early media. ErrClientEarlyMedia will be returned
	EarlyMediaDetect bool

	WebrtcConfig webrtc.Configuration
}

func (d *DialogClientSession) InviteWebrtc(ctx context.Context, opts InviteWebrtcOptions) (*DialogWebrtc, error) {
	m := &DialogWebrtc{}

	// TODO this can be racy
	d.Dialog.OnState(func(s sip.DialogState) {
		if s == sip.DialogStateEnded {
			m.Close()
		}

		if s == sip.DialogStateConfirmed {
			// Do some finalize on ACK?

		}
	})

	d.dialogCallbacks.mu.Lock()
	var pendingSession *mediawebrtc.MediaSession
	d.onRemoteSDP = func(ctx context.Context, remoteSDP []byte, offered bool) error {
		m.mu.Lock()
		defer m.mu.Unlock()

		if m.mediaSession == nil {
			return fmt.Errorf("reinvite called on non initialized media")
		}

		sess := m.mediaSession
		if !offered {
			sess = m.mediaSession.Fork()
			pendingSession = sess
		}

		if err := sess.RemoteSDP(ctx, remoteSDP, offered); err != nil {
			return err
		}
		if opts.OnMediaUpdate != nil {
			opts.OnMediaUpdate(m)
		}
		return nil
	}
	d.onLocalSDP = func(ctx context.Context, answered bool, mode string, mediaSession ...*media.MediaSession) ([]byte, error) {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.mediaSession == nil {
			return nil, fmt.Errorf("reinvite called on non initialized media")
		}
		sess := m.mediaSession
		if pendingSession != nil {
			sess = pendingSession
		}
		localSDP, err := sess.LocalSDP(ctx, answered)
		if err != nil {
			return nil, err
		}
		if pendingSession != nil && answered {
			m.mediaSession = pendingSession
			pendingSession = nil
		}
		return localSDP, nil
	}
	d.onFinalize = func(ctx context.Context) error {
		return nil
	}
	d.onClose = append(d.onClose, m.Close)
	d.dialogCallbacks.mu.Unlock()

	if err := d.inviteWebrtc(ctx, m, opts); err != nil {
		m.Close()
		return nil, err
	}

	if m.mediaSession.Codec().SampleRate == 0 {
		panic("no codec")
	}

	// Make this faster access for now
	m.RTPPacketReader = m.mediaSession.RTPPacketReader
	m.RTPPacketWriter = m.mediaSession.RTPPacketWriter

	return m, nil
}

func webrtcSDPMediaDirection(body []byte) string {
	sd := mediasdp.SessionDescription{}
	if err := mediasdp.Unmarshal(body, &sd); err != nil {
		return mediasdp.ModeSendrecv
	}

	direction := sd.MediaDirection()
	if direction == "" {
		return mediasdp.ModeSendrecv
	}
	return direction
}

func webrtcSDPAudioCodec(body []byte, current media.Codec) (media.Codec, error) {
	sd := mediasdp.SessionDescription{}
	if err := mediasdp.Unmarshal(body, &sd); err != nil {
		return current, err
	}

	md, err := sd.MediaDescription("audio")
	if err != nil {
		return current, err
	}

	remoteCodecs := make([]media.Codec, len(md.Formats))
	n, err := media.CodecsFromSDPRead(md.Formats, sd.Values("a"), remoteCodecs)
	if err != nil {
		return current, err
	}
	remoteCodecs = remoteCodecs[:n]

	for _, c := range remoteCodecs {
		switch c.PayloadType {
		case media.CodecAudioUlaw.PayloadType, media.CodecAudioAlaw.PayloadType:
			return c, nil
		}
	}

	return current, fmt.Errorf("reinvite has no supported webrtc audio codec: remote=%v", remoteCodecs)
}

func applyWebrtcRemoteCodec(sess *webrtcSession, rtpWriter *media.RTPPacketWriter, body []byte) error {
	if sess == nil || sess.writer == nil {
		return fmt.Errorf("webrtc media session is not initialized")
	}
	if rtpWriter == nil {
		return fmt.Errorf("webrtc rtp packet writer is not initialized")
	}

	codec, err := webrtcSDPAudioCodec(body, sess.Codec)
	if err != nil {
		return err
	}
	if codec == sess.Codec {
		return nil
	}

	mimeType, err := parseCodecMimeType(codec.PayloadType)
	if err != nil {
		return err
	}

	track, err := webrtc.NewTrackLocalStaticRTP(
		webrtc.RTPCodecCapability{MimeType: mimeType},
		"audio",
		"diago",
	)
	if err != nil {
		return err
	}

	if err := sess.writer.ReplaceTrack(track); err != nil {
		return err
	}

	sess.Codec = codec
	rtpWriter.UpdateWriter(sess.writer, codec)
	return nil
}

// func applyWebrtcRemoteDirection(writer *WebrtcTrackRTPWriter, remoteDirection string) error {
// 	shouldSend := remoteDirection == mediasdp.ModeSendrecv || remoteDirection == mediasdp.ModeRecvonly || remoteDirection == ""
// 	return writer.UpdateDirection(shouldSend)
// }

func (d *DialogClientSession) inviteWebrtc(ctx context.Context, m *DialogWebrtc, opts InviteWebrtcOptions) error {
	sess := &mediawebrtc.MediaSession{
		// Keep the WebRTC media session aligned with the dialog codec config so
		// later re-INVITEs can negotiate against the same codec set.
		Codecs: d.mediaConfig.Codecs,
	}
	if err := sess.Init(opts.WebrtcConfig); err != nil {
		return err
	}

	err := func() error {
		sd, err := sess.LocalSDP(ctx, false)
		if err != nil {
			return err
		}

		if err := d.doInvite(ctx, sd); err != nil {
			return err
		}

		if err := d.DialogClientSession.WaitAnswer(ctx, sipgo.AnswerOptions{
			OnResponse: opts.OnResponse,
			Username:   opts.Username,
			Password:   opts.Password,
		}); err != nil {
			return err
		}

		remoteSD := d.InviteResponse.Body()
		if err := sess.RemoteSDP(ctx, remoteSD, true); err != nil {
			return err
		}

		if err := d.Ack(ctx); err != nil {
			return err
		}

		if err := sess.Finalize(ctx); err != nil {
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
	return nil
}
