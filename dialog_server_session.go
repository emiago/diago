package diago

import (
	"context"
	"fmt"
	"net"
	"time"

	"github.com/emiago/media"
	"github.com/emiago/media/sdp"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/pion/rtcp"
	"github.com/pion/webrtc/v3"
	"github.com/rs/zerolog/log"
)

// DialogServerSession represents inbound channel
type DialogServerSession struct {
	*sipgo.DialogServerSession

	// MediaSession *media.MediaSession
	DialogMedia

	webrtPeer *webrtc.PeerConnection

	contactHDR sip.ContactHeader
	formats    sdp.Formats
}

func (d *DialogServerSession) Id() string {
	return d.ID
}

func (d *DialogServerSession) Close() {
	if d.MediaSession != nil {
		d.MediaSession.Close()
	}

	if d.webrtPeer != nil {
		d.webrtPeer.Close()
	}

	d.DialogServerSession.Close()
}

func (d *DialogServerSession) FromUser() string {
	return d.InviteRequest.From().Address.User
}

// User that was dialed
func (d *DialogServerSession) ToUser() string {
	return d.InviteRequest.To().Address.User
}

func (d *DialogServerSession) Progress() error {
	return d.Respond(sip.StatusTrying, "Trying", nil)
}

func (d *DialogServerSession) Ringing() error {
	return d.Respond(sip.StatusRinging, "Ringing", nil)
}

func (d *DialogServerSession) Respond(statusCode sip.StatusCode, reason string, body []byte, headers ...sip.Header) error {
	// TODO fix this on dialog srv
	headers = append(headers, &d.contactHDR)
	return d.DialogServerSession.Respond(statusCode, reason, body, headers...)
}

func (d *DialogServerSession) RespondSDP(body []byte) error {
	headers := []sip.Header{sip.NewHeader("Content-Type", "application/sdp")}
	headers = append(headers, &d.contactHDR)
	return d.DialogServerSession.Respond(200, "OK", body, headers...)
}

func (d *DialogServerSession) Answer() error {
	// TODO, lot of here settings need to come from TU. or TU must copy before shipping
	// We may have this settings
	// - Codecs
	// - RTP port ranges

	// For now we keep things global and hardcoded
	// Codecs are ulaw,alaw
	// RTP port range is not set

	// Now media SETUP
	// ip, port, err := sipgox.FindFreeInterfaceHostPort("udp", "")
	// if err != nil {
	// 	return err
	// }

	ip, _, err := sip.ResolveInterfacesIP("ip4", nil)
	if err != nil {
		return err
	}

	laddr := &net.UDPAddr{IP: ip, Port: 0}
	sess, err := media.NewMediaSession(laddr)
	sess.Formats = d.formats
	if err != nil {
		return err
	}

	rtpSess := media.NewRTPSession(sess)
	return d.AnswerWithMedia(rtpSess)
}

func (d *DialogServerSession) AnswerWithMedia(rtpSess *media.RTPSession) error {
	sess := rtpSess.Sess
	sdp := d.InviteRequest.Body()
	if sdp == nil {
		return fmt.Errorf("no sdp present in INVITE")
	}

	if err := sess.RemoteSDP(sdp); err != nil {
		return err
	}

	d.MediaSession = sess
	rtpSess.MonitorBackground() // Starts reading RTCP
	d.RTPReader = media.NewRTPReader(rtpSess)
	d.RTPWriter = media.NewRTPWriter(rtpSess)
	if err := d.RespondSDP(sess.LocalSDP()); err != nil {
		return err
	}

	// Wait ACK
	// If we do not wait ACK, hanguping call will fail as ACK can be delayed when we are doing Hangup
	for {
		select {
		case <-time.After(10 * time.Second):
			return fmt.Errorf("no ACK received")
		case state := <-d.State():
			if state == sip.DialogStateConfirmed {
				return nil
			}
		}
	}
}

func (d *DialogServerSession) Hangup(ctx context.Context) error {
	return d.Bye(ctx)
}

// For dialog bridge as proxy raw media. We need also to support raw media passing
func (d *DialogServerSession) AnswerWebrtc() error {

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
	remoteTrackCh := make(chan WebrtcTrackRTPReader)

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
		case remoteTrackCh <- wr:
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

	var ioReader WebrtcTrackRTPReader
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
	// case webrtc.MimeTypePCMA:
	// 	log.Info().Msg("Remote track PCMA")
	// 	d.RTPReader = media.NewRTPReaderCodec(ioReader, media.CodecAudioAlaw)
	default:
		log.Warn().Msgf("Media requested is not supported %s", med)
		return fmt.Errorf("Media remote track is not supported")
	}

	d.RTPReader = media.NewRTPReaderCodec(&ioReader, media.CodecAudioUlaw)
	d.RTPWriter = media.NewRTPWriterCodec(writer, codec)
	return nil
}
