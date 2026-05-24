package diago

import (
	"context"
	"fmt"
	"time"

	"github.com/emiago/diago/media"
	"github.com/emiago/diago/media/sdp"
	"github.com/emiago/sipgo/sip"
	"github.com/google/uuid"
	"github.com/pion/rtcp"
	webrtcsdp "github.com/pion/sdp/v3"
	"github.com/pion/webrtc/v3"
)

// TODO: This should replace current one
func (d *DialogServerSession) AnswerV2(opt AnswerOptions) (*DialogMedia, error) {
	d.mu.Lock()
	d.onReferDialog = opt.OnRefer
	d.onMediaUpdate = opt.OnMediaUpdate
	d.mu.Unlock()

	m := &DialogMedia{}
	conf := d.mediaConf
	conf.update(opt.Codecs, opt.RTPNAT)
	if err := m.initMediaSessionFromConf(conf); err != nil {
		return nil, err
	}
	rtpSess := media.NewRTPSession(m.mediaSession)
	return m, d.answerSession(rtpSess)
}

// TODO Change answerOptions because codecs or RTPNAT makes no sense here
func (d *DialogServerSession) AnswerV2EarlyMedia(m *DialogMedia, opt AnswerOptions) error {
	d.mu.Lock()
	d.onReferDialog = opt.OnRefer
	d.onMediaUpdate = opt.OnMediaUpdate
	d.mu.Unlock()

	if err := d.RespondSDP(d.mediaSession.LocalSDP()); err != nil {
		return err
	}
	return nil
}

func (d *DialogServerSession) ProgressMediaV2(opts ProgressMediaOptions) (*DialogMedia, error) {
	codecs := opts.Codecs
	rtpNAT := opts.RTPNAT

	conf := d.mediaConf
	// Let override of formats
	if codecs != nil {
		conf.Codecs = codecs
	}
	conf.rtpNAT = rtpNAT

	med := &DialogMedia{}

	err := func() error {
		if err := med.initMediaSessionFromConf(conf); err != nil {
			return err
		}

		rtpSess := media.NewRTPSession(med.mediaSession)
		if err := med.setupRTPSession(d.InviteRequest.Body(), rtpSess); err != nil {
			return err
		}

		headers := []sip.Header{sip.NewHeader("Content-Type", "application/sdp")}
		body := rtpSess.Sess.LocalSDP()
		if err := d.DialogServerSession.Respond(183, "Session Progress", body, headers...); err != nil {
			return err
		}
		return rtpSess.MonitorBackground()
	}()
	return med, err
}

type AnswerWebrtcOptions struct {
	// OnMediaUpdate triggers when media update happens. It is blocking func, so make sure you exit
	OnMediaUpdate func(d *DialogMedia)

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

	return m, d.answerWebrtc(m, d.InviteRequest.Body(), opts)
}

