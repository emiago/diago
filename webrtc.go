// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic
package diago

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"slices"

	"github.com/emiago/diago/media"
	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/packetdump"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	webrtcsdp "github.com/pion/sdp/v3"
	"github.com/pion/webrtc/v3"
)

// For debug
// PIONS_LOG_INFO=all

// That will do
// trace: ice
// debug: pc dtls
// info: everything else!

// Prepare the configuration
var defaultWebrtcConfig = webrtc.Configuration{
	ICEServers: []webrtc.ICEServer{
		{
			URLs: []string{"stun:stun.l.google.com:19302"},
		},
	},

	ICETransportPolicy: webrtc.ICETransportPolicyAll,
	// ICETransportPolicy: webrtc.ICETransportPolicyRelay,
	BundlePolicy:         webrtc.BundlePolicyMaxBundle,
	SDPSemantics:         webrtc.SDPSemanticsUnifiedPlanWithFallback,
	ICECandidatePoolSize: 5,
}

var (
	defaultWebrtcAPI *webrtc.API
)

func init() {
	webrtcInit([]net.IP{})
}

func SetWebrtcAPI(a *webrtc.API) {
	defaultWebrtcAPI = a
}

func webrtcInit(iceIPs []net.IP) error {
	var err error
	defaultWebrtcAPI, err = NewWebrtcAPI(iceIPs)
	return err
}

func NewWebrtcAPI(iceIPs []net.IP) (*webrtc.API, error) {
	api := webrtc.NewAPI()
	return api, func() error {
		var webrtcMedia = webrtc.MediaEngine{}
		if err := webrtcRegisterCodecs(&webrtcMedia); err != nil {
			return err
		}
		settEng := webrtc.SettingEngine{}
		// We want UDP
		settEng.DisableActiveTCP(true)
		// We do not need to deal with DTLS
		settEng.DisableCertificateFingerprintVerification(true)

		// Use local if only provided as ICEIP
		hasLocal := slices.ContainsFunc(iceIPs, func(ip net.IP) bool { return ip.IsLoopback() })
		settEng.SetIncludeLoopbackCandidate(hasLocal)
		settEng.SetIPFilter(func(i net.IP) bool {
			if iceIPs == nil {
				return true
			}
			// We should use only Transport IP
			listensOnIp := true
			for _, lip := range iceIPs {
				if lip.IsUnspecified() {
					// Use all if IP is unspecified
					return true
				}

				isIpv6 := lip.To4() == nil
				isIpv6Int := i.To4() == nil
				if !(lip.Equal(i) && isIpv6 == isIpv6Int) {
					listensOnIp = false
				}
			}
			return listensOnIp
		})

		rtpDebug := os.Getenv("RTP_DEBUG") == "true"
		rtcpDebug := os.Getenv("RTCP_DEBUG") == "true"

		// // // Create an InterceptorRegistry
		i := &interceptor.Registry{}
		if rtpDebug || rtcpDebug {
			si, _ := packetdump.NewSenderInterceptor(
				packetdump.PacketLog(
					&packetLogger{
						rtpDebug:  rtpDebug,
						rtcpDebug: rtcpDebug,
						direction: "WebRTC Sent",
					},
				),
				// packetdump.RTPFilter(func(pkt *rtp.Packet) bool {
				// 	if rtpDebug {
				// 		fmt.Fprintf(os.Stderr, "=== Sent RTP ===\n%s\n", pkt.String())
				// 	}

				// 	return true
				// }),
				// packetdump.RTCPPerPacketFilter(func(pkt rtcp.Packet) bool {
				// 	if rtcpDebug {
				// 		fmt.Fprintf(os.Stderr, "=== Sent RTCP ===\n%s\n", media.StringRTCP(pkt))
				// 	}
				// 	return true
				// }),
			)
			ri, _ := packetdump.NewReceiverInterceptor(
				packetdump.PacketLog(
					&packetLogger{
						rtpDebug:  rtpDebug,
						rtcpDebug: rtcpDebug,
						direction: "WebRTC Recv",
					},
				),
				// packetdump.RTPFilter(func(pkt *rtp.Packet) bool {
				// 	if rtpDebug {
				// 		fmt.Fprintf(os.Stderr, "=== Recv RTP ===\n%s\n", pkt.String())
				// 	}
				// 	return true
				// }),
				// packetdump.RTCPPerPacketFilter(func(pkt rtcp.Packet) bool {
				// 	if rtcpDebug {
				// 		fmt.Fprintf(os.Stderr, "=== Recv RTCP ===\n%s\n", media.StringRTCP(pkt))
				// 	}
				// 	return true
				// }),
			)
			i.Add(si)
			i.Add(ri)
		}

		// // Register default interceptors
		if err := webrtc.RegisterDefaultInterceptors(&webrtcMedia, i); err != nil {
			return err
		}
		settEng.DisableActiveTCP(true)
		// settEng.SetICETimeouts()
		// settEng.DisableSRTPReplayProtection(true)
		// settEng.DisableSRTCPReplayProtection(true)
		// settEng.SetRelayAcceptanceMinWait(1 * time.Millisecond)
		// settEng.SetSrflxAcceptanceMinWait(1 * time.Millisecond)
		// settEng.SetPrflxAcceptanceMinWait(1 * time.Millisecond)

		// // defaultSrflxAcceptanceMinWait is the wait time before nominating a srflx candidate
		// defaultSrflxAcceptanceMinWait = 500 * time.Millisecond

		// // defaultPrflxAcceptanceMinWait is the wait time before nominating a prflx candidate
		// defaultPrflxAcceptanceMinWait = 1000 * time.Millisecond

		// // defaultRelayAcceptanceMinWait is the wait time before nominating a relay candidate
		// defaultRelayAcceptanceMinWait = 2000 * time.Millisecond

		api = webrtc.NewAPI(
			webrtc.WithMediaEngine(&webrtcMedia),
			webrtc.WithSettingEngine(settEng),
			webrtc.WithInterceptorRegistry(i),
		)
		return nil
	}()
}

