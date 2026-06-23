package diago

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/emiago/diago/media"
	mediasdp "github.com/emiago/diago/media/sdp"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/google/uuid"
	"github.com/pion/webrtc/v3"
)

// InviteV2 does SIP invite and return media stack.
// If early media detection is enabled you will get error and media stack with error = ErrClientEarlyMedia
func (d *DialogClientSession) InviteV2(ctx context.Context, opts InviteClientOptions) (*DialogMedia, error) {
	med := &DialogMedia{}
	if err := med.initMediaSessionFromConf(d.mediaConfig); err != nil {
		return nil, err
	}

	// NOTE: this can be racy
	d.Dialog.OnState(func(s sip.DialogState) {
		if s == sip.DialogStateEnded {
			med.Close()
		}

		if s == sip.DialogStateConfirmed {
			// Do some finalize on ACK?
		}
	})

	if err := d.invite(ctx, med, opts); err != nil {
		if errors.Is(err, ErrClientEarlyMedia) {
			return med, err
		}
		med.Close()
		return nil, err
	}

	return med, nil
}

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

	d.onReInvite = func(req *sip.Request) (*sip.Response, error) {
		// Handle media reinvite
		if req.IsAck() {
			// This should be on our reinvite handling
			// For now just handle without error
			return nil, nil
		}

		res := sip.NewResponseFromRequest(req, 200, "OK", nil)
		// remoteDirection := webrtcSDPMediaDirection(req.Body())

		err := func(sdp []byte) error {
			m.mu.Lock()
			defer m.mu.Unlock()

			if m.peerConnection == nil {
				return fmt.Errorf("reinvite called on non initialized media")
			}
			if m.mediaSession == nil || m.mediaSession.writer == nil {
				return fmt.Errorf("reinvite called before webrtc media writer is initialized")
			}

			err := m.peerConnection.SetRemoteDescription(webrtc.SessionDescription{
				Type: webrtc.SDPTypeOffer,
				SDP:  string(sdp),
			})
			if err != nil {
				return err
			}

			if err := applyWebrtcRemoteCodec(m.mediaSession, m.RTPPacketWriter, sdp); err != nil {
				return err
			}

			// if err := applyWebrtcRemoteDirection(m.mediaSession.writer, remoteDirection); err != nil {
			// 	return err
			// }

			answer, err := m.peerConnection.CreateAnswer(nil)
			if err != nil {
				return err
			}

			gatherComplete := webrtc.GatheringCompletePromise(m.peerConnection)
			if err := m.peerConnection.SetLocalDescription(answer); err != nil {
				return err
			}
			<-gatherComplete

			ld := m.peerConnection.LocalDescription()
			res = sip.NewResponseFromRequest(req, 200, "OK", []byte(ld.SDP))
			res.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
			return nil
		}(req.Body())

		if err != nil {
			return nil, err
		}

		if opts.OnMediaUpdate != nil {
			opts.OnMediaUpdate(m)
		}
		return res, nil
	}

	if err := d.inviteWebrtc(ctx, m, opts); err != nil {
		m.Close()
		return nil, err
	}

	if m.mediaSession.Codec.SampleRate == 0 {
		panic("no codec")
	}

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
	api := defaultWebrtcAPI
	log := slog.With("peer_connection", uuid.New().String(), "caller", "InviteWebrtc")

	if len(opts.WebrtcConfig.ICEServers) == 0 {
		opts.WebrtcConfig = defaultWebrtcConfig
	}

	peerConnection, err := api.NewPeerConnection(opts.WebrtcConfig)
	if err != nil {
		return err
	}

	m.OnClose(func() error {
		return peerConnection.Close()
	})

	// Set the handler for ICE connection state
	// This will notify you when the peer has connected/disconnected
	// iceConnectedCtx, iceConnectedCancel := context.WithCancel(context.TODO())
	peerConnection.OnICEConnectionStateChange(func(connectionState webrtc.ICEConnectionState) {
		log.Debug("Connection State has changed", "state", connectionState.String())

		if connectionState == webrtc.ICEConnectionStateFailed {
			if closeErr := peerConnection.Close(); closeErr != nil {
				fmt.Println("ICE close", err)
			}
		}

		// if connectionState == webrtc.ICEConnectionStateConnected {
		// 	iceConnectedCancel()
		// }
	})

	peerConnectedCtx, peerConnectedCancel := context.WithCancel(context.TODO())
	peerConnection.OnConnectionStateChange(func(connectionState webrtc.PeerConnectionState) {
		log.Debug("Peer Connection State has changed", "state", connectionState.String())
		if connectionState == webrtc.PeerConnectionStateConnected {
			peerConnectedCancel()
		}
	})

	// TODO mapping
	writeAudioTrack, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMU}, "audio", "diago")
	if err != nil {
		return err
	}

	rtpSender, err := peerConnection.AddTrack(writeAudioTrack)
	if err != nil {
		return err
	}

	// Handling originator media codecs to avoid transcoding!
	sd, err := peerConnection.CreateOffer(&webrtc.OfferOptions{
		OfferAnswerOptions: webrtc.OfferAnswerOptions{},
		ICERestart:         false,
	})
	if err != nil {
		return err
	}

	// Create channel that is blocked until ICE Gathering is complete
	gatherComplete := webrtc.GatheringCompletePromise(peerConnection)
	if err = peerConnection.SetLocalDescription(sd); err != nil {
		return err
	}

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-gatherComplete:
	}

	// Now lets build media session from webrtc stack
	// sess := &media.MediaSession{}
	// Lets override with sdp
	// localSDP := peerConnection.LocalDescription().SDP
	// if err := sess.InitWithSDP([]byte(localSDP)); err != nil {
	// 	return err
	// }

	// codec := sess.Codecs[0]
	codec := media.CodecAudioUlaw
	// codecMimeType, _ := parseCodecMimeType(codec.PayloadType)

	// We are using own packetizer to send or read rtp
	nilReader := newRTPNilReader()
	rtpReader := media.NewRTPPacketReader(nilReader, codec)
	m.RTPPacketReader = rtpReader
	m.mediaSession = &webrtcSession{
		Codec: codec,
	}
	log.Info("Setting rtp packet reader", "codec", m.mediaSession.Codec)

	peerConnection.OnTrack(func(remoteTrack *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) { //nolint: revive
		// ioReader := &WebrtcTrackRTPReader{
		// 	track:    remoteTrack,
		// 	receiver: receiver,
		// }

		// readMimeType := ioReader.track.Codec().MimeType
		// log.Debug("Webrtc remote track started", "mime_type", readMimeType)
		// if codecMimeType != ioReader.track.Codec().MimeType {
		// 	log.Info("Read media codec type received is not expected", "mime_type", readMimeType)
		// }

		// rtpReader.UpdateReader(ioReader)
		// m.mu.Lock()
		// m.mediaSession.reader = ioReader
		// m.mu.Unlock()
		// nilReader.Close()
	})

	// writer := &WebrtcTrackRTPWriter{
	// 	track:   writeAudioTrack,
	// 	sender:  rtpSender,
	// 	enabled: true,
	// }

	log.Info("Invite media session setup", "codec", codec.String())
	// rtpWriter := media.NewRTPPacketWriter(writer, codec)
	// m.RTPPacketWriter = rtpWriter
	// d.Media().InitMediaSession(sess, rtpReader, rtpWriter)

	inviteReq := d.InviteRequest

	// We have to manually do INVITE
	dialogCli := d.UA
	inviteReq.AppendHeader(&dialogCli.ContactHDR)
	inviteReq.AppendHeader(sip.NewHeader("Content-Type", "application/sdp"))
	inviteReq.SetBody([]byte(peerConnection.LocalDescription().SDP))

	// We allow changing full from header, but we need to make sure it is correctly set
	if fromHDR := inviteReq.From(); fromHDR != nil {
		fromHDR.Params.Add("tag", sip.GenerateTagN(16))
	}

	// Build here request
	client := d.UA.Client
	if err := sipgo.ClientRequestBuild(client, inviteReq); err != nil {
		return err
	}

	// This only gets called after session established
	// d.onMediaUpdate = opts.OnMediaUpdate
	err = d.DialogClientSession.Invite(ctx, func(c *sipgo.Client, req *sip.Request) error {
		// Do nothing
		return nil
	})
	if err != nil {
		// sess.Close()
		return err
	}
	log = log.With("call_id", d.InviteRequest.CallID().Value())

	if err := d.DialogClientSession.WaitAnswer(ctx, sipgo.AnswerOptions{
		OnResponse: opts.OnResponse,
		Username:   opts.Username,
		Password:   opts.Password,
	}); err != nil {
		return err
	}

	// for completness
	// if err := d.MediaSession().RemoteSDP(d.InviteResponse.Body()); err != nil {
	// 	return err
	// }

	// Webrtc
	sda := webrtc.SessionDescription{
		Type: webrtc.SDPTypeAnswer,
		SDP:  string(d.InviteResponse.Body()),
	}

	if err := peerConnection.SetRemoteDescription(sda); err != nil {
		return err
	}

	if err := d.Ack(ctx); err != nil {
		return err
	}

	// log.Debug("Waiting for ICE connected")
	// select {
	// case <-ctx.Done():
	// 	return fmt.Errorf("waiting ICE connected failed: %w", ctx.Err())
	// case <-iceConnectedCtx.Done():
	// }

	// This is more required
	log.Debug("Waiting for peer connection connected")
	select {
	case <-ctx.Done():
		return fmt.Errorf("waiting peer connection connected failed: %w", ctx.Err())
	case <-peerConnectedCtx.Done():
	}

	// connStats, _ := peerConnection.GetStats().GetConnectionStats(peerConnection)
	// Read incoming RTCP packets
	// Before these packets are returned they are processed by interceptors. For things
	// like NACK this needs to be called.
	go func() {
		log.Debug("Webrtc reading remote RTCP")
		defer log.Debug("Webrtc reading remote RTCP stopped")
		rtcpBuf := make([]byte, media.RTPBufSize)
		for {
			_, _, rtcpErr := rtpSender.Read(rtcpBuf)
			if rtcpErr != nil {
				return
			}
		}
	}()

	logICECandidatePairs(log, rtpSender)

	m.peerConnection = peerConnection
	// m.mediaSession.writer = writer

	return nil
}