func (d *DialogServerSession) answerWebrtc(m *DialogWebrtc, sdpBody []byte, opts AnswerWebrtcOptions) error {
	api := defaultWebrtcAPI
	log := m.log.With("peer_connection", uuid.New().String(), "caller", "AnswerWebrtc")

	// Create a new RTCPeerConnection
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
		log.Debug(fmt.Sprintf("Connection State has changed %s \n", connectionState.String()))

		if connectionState == webrtc.ICEConnectionStateFailed {
			if closeErr := peerConnection.Close(); closeErr != nil {
				log.Debug("ICE close", "error", err)
			}
		}

		// if connectionState == webrtc.ICEConnectionStateConnected || connectionState == webrtc.ICEConnectionStateCompleted {
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

	// Check remote codecs support, and return order in that way with our support
	// Set the remote SessionDescription
	sd := webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  string(sdpBody),
	}
	if err = peerConnection.SetRemoteDescription(sd); err != nil {
		return err
	}

	remoteSD, err := peerConnection.RemoteDescription().Unmarshal()
	if err != nil {
		return fmt.Errorf("failed to parse remote description: %w", err)
	}

	if len(remoteSD.MediaDescriptions) == 0 {
		return fmt.Errorf("no media descriptions found in SDP")
	}

	attrs := []string{}
	for _, a := range remoteSD.Attributes {
		attrs = append(attrs, a.String())
	}

	var audioMediaDesc *webrtcsdp.MediaDescription
	for _, md := range remoteSD.MediaDescriptions {
		if md.MediaName.Media == "audio" {
			audioMediaDesc = md
			break
		}
	}
	if audioMediaDesc == nil {
		return fmt.Errorf("answer webrtc: no audio media description present")
	}

	remoteFormats := audioMediaDesc.MediaName.Formats
	remoteCodecs := make([]media.Codec, len(remoteFormats))
	n, err := media.CodecsFromSDPRead(audioMediaDesc.MediaName.Formats, attrs, remoteCodecs)
	if err != nil {
		return err
	}
	remoteCodecs = remoteCodecs[:n]

	// localFormats := make([]string, 0, len(opts.Formats))
	localCodecs := make([]media.Codec, 0, len(opts.Codecs))
	// Order local formats based on remote
	log.Debug(fmt.Sprintf("Comparing formats remote=%v local=%v", remoteCodecs, opts.Codecs))
	for _, rf := range remoteCodecs {
		for _, lf := range opts.Codecs {
			if lf == rf {
				localCodecs = append(localCodecs, lf)
			}
		}
	}
	if len(localCodecs) == 0 {
		return fmt.Errorf("remote has no local codecs support, remote=%v local=%v", remoteFormats, opts.Codecs)
	}

	// Now use first to write
	codec := localCodecs[0]
	codecMimeType, err := func(f uint8) (string, error) {
		switch f {
		case media.CodecAudioUlaw.PayloadType:
			return webrtc.MimeTypePCMU, nil
		case media.CodecAudioAlaw.PayloadType:
			return webrtc.MimeTypePCMA, nil
		default:
			return "", fmt.Errorf("no mime type for format=%q", f)
		}
	}(codec.PayloadType)
	if err != nil {
		return err
	}

	// Create media session so that codecs are used correctly by diago
	log.Info("Answer media session setup", "codec", codec.String())
	// mess := &media.MediaSession{
	// 	// Formats: localFormats,
	// 	Codecs: localCodecs,
	// }

	// Allow us to receive 1 video track
	// if _, err = peerConnection.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio); err != nil {
	// 	fmt.Println("Add transceiver", err)
	// }
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// remoteTrackCh := make(chan *WebrtcTrackRTPReader)

	// Set a handler for when a new remote track starts, this just distributes all our packets
	// to connected peers

	// Create RTP Reader as placeholder until Track is received
	// HERE is the problem. If user has no MIC enabled or no traffic is comming in, we will never get this remote track
	// Pion waits some traffic in order to determine track SSRC and Payload type
	// Here is line where it waits for tracks.
	// 	github.com/pion/webrtc/v3.(*PeerConnection).startReceiver() /home/emia/Projects/public/webrtc/peerconnection.go:1239 (hits goroutine(109):1 total:1) (PC: 0xb9acee)
	// 	1238:		for _, t := range receiver.Tracks() {
	//   =>1239:			if t.SSRC() == 0 || t.RID() != "" {
	// 	1240:				return
	// 	1241:			}
	// 	1242:
	// 	1243:			go func(track *TrackRemote) {

	// SO track is generated and received
	// but when it executes track.peek it gets EOF
	// go func(track *TrackRemote) {
	// 	1244:				b := make([]byte, pc.api.settingEngine.getReceiveMTU())
	//   =>1245:				n, _, err := track.peek(b)

	// EOF is comming from reading RTP
	// 	> github.com/pion/transport/v2/packetio.(*Buffer).Read() /home/emia/go/pkg/mod/github.com/pion/transport/v2@v2.2.10/packetio/buffer.go:252 (PC: 0xa17a59)
	//    247:				return copied, nil
	//    248:			}
	//    249:
	//    250:			if b.closed {
	//    251:				b.mutex.Unlock()
	// => 252:				return 0, io.EOF
	//    253:			}

	// NOW WHY this buffer gets closed?

	// So what is workarround. Our RTPPacketReader is lucklily concurent safe
	// but we need to create some tmp io reader

	nilReader := newRTPNilReader()
	rtpReader := media.NewRTPPacketReader(nilReader, codec)
	m.mediaSession = &webrtcSession{Codec: codec}
	peerConnection.OnTrack(func(remoteTrack *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) { //nolint: revive
		// Create a local track, all our SFU clients will be fed via this track
		// remoteTrack.ReadRTP()
		// localTrack, newTrackErr := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMU}, "audio", "pion")
		// if newTrackErr != nil {
		// 	panic(newTrackErr)
		// }
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
		// d.Media().RTPPacketReader
		// Get what ever is current RTPPacketWriter
		// Normally this should be already in place

		// Adding this only for debugging
		// go func() {
		// 	log.Info("Webrtc reading remote RTCP")
		// 	defer log.Info("Webrtc reading remote RTCP stopped")
		// 	rtcpBuf := make([]byte, media.RTPBufSize)
		// 	pkts := make([]rtcp.Packet, 5)
		// 	for {
		// 		n, _, rtcpErr := receiver.Read(rtcpBuf)
		// 		if rtcpErr != nil {
		// 			return
		// 		}

		// 		n, err := media.RTCPUnmarshal(rtcpBuf[:n], pkts)
		// 		if err != nil {
		// 			log.Error("Failed to unmarshal RTCP", "error", err)
		// 			continue
		// 		}

		// 		if media.RTCPDebug {
		// 			for _, p := range pkts[:n] {
		// 				log.Debug(fmt.Sprintf("RTCP read:\n%s", media.StringRTCP(p)))
		// 			}
		// 		}
		// 	}
		// }()

		// 	// ErrClosedPipe means we don't have any subscribers, this is ok if no peers have connected yet
		// 	// if _, err = localTrack.Write(rtpBuf[:i]); err != nil && !errors.Is(err, io.ErrClosedPipe) {
		// 	// 	panic(err)
		// 	// }
		// }
	})

	writeAudioTrack, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: codecMimeType}, "audio", "diago")
	if err != nil {
		return err
	}

	rtpSender, err := peerConnection.AddTrack(writeAudioTrack)
	if err != nil {
		return err
	}

	answer, err := peerConnection.CreateAnswer(nil)
	if err != nil {
		return fmt.Errorf("failed to create answer: %w", err)
	}

	// Create channel that is blocked until ICE Gathering is complete
	gatherComplete := webrtc.GatheringCompletePromise(peerConnection)

	// Sets the LocalDescription, and sta
	if err = peerConnection.SetLocalDescription(answer); err != nil {
		return err
	}

	log.Debug("Waiting ICE gathering")
	<-gatherComplete

	log.Debug("Responding local descritption")
	localSDP := peerConnection.LocalDescription().SDP
	if err := d.RespondSDP([]byte(localSDP)); err != nil {
		return err
	}

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

	// log.Debug("Waiting for ICE connected")
	// select {
	// case <-ctx.Done():
	// 	return fmt.Errorf("waiting ICE connected failed: %w", ctx.Err())
	// case <-iceConnectedCtx.Done():
	// }

	log.Debug("Waiting for peer connection connected")
	select {
	case <-ctx.Done():
		return fmt.Errorf("waiting peer connection connected failed: %w", ctx.Err())
	case <-peerConnectedCtx.Done():
	}

	logICECandidatePairs(log, rtpSender)

	writer := &WebrtcTrackRTPWriter{
		track:  writeAudioTrack,
		sender: rtpSender,
	}
	rtpWriter := media.NewRTPPacketWriter(writer, codec)

	// Reading from webrtc answer or remoteSD is not possible for bellow. So we are using our fast parser
	// TODO: Find faster way of reading this information
	answersdp := sdp.SessionDescription{}
	sdp.Unmarshal([]byte(answer.SDP), &answersdp)
	remotesdp := sdp.SessionDescription{}
	sdp.Unmarshal([]byte(sdpBody), &remotesdp)

	m.RTPPacketReader = rtpReader
	m.RTPPacketWriter = rtpWriter
	m.mediaSession.Laddr = sdReadAddress(answersdp)
	m.mediaSession.Raddr = sdReadAddress(remotesdp)
	m.mediaSession.writer = writer
	m.peerConnection = peerConnection

	return nil
}

func sdReadAddress(sd sdp.SessionDescription) string {
	ci, _ := sd.ConnectionInformation()
	if ci.IP == nil {
		return ""
	}

	return ci.IP.String()
}
