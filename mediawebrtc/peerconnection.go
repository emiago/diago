package mediawebrtc

import (
	"context"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/emiago/diago/media"
	"github.com/emiago/diago/media/sdp"
	"github.com/google/uuid"
	webrtcsdp "github.com/pion/sdp/v3"
	"github.com/pion/webrtc/v3"
)

type MediaSession struct {
	// Set Initially and read only
	Laddr  string
	Raddr  string
	Codecs []media.Codec
	Mode   string

	codec        media.Codec
	filterCodecs []media.Codec

	peerConnection *webrtc.PeerConnection
	peerConnected  context.Context

	writer *RTPWriterTrack
	reader *RTPReaderTrack

	RTPPacketReader *media.RTPPacketReader
	RTPPacketWriter *media.RTPPacketWriter

	mu sync.Mutex
}

func (m *MediaSession) Init(webrtcConfig webrtc.Configuration) error {
	api := defaultWebrtcAPI

	// Create a new RTCPeerConnection
	if len(webrtcConfig.ICEServers) == 0 {
		webrtcConfig = defaultWebrtcConfig
	}

	peerConnection, err := api.NewPeerConnection(webrtcConfig)
	if err != nil {
		return err
	}

	log := DefaultLogger()
	peerConnectedCtx, peerConnectedCancel := context.WithCancel(context.TODO())
	peerConnection.OnConnectionStateChange(func(connectionState webrtc.PeerConnectionState) {
		log.Debug("Peer Connection State has changed", "state", connectionState.String())
		if connectionState == webrtc.PeerConnectionStateConnected {
			peerConnectedCancel()
		}
	})

	// Set the handler for ICE connection state
	// This will notify you when the peer has connected/disconnected
	// iceConnectedCtx, iceConnectedCancel := context.WithCancel(context.TODO())
	peerConnection.OnICEConnectionStateChange(func(connectionState webrtc.ICEConnectionState) {
		log.Debug(fmt.Sprintf("Connection State has changed %s \n", connectionState.String()))

		if connectionState == webrtc.ICEConnectionStateFailed {
			if closeErr := peerConnection.Close(); closeErr != nil {
				log.Debug("ICE close", "error", closeErr)
			}
		}

		// if connectionState == webrtc.ICEConnectionStateConnected || connectionState == webrtc.ICEConnectionStateCompleted {
		// 	iceConnectedCancel()
		// }
	})

	m.peerConnection = peerConnection
	m.peerConnected = peerConnectedCtx
	return nil
}

func (m *MediaSession) Fork() *MediaSession {
	return &MediaSession{
		Codecs:         slices.Clone(m.Codecs),
		Mode:           m.Mode,
		Laddr:          m.Laddr,
		Raddr:          m.Raddr,
		filterCodecs:   slices.Clone(m.filterCodecs),
		peerConnection: m.peerConnection,
	}
}

func (m *MediaSession) Close() error {
	if m.peerConnection != nil {
		return m.peerConnection.Close()
	}
	return nil
}

func (m *MediaSession) Codec() media.Codec {
	if m.codec.SampleRate == 0 && len(m.Codecs) > 0 {
		return m.Codecs[0]
	}
	return m.codec
}

func (m *MediaSession) RTPReaderReady() bool {
	return m.reader != nil
}

// CommonCodecs returns common codecs if negotiation is finished, that is Local and Remote SDP are exchanged.
// NOTE: Not thread safe, should be called after negotiation only.
func (m *MediaSession) CommonCodecs() []media.Codec {
	return m.filterCodecs
}

func (s *MediaSession) StopRTP(rw int8, dur time.Duration) error {
	t := time.Now().Add(dur)
	if rw&1 > 0 {
		if s.reader == nil || s.reader.Receiver == nil {
			return fmt.Errorf("rtp reader is not initialized")
		}
		return s.reader.Receiver.SetReadDeadline(t)
	}
	if rw&2 > 0 {
		// if dur == 0 {
		// 	return s.writer.sender.Stop()
		// }
		return fmt.Errorf("no support for duration based RTP write stop")
	}

	if s.reader == nil || s.reader.Receiver == nil {
		return fmt.Errorf("rtp reader is not initialized")
	}
	e1 := s.reader.Receiver.SetReadDeadline(t)
	// e2 := s.writer.sender.Stop()
	return e1
}
func (s *MediaSession) StartRTP(rw int8) error {
	if rw&1 > 0 {
		if s.reader == nil || s.reader.Receiver == nil {
			return fmt.Errorf("rtp reader is not initialized")
		}
		return s.reader.Receiver.SetReadDeadline(time.Time{})
	}
	if rw&2 > 0 {
		return fmt.Errorf("no support to restart writer")
	}
	if s.reader == nil || s.reader.Receiver == nil {
		return fmt.Errorf("rtp reader is not initialized")
	}
	return s.reader.Receiver.SetReadDeadline(time.Time{})
}

