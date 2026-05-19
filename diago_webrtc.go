package diago

import (
	"github.com/pion/webrtc/v3"
)

func webrtcRegisterCodecs(webrtcMedia *webrtc.MediaEngine) error {
	// webrtcMedia.RegisterDefaultCodecs()
	for _, codec := range []webrtc.RTPCodecParameters{
		// {
		// 	RTPCodecCapability: RTPCodecCapability{MimeTypeOpus, 48000, 2, "minptime=10;useinbandfec=1", nil},
		// 	PayloadType:        111,
		// },
		// {
		// 	RTPCodecCapability: RTPCodecCapability{MimeTypeG722, 8000, 0, "", nil},
		// 	PayloadType:        9,
		// },
		{
			RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMU, ClockRate: 8000, Channels: 1, SDPFmtpLine: "", RTCPFeedback: nil},
			PayloadType:        0,
		},
		{
			RTPCodecCapability: webrtc.RTPCodecCapability{MimeType: webrtc.MimeTypePCMA, ClockRate: 8000, Channels: 1, SDPFmtpLine: "", RTCPFeedback: nil},
			PayloadType:        8,
		},
	} {
		if err := webrtcMedia.RegisterCodec(codec, webrtc.RTPCodecTypeAudio); err != nil {
			return err
		}
	}
	return nil

}
