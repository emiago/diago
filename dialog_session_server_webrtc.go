package diago

import (
	"context"
	"fmt"
	"time"

	"github.com/emiago/media"
	"github.com/emiago/media/sdp"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"
	"github.com/rs/zerolog/log"
)

// Prepare the configuration
var webrtcConfig = webrtc.Configuration{
	ICEServers: []webrtc.ICEServer{
		{
			URLs: []string{"stun:stun.l.google.com:19302"},
		},
	},

	// ICETransportPolicy: webrtc.ICETransportPolicyRelay,
	// BundlePolicy: webrtc.BundlePolicyBalanced,
}

// Create a new MediaEngine instance

//   if err := m.RegisterDefaultCodecs(); err != nil {
// 	  panic(err)
//   }

// // Create a new API instance with the MediaEngine
// api := webrtc.NewAPI(webrtc.WithMediaEngine(&m))
var webrtcAPI *webrtc.API

func init() {
	var webrtcMedia = webrtc.MediaEngine{}
	if err := webrtcMedia.RegisterCodec(webrtc.RTPCodecParameters{
		RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMU, ClockRate: 8000, Channels: 0, SDPFmtpLine: "", RTCPFeedback: nil},
		PayloadType:        0,
	}, webrtc.RTPCodecTypeAudio); err != nil {
		panic(err)
	}

	settEng := webrtc.SettingEngine{}
	// settEng.SetNAT1To1IPs([]string{
	// 	"127.0.0.1",
	// 	"192.168.100.3",
	// },
	// 	webrtc.ICECandidateTypeHost,
	// )

	webrtcAPI = webrtc.NewAPI(
		webrtc.WithMediaEngine(&webrtcMedia),
		webrtc.WithSettingEngine(settEng),
	)

}

type WebrtcTrackRTPReader struct {
	track    *webrtc.TrackRemote
	receiver *webrtc.RTPReceiver
}

func (r *WebrtcTrackRTPReader) ReadRTP(buf []byte, p *rtp.Packet) error {
	n, _, err := r.track.Read(buf)
	if err != nil {
		return err
	}

	err = p.Unmarshal(buf[:n])
	if media.RTPDebug {
		log.Debug().Msgf("Sent RTP\n%s", p.String())
	}
	return err
}

func (r *WebrtcTrackRTPReader) ReadRTPRaw(buf []byte) (int, error) {
	n, _, err := r.track.Read(buf)
	if media.RTPDebug {
		log.Debug().Msgf("READ RTP Raw len=%d\n", n)
	}
	return n, err
}

func (r *WebrtcTrackRTPReader) ReadRTCP(buf []byte, rtcpBuf []rtcp.Packet) (int, error) {
	n, _, rtcpErr := r.receiver.Read(buf)
	if rtcpErr != nil {
		return n, rtcpErr
	}

	return media.RTCPUnmarshal(buf[:n], rtcpBuf)
}

func (r *WebrtcTrackRTPReader) ReadRTCPRaw(buf []byte) (int, error) {
	n, _, rtcpErr := r.receiver.Read(buf)
	return n, rtcpErr
}

type WebrtcTrackRTPWriter struct {
	track  *webrtc.TrackLocalStaticRTP
	sender *webrtc.RTPSender
}

func (r *WebrtcTrackRTPWriter) WriteRTP(p *rtp.Packet) error {
	if media.RTPDebug {
		log.Debug().Msgf("Sent RTP\n%s", p.String())
	}
	return r.track.WriteRTP(p)
}

func (r *WebrtcTrackRTPWriter) WriteRTPRaw(buf []byte) (int, error) {
	if media.RTPDebug {
		log.Debug().Msgf("Recv RTP Raw len=%d\n", len(buf))
	}
	return r.track.Write(buf)
}

func (r *WebrtcTrackRTPWriter) WriteRTCP(p rtcp.Packet) error {
	// By default pion does RTCP sending by default
	return nil
}