type packetLogger struct {
	rtpDebug  bool
	rtcpDebug bool
	direction string
}

func (l *packetLogger) LogRTPPacket(header *rtp.Header, payload []byte, attributes interceptor.Attributes) {
	if l.rtpDebug {
		pkt := rtp.Packet{
			Header:  *header,
			Payload: payload,
		}
		fmt.Fprintf(os.Stderr, "=== %s RTP ===\n%s\n", l.direction, pkt.String())
	}
}
func (l *packetLogger) LogRTCPPackets(pkts []rtcp.Packet, attributes interceptor.Attributes) {
	if l.rtcpDebug {
		for _, p := range pkts {
			fmt.Fprintf(os.Stderr, "=== %s RTP ===\n%s\n", l.direction, media.StringRTCP(p))
		}
	}
}

func logICECandidatePairs(log *slog.Logger, rtpSender *webrtc.RTPSender) {
	// Find out ICE pairs
	dt := rtpSender.Transport()
	if dt != nil {
		ices := dt.ICETransport()
		if ices != nil {
			pair, err := ices.GetSelectedCandidatePair()
			if err == nil && pair != nil {
				log.Debug("ICE Selected candidate pair",
					"localAddr", pair.Local.Address,
					"localPort", pair.Local.Port,
					"localType", pair.Local.Typ.String(),
					"remoteAddr", pair.Remote.Address,
					"remotePort", pair.Remote.Port,
					"remoteType", pair.Remote.Typ.String(),
				)
			}
		}
	}
}

type rtpNilReader struct {
	blockRead chan struct{}
}

func newRTPNilReader() *rtpNilReader {
	return &rtpNilReader{
		blockRead: make(chan struct{}),
	}
}

func (r *rtpNilReader) Close() {
	close(r.blockRead)
}

func (r *rtpNilReader) ReadRTP(buf []byte, p *rtp.Packet) (int, error) {
	<-r.blockRead
	return 0, io.EOF
}

type webrtcCodecs struct {
	localCodecs  []media.Codec
	remoteCodecs []media.Codec
}

func webrtcLoadCodecs(remoteSD *webrtcsdp.SessionDescription, filterCodecs []media.Codec, codecs *webrtcCodecs) error {
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
		return fmt.Errorf("No audio media description present")
	}

	remoteFormats := audioMediaDesc.MediaName.Formats
	remoteCodecs := make([]media.Codec, len(remoteFormats))
	n, err := media.CodecsFromSDPRead(audioMediaDesc.MediaName.Formats, attrs, remoteCodecs)
	if err != nil {
		return err
	}
	remoteCodecs = remoteCodecs[:n]

	// localFormats := make([]string, 0, len(opts.Formats))

	if filterCodecs != nil {
		localCodecs := make([]media.Codec, 0, len(filterCodecs))
		// Order local formats based on remote
		// log.Info(fmt.Sprintf("Comparing formats remote=%v local=%v", remoteCodecs, filterCodecs))
		for _, rf := range remoteCodecs {
			for _, lf := range filterCodecs {
				if lf == rf {
					localCodecs = append(localCodecs, lf)
				}
			}
		}
		if len(localCodecs) == 0 {
			return fmt.Errorf("remote has no local codecs support, remote=%v local=%v", remoteFormats, filterCodecs)
		}
		codecs.localCodecs = localCodecs
	}

	codecs.remoteCodecs = remoteCodecs
	return nil
}

func parseCodecMimeType(f uint8) (string, error) {
	switch f {
	case media.CodecAudioUlaw.PayloadType:
		return webrtc.MimeTypePCMU, nil
	case media.CodecAudioAlaw.PayloadType:
		return webrtc.MimeTypePCMA, nil
	default:
		return "", fmt.Errorf("no mime type for format=%q", f)
	}
}