func (m *MediaSession) RemoteSDP(ctx context.Context, sdpBody []byte, offered bool) error {
	log := DefaultLogger().With("peer_connection", uuid.New().String())
	peerConnection := m.peerConnection
	if offered {
		sda := webrtc.SessionDescription{
			Type: webrtc.SDPTypeAnswer,
			SDP:  string(sdpBody),
		}

		if err := peerConnection.SetRemoteDescription(sda); err != nil {
			return err
		}
		remoteCodecs, err := audioCodecsFromWebrtcSDP(peerConnection.RemoteDescription())
		if err != nil {
			return err
		}
		localCodecs := commonCodecs(remoteCodecs, m.Codecs)
		if len(localCodecs) == 0 {
			return fmt.Errorf("remote has no local codecs support, remote=%v local=%v", remoteCodecs, m.Codecs)
		}
		m.Codecs = slices.Clone(localCodecs)
		m.filterCodecs = slices.Clone(localCodecs)
		if err := m.setActiveCodec(localCodecs[0]); err != nil {
			return err
		}
		return nil
	}

	// Check remote codecs support, and return order in that way with our support
	// Set the remote SessionDescription
	sd := webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  string(sdpBody),
	}
	if err := peerConnection.SetRemoteDescription(sd); err != nil {
		return fmt.Errorf("failed to set remote description: %w", err)
	}

	remoteCodecs, err := audioCodecsFromWebrtcSDP(peerConnection.RemoteDescription())
	if err != nil {
		return err
	}

	localCodecs := commonCodecs(remoteCodecs, m.Codecs)
	log.Debug(fmt.Sprintf("Comparing formats remote=%v local=%v", remoteCodecs, m.Codecs))
	if len(localCodecs) == 0 {
		return fmt.Errorf("remote has no local codecs support, remote=%v local=%v", remoteCodecs, m.Codecs)
	}

	// Now use first to write
	codec := localCodecs[0]
	// Create media session so that codecs are used correctly by diago
	log.Info("Answer media session setup", "codec", codec.String())
	m.filterCodecs = slices.Clone(localCodecs)
	m.Codecs = localCodecs

	if err := m.setupTracks(log); err != nil {
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
	answerCodecs, err := audioCodecsFromWebrtcSDP(peerConnection.LocalDescription())
	if err != nil {
		return err
	}
	answerCommonCodecs := commonCodecs(answerCodecs, localCodecs)
	if len(answerCommonCodecs) == 0 {
		return fmt.Errorf("answer has no local codecs support, answer=%v local=%v", answerCodecs, localCodecs)
	}
	m.Codecs = slices.Clone(answerCommonCodecs)
	m.filterCodecs = slices.Clone(answerCommonCodecs)
	if err := m.setActiveCodec(answerCommonCodecs[0]); err != nil {
		return err
	}

	log.Debug("Waiting ICE gathering")
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-gatherComplete:
	}

	// log.Debug("Waiting for ICE connected")
	// select {
	// case <-ctx.Done():
	// 	return fmt.Errorf("waiting ICE connected failed: %w", ctx.Err())
	// case <-iceConnectedCtx.Done():
	// }

	// Reading from webrtc answer or remoteSD is not possible for bellow. So we are using our fast parser
	// TODO: Find faster way of reading this information
	answersdp := sdp.SessionDescription{}
	sdp.Unmarshal([]byte(answer.SDP), &answersdp)
	remotesdp := sdp.SessionDescription{}
	sdp.Unmarshal([]byte(sdpBody), &remotesdp)

	m.Laddr = sdReadAddress(answersdp)
	m.Raddr = sdReadAddress(remotesdp)
	return nil
}

func (m *MediaSession) Finalize(ctx context.Context) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-m.peerConnected.Done():
	}
	log := DefaultLogger().With("peer_connection", uuid.New().String())
	logICECandidatePairs(log, m.writer.sender)
	return nil
}

