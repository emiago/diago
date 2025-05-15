// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diagomod

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/emiago/diago"
	"github.com/emiago/diago/media"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"
)

// For debug
// PIONS_LOG_INFO=all

// That will do
// trace: ice
// debug: pc dtls
// info: everything else!

// Prepare the configuration
var webrtcConfig = webrtc.Configuration{
	ICEServers: []webrtc.ICEServer{
		{
			URLs: []string{"stun:stun.l.google.com:19302"},
		},
	},

	ICETransportPolicy: webrtc.ICETransportPolicyAll,
	BundlePolicy:       webrtc.BundlePolicyMaxBundle,
	SDPSemantics:       webrtc.SDPSemanticsUnifiedPlanWithFallback,
}

// // Create a new API instance with the MediaEngine
// api := webrtc.NewAPI(webrtc.WithMediaEngine(&m))
// var webrtcAPI *webrtc.API

var DefaultDiagoWebrtc = NewDiagoWebrtc()

type DiagoWebrtc struct {
	*webrtc.API
	log *slog.Logger
}

func NewDiagoWebrtc() DiagoWebrtc {
	var webrtcMedia = webrtc.MediaEngine{}
	if err := webrtcRegisterCodecs(&webrtcMedia); err != nil {
		panic(err)
	}
	settEng := webrtc.SettingEngine{}
	// We want UDP
	settEng.DisableActiveTCP(true)
	// We do not need to deal with DTLS
	settEng.DisableCertificateFingerprintVerification(true)
	webrtcAPI := webrtc.NewAPI(
		webrtc.WithMediaEngine(&webrtcMedia),
		webrtc.WithSettingEngine(settEng),
	)
	d := DiagoWebrtc{
		API: webrtcAPI,
		log: slog.Default(),
	}
	return d
}

func webrtcRegisterCodecs(webrtcMedia *webrtc.MediaEngine) error {
	// webrtcMedia.RegisterDefaultCodecs()
	for _, codec := range []webrtc.RTPCodecParameters{
		{
			RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMU, ClockRate: 8000, Channels: 0, SDPFmtpLine: "", RTCPFeedback: nil},
			PayloadType:        0,
		},
		{
			RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMA, ClockRate: 8000, Channels: 0, SDPFmtpLine: "", RTCPFeedback: nil},
			PayloadType:        8,
		},
	} {
		if err := webrtcMedia.RegisterCodec(codec, webrtc.RTPCodecTypeAudio); err != nil {
			return err
		}
	}
	return nil

}

type WebrtcTrackRTPReader struct {
	track    *webrtc.TrackRemote
	receiver *webrtc.RTPReceiver
}

func (r *WebrtcTrackRTPReader) ReadRTP(buf []byte, p *rtp.Packet) (int, error) {
	n, _, err := r.track.Read(buf)
	if err != nil {
		return n, err
	}

	err = p.Unmarshal(buf[:n])
	if media.RTPDebug {
		slog.Debug(fmt.Sprintf("Sent RTP\n%s", p.String()))
	}
	return n, err
}

func (r *WebrtcTrackRTPReader) ReadRTPRaw(buf []byte) (int, error) {
	n, _, err := r.track.Read(buf)
	if media.RTPDebug {
		slog.Debug(fmt.Sprintf("READ RTP Raw len=%d\n", n))
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
		slog.Debug(fmt.Sprintf("Sent RTP\n%s", p.String()))
	}
	return r.track.WriteRTP(p)
}

