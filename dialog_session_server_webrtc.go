package diago

import (
	"github.com/emiago/media"
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