func (r *WebrtcTrackRTPWriter) WriteRTCPRaw(buf []byte) (int, error) {
	// By default pion does RTCP sending by default
	return 0, nil
}

// For dialog bridge as proxy raw media. We need also to support raw media passing
func (d *DialogServerSession) AnswerWebrtc() error {
	// TODO Check diago media conf
	mimeTypes := []string{
		// sdp.FORMAT_TYPE_ALAW,
		sdp.FORMAT_TYPE_ULAW,
	}
	return d.answerWebrtc(mimeTypes)
}

func (d *DialogServerSession) answerWebrtc(formats sdp.Formats) error {
	// Convert mime types to formats
	mimeTypes := make([]string, 0, len(formats))
	for _, f := range formats {
		var mt string
		switch f {
		case sdp.FORMAT_TYPE_ALAW:
			mt = webrtc.MimeTypePCMA
		case sdp.FORMAT_TYPE_ULAW:
			mt = webrtc.MimeTypePCMU
		default:
			return fmt.Errorf("unsuported mime type for webrtc %q", f)
		}
		mimeTypes = append(mimeTypes, mt)
	}

	// Create a new RTCPeerConnection
	peerConnection, err := webrtcAPI.NewPeerConnection(webrtcConfig)
	if err != nil {
		return err
	}

	// Keep reference for closing
	d.webrtPeer = peerConnection

	// Set the handler for ICE connection state
	// This will notify you when the peer has connected/disconnected
	peerConnection.OnICEConnectionStateChange(func(connectionState webrtc.ICEConnectionState) {
		fmt.Printf("Connection State has changed %s \n", connectionState.String())

		if connectionState == webrtc.ICEConnectionStateFailed {
			if closeErr := peerConnection.Close(); closeErr != nil {
				fmt.Println("ICE close", err)
			}
		}
	})

	// Allow us to receive 1 video track
	// if _, err = peerConnection.AddTransceiverFromKind(webrtc.RTPCodecTypeAudio); err != nil {
	// 	fmt.Println("Add transceiver", err)
	// }
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	remoteTrackCh := make(chan *WebrtcTrackRTPReader)

	// Set a handler for when a new remote track starts, this just distributes all our packets
	// to connected peers
	peerConnection.OnTrack(func(remoteTrack *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) { //nolint: revive
		// Create a local track, all our SFU clients will be fed via this track
		// remoteTrack.ReadRTP()
		// localTrack, newTrackErr := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMU}, "audio", "pion")
		// if newTrackErr != nil {
		// 	panic(newTrackErr)
		// }
		wr := WebrtcTrackRTPReader{
			track:    remoteTrack,
			receiver: receiver,
		}

		select {
		case remoteTrackCh <- &wr:
		case <-ctx.Done():
			log.Info().Msg("call finished before receiving remote track")
			return
		}
		log.Info().Msg("Webrtc remote RTCP loop started")
		// defer log.Info().Msg("Webrtc remote RTCP loop stopped")
		// go func() {
		// 	rtcpBuf := make([]byte, 1500)
		// 	for {
		// 		n, _, rtcpErr := receiver.Read(rtcpBuf)
		// 		if rtcpErr != nil {
		// 			return
		// 		}
		// 		pkts, err := rtcp.Unmarshal(rtcpBuf[:n])
		// 		if err != nil {
		// 			fmt.Println("Failed to unmarshal RTCP", err)
		// 		}

		// 		for _, p := range pkts {
		// 			fmt.Println("RTCP RECV", p.(fmt.Stringer).String())
		// 		}
		// 	}
		// }()

		// rtpBuf := make([]byte, 1500)
		// for {
		// 	i, _, readErr := remoteTrack.Read(rtpBuf)
		// 	if readErr != nil {
		// 		fmt.Println("Reade remote track err", err)
		// 		return
		// 	}

		// 	pkt := rtp.Packet{}
		// 	pkt.Unmarshal(rtpBuf[:i])

		// 	// fmt.Println("RTP RECV", pkt.String())

		// 	// ErrClosedPipe means we don't have any subscribers, this is ok if no peers have connected yet
		// 	// if _, err = localTrack.Write(rtpBuf[:i]); err != nil && !errors.Is(err, io.ErrClosedPipe) {
		// 	// 	panic(err)
		// 	// }
		// }
	})

	// audioTrack := <-localTrackChan
	// Create a audio track
	audioTrack, err := webrtc.NewTrackLocalStaticRTP(webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMU}, "audio", "pion")
	if err != nil {
		return err
	}

	rtpSender, err := peerConnection.AddTrack(audioTrack)
	if err != nil {
		return err
	}

	// Set the remote SessionDescription
	sd := webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  string(d.InviteRequest.Body()),
	}
	if err = peerConnection.SetRemoteDescription(sd); err != nil {
		return err
	}

	// audioTrack := <-localTrackChan
	answer, err := peerConnection.CreateAnswer(nil)
	if err != nil {
		return err
	}

	// Create channel that is blocked until ICE Gathering is complete
	gatherComplete := webrtc.GatheringCompletePromise(peerConnection)

	// Sets the LocalDescription, and sta
	if err = peerConnection.SetLocalDescription(answer); err != nil {
		return err
	}

	<-gatherComplete

	if err := d.RespondSDP([]byte(answer.SDP)); err != nil {
		return err
	}
	// ip, _, err := sip.ResolveInterfacesIP("ip4", nil)
	// if err != nil {
	// 	return nil, err
	// }

	// Read incoming RTCP packets
	// Before these packets are returned they are processed by interceptors. For things
	// like NACK this needs to be called.
	go func() {
		log.Info().Msg("Webrtc local RTCP loop started")
		defer log.Info().Msg("Webrtc local RTCP loop stopped")
		rtcpBuf := make([]byte, 1500)
		for {
			n, _, rtcpErr := rtpSender.Read(rtcpBuf)
			if rtcpErr != nil {
				return
			}
			pkts, err := rtcp.Unmarshal(rtcpBuf[:n])
			if err != nil {
				fmt.Println("Failed to unmarshal RTCP", err)
			}

			for _, p := range pkts {
				fmt.Println("RTCP LOCAL SEND", p.(fmt.Stringer).String())
			}
		}
	}()

	codec := media.CodecAudioUlaw
	writer := &WebrtcTrackRTPWriter{
		track:  audioTrack,
		sender: rtpSender,
	}

	var ioReader *WebrtcTrackRTPReader
	select {
	case <-ctx.Done():
		return fmt.Errorf("waiting remote failed: %w", ctx.Err())
	case ioReader = <-remoteTrackCh:
	}

	switch med := ioReader.track.Codec().MimeType; med {
	// case webrtc.MimeTypeOpus:
	// 	log.Warn().Msg("Opus is requested but we have no support yet")
	case webrtc.MimeTypePCMU:
		log.Info().Msg("Remote track PCMU")
		d.RTPPacketReader = media.NewRTPPacketReader(ioReader, media.CodecAudioUlaw)
	case webrtc.MimeTypePCMA:
		log.Info().Msg("Remote track PCMA")
		d.RTPPacketReader = media.NewRTPPacketReader(ioReader, media.CodecAudioAlaw)
	// 	d.RTPReader = media.NewRTPReaderCodec(ioReader, media.CodecAudioAlaw)
	default:
		log.Warn().Msgf("Media requested is not supported %s", med)
		return fmt.Errorf("Media remote track is not supported")
	}

	// For compatibility reasons we are creating media session, to be able to read things like SDP formats
	d.MediaSession = &media.MediaSession{
		Formats: sdp.NewFormats(sdp.FORMAT_TYPE_ULAW),
	}
	// d.RTPPacketReader = media.NewRTPPacketReader(&ioReader, media.CodecAudioUlaw)
	d.RTPPacketWriter = media.NewRTPPacketWriter(writer, codec)
	return nil
}