func (r *WebrtcTrackRTPWriter) WriteRTPRaw(buf []byte) (int, error) {
	if media.RTPDebug {
		slog.Debug(fmt.Sprintf("Recv RTP Raw len=%d\n", len(buf)))
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

type AnswerWebrtcOptions struct {
	// Formats sdp.Formats
	Codecs []media.Codec
}

func AnswerWebrtc(d *diago.DialogServerSession, opts AnswerWebrtcOptions) error {
	return DefaultDiagoWebrtc.AnswerWebrtc(d, opts)
}

func (api *DiagoWebrtc) AnswerWebrtc(d *diago.DialogServerSession, opts AnswerWebrtcOptions) error {
	if opts.Codecs == nil {
		opts.Codecs = []media.Codec{
			media.CodecAudioAlaw,
			media.CodecAudioUlaw,
			// RTP telephone event not supported
		}
	}

	return api.answerWebrtc(d, opts)
}

func (api *DiagoWebrtc) answerWebrtc(d *diago.DialogServerSession, opts AnswerWebrtcOptions) error {
	// Create a new RTCPeerConnection
	log := api.log
	peerConnection, err := api.NewPeerConnection(webrtcConfig)
	if err != nil {
		return err
	}

	d.OnClose(func() error {
		return peerConnection.Close()
	})

	// Set the handler for ICE connection state
	// This will notify you when the peer has connected/disconnected
	iceConnectedCtx, iceConnectedCancel := context.WithCancel(context.TODO())
	peerConnection.OnICEConnectionStateChange(func(connectionState webrtc.ICEConnectionState) {
		log.Info(fmt.Sprintf("Connection State has changed %s \n", connectionState.String()))

		if connectionState == webrtc.ICEConnectionStateFailed {
			if closeErr := peerConnection.Close(); closeErr != nil {
				fmt.Println("ICE close", err)
			}
		}

		if connectionState == webrtc.ICEConnectionStateConnected {
			iceConnectedCancel()
		}
	})

	// Check remote codecs support, and return order in that way with our support
	// Set the remote SessionDescription
	sd := webrtc.SessionDescription{
		Type: webrtc.SDPTypeOffer,
		SDP:  string(d.InviteRequest.Body()),
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

	remoteFormats := remoteSD.MediaDescriptions[0].MediaName.Formats
	remoteCodecs := make([]media.Codec, len(remoteFormats))
	n, err := media.CodecsFromSDPRead(remoteSD.MediaDescriptions[0].MediaName.Formats, attrs, remoteCodecs)
	if err != nil {
		return err
	}
	remoteCodecs = remoteCodecs[:n]

	// localFormats := make([]string, 0, len(opts.Formats))
	localCodecs := make([]media.Codec, 0, len(opts.Codecs))
	// Order local formats based on remote
	log.Info(fmt.Sprintf("Comparing formats remote=%v local=%v", remoteCodecs, opts.Codecs))
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
	log.Info("Media session setup", "formats", localCodecs, "codec", codec.String())
	mess := &media.MediaSession{
		// Formats: localFormats,
		Codecs: localCodecs,
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	rtpReader := media.NewRTPPacketReader(&RTPNilReader{}, codec)
	peerConnection.OnTrack(func(remoteTrack *webrtc.TrackRemote, receiver *webrtc.RTPReceiver) { //nolint: revive
		ioReader := &WebrtcTrackRTPReader{
			track:    remoteTrack,
			receiver: receiver,
		}

		readMimeType := ioReader.track.Codec().MimeType
		log.Info("Webrtc remote track started", "mimeType", readMimeType)
		if codecMimeType != ioReader.track.Codec().MimeType {
			log.Info("Read media codec type received is not expected", "mimeType", readMimeType)
		}

		rtpReader.UpdateReader(ioReader)
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

	log.Info("Waiting for ICE connected")
	select {
	case <-ctx.Done():
		return fmt.Errorf("waiting ICE connected failed: %w", ctx.Err())
	case <-iceConnectedCtx.Done():
	}

	writer := &WebrtcTrackRTPWriter{
		track:  writeAudioTrack,
		sender: rtpSender,
	}

	rtpWriter := media.NewRTPPacketWriter(writer, codec)
	d.Media().InitMediaSession(mess, rtpReader, rtpWriter)
	return nil
}

type RTPNilReader struct {
	media.RTPReader
}

func (r *RTPNilReader) ReadRTP(buf []byte, p *rtp.Packet) (int, error) {
	time.Sleep(20 * time.Millisecond)
	return 0, nil
}
