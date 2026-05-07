package diago

import (
	"context"
	"fmt"
	"log/slog"

	"github.com/emiago/diago/media"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/google/uuid"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v3"
)

type InviteWebrtcOptions struct {
	Originator DialogSession
	OnResponse func(res *sip.Response) error
	// OnMediaUpdate called when media is changed.
	// NOTE: you should not block this call as it blocks response processing.
	// OnMediaUpdate func(d *DialogWebrtc)
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

	if err := d.inviteWebrtc(ctx, m, opts); err != nil {
		m.Close()
		return nil, err
	}
	return m, nil
}

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
	codecMimeType, _ := parseCodecMimeType(codec.PayloadType)

	// We are using own packetizer to send or read rtp
	nilReader := newRTPNilReader()
	rtpReader := media.NewRTPPacketReader(nilReader, codec)
	m.RTPPacketReader = rtpReader
	m.mediaSession = &webrtcSession{
		Codec: codec,
	}

	peerConnection.OnTrack(func(remoteTrack *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) { //nolint: revive
		ioReader := &WebrtcTrackRTPReader{
			track:    remoteTrack,
			receiver: receiver,
		}

		readMimeType := ioReader.track.Codec().MimeType
		log.Debug("Webrtc remote track started", "mime_type", readMimeType)
		if codecMimeType != ioReader.track.Codec().MimeType {
			log.Info("Read media codec type received is not expected", "mime_type", readMimeType)
		}

		rtpReader.UpdateReader(ioReader)
		m.mu.Lock()
		m.mediaSession.reader = ioReader
		m.mu.Unlock()
		nilReader.Close()
	})

	writer := &WebrtcTrackRTPWriter{
		track:  writeAudioTrack,
		sender: rtpSender,
	}

	log.Info("Invite media session setup", "codec", codec.String())
	rtpWriter := media.NewRTPPacketWriter(writer, codec)
	m.RTPPacketWriter = rtpWriter
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

	if err := d.DialogClientSession.WaitAnswer(ctx, sipgo.AnswerOptions{}); err != nil {
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
		pkts := make([]rtcp.Packet, 5)
		for {
			n, _, rtcpErr := rtpSender.Read(rtcpBuf)
			if rtcpErr != nil {
				return
			}
			// rtpSender.Track().Bind(&webrtc.AudioSenderStats{})

			n, err := media.RTCPUnmarshal(rtcpBuf[:n], pkts)
			if err != nil {
				log.Error("Failed to unmarshal RTCP", "error", err)
				continue
			}

			if media.RTCPDebug {
				for _, p := range pkts[:n] {
					log.Debug(fmt.Sprintf("RTCP write:\n%s", media.StringRTCP(p)))
				}
			}
		}
	}()

	logICECandidatePairs(log, rtpSender)

	m.peerConnection = peerConnection
	m.mediaSession.writer = writer

	return nil
}