func (m *MediaSession) LocalSDP(ctx context.Context, answered bool) ([]byte, error) {
	log := DefaultLogger().With("peer_connection", uuid.New().String())

	if answered {
		peerConnection := m.peerConnection
		localSDP := peerConnection.LocalDescription().SDP
		return []byte(localSDP), nil
	}

	var localSDP []byte = nil
	return localSDP, func() error {
		peerConnection := m.peerConnection

		if err := m.setupTracks(log); err != nil {
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

		localSDP = []byte(peerConnection.LocalDescription().SDP)
		log.Info("Invite media session setup", "codec", m.codec.String())
		return nil
	}()
}

func (m *MediaSession) PeerConnection() *webrtc.PeerConnection {
	return m.peerConnection
}

func audioCodecsFromWebrtcSDP(sd *webrtc.SessionDescription) ([]media.Codec, error) {
	if sd == nil {
		return nil, fmt.Errorf("missing remote description")
	}

	remoteSD, err := sd.Unmarshal()
	if err != nil {
		return nil, fmt.Errorf("failed to parse remote description: %w", err)
	}

	if len(remoteSD.MediaDescriptions) == 0 {
		return nil, fmt.Errorf("no media descriptions found in SDP")
	}

	var audioMediaDesc *webrtcsdp.MediaDescription
	for _, md := range remoteSD.MediaDescriptions {
		if md.MediaName.Media == "audio" {
			audioMediaDesc = md
			break
		}
	}
	if audioMediaDesc == nil {
		return nil, fmt.Errorf("answer webrtc: no audio media description present")
	}

	attrs := []string{}
	for _, a := range remoteSD.Attributes {
		attrs = append(attrs, a.String())
	}
	for _, a := range audioMediaDesc.Attributes {
		attrs = append(attrs, a.String())
	}

	remoteCodecs := make([]media.Codec, len(audioMediaDesc.MediaName.Formats))
	n, err := media.CodecsFromSDPRead(audioMediaDesc.MediaName.Formats, attrs, remoteCodecs)
	if err != nil {
		return nil, err
	}
	return remoteCodecs[:n], nil
}

func commonCodecs(remoteCodecs, localCodecs []media.Codec) []media.Codec {
	filtered := make([]media.Codec, 0, len(remoteCodecs))
	for _, rc := range remoteCodecs {
		for _, lc := range localCodecs {
			if lc == rc {
				filtered = append(filtered, lc)
				break
			}
		}
	}
	return filtered
}

func (m *MediaSession) setActiveCodec(codec media.Codec) error {
	if m.codec == codec {
		return nil
	}

	codecMimeType, err := parseCodecMimeType(codec.PayloadType)
	if err != nil {
		return err
	}

	if m.writer != nil {
		writeAudioTrack, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: codecMimeType}, "audio", "diago")
		if err != nil {
			return err
		}
		if err := m.writer.ReplaceTrack(writeAudioTrack); err != nil {
			return err
		}
	}
	if m.RTPPacketWriter != nil {
		m.RTPPacketWriter.UpdateWriter(m.writer, codec)
	}
	m.codec = codec
	return nil
}

func (m *MediaSession) setupTracks(log *slog.Logger) error {
	peerConnection := m.peerConnection

	if len(m.Codecs) == 0 {
		return fmt.Errorf("webrtc setup tracks: no codecs configured")
	}
	codec := m.Codecs[0]
	codecMimeType, _ := parseCodecMimeType(codec.PayloadType)
	// if err != nil {
	// 	return err
	// }

	// mess := &media.MediaSession{
	// 	// Formats: localFormats,
	// 	Codecs: localCodecs,
	// }

	// Allow us to receive 1 video track
	// if _, err = peerConnection.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio); err != nil {
	// 	fmt.Println("Add transceiver", err)
	// }

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

	// We are using own packetizer to send or read rtp
	nilReader := newRTPNilReader()
	rtpReader := media.NewRTPPacketReader(nilReader, codec)
	log.Info("Setting rtp packet reader", "codec", codec)
	peerConnection.OnTrack(func(remoteTrack *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) { //nolint: revive
		ioReader := &RTPReaderTrack{
			Track:    remoteTrack,
			Receiver: receiver,
		}

		readMimeType := remoteTrack.Codec().MimeType
		log.Debug("Webrtc remote track started", "mime_type", readMimeType)
		if codecMimeType != remoteTrack.Codec().MimeType {
			log.Info("Read media codec type received is not expected", "mime_type", readMimeType)
		}

		rtpReader.UpdateReader(ioReader)
		m.mu.Lock()
		m.reader = ioReader
		m.mu.Unlock()
		nilReader.Close()
	})

	writeAudioTrack, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: codecMimeType}, "audio", "diago")
	if err != nil {
		return err
	}

	rtpSender, err := peerConnection.AddTrack(writeAudioTrack)
	if err != nil {
		return err
	}

	log.Info("Invite media session setup", "codec", codec.String())
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

	writer := &RTPWriterTrack{
		track:   writeAudioTrack,
		sender:  rtpSender,
		enabled: true,
	}
	m.writer = writer
	m.codec = codec
	m.RTPPacketReader = rtpReader
	m.RTPPacketWriter = media.NewRTPPacketWriter(writer, codec)
	return nil
}

func sdReadAddress(sd sdp.SessionDescription) string {
	ci, _ := sd.ConnectionInformation()
	if ci.IP == nil {
		return ""
	}

	return ci.IP.String()
}
