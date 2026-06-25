// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic
package mediawebrtc

import (
	"fmt"
	"net"
	"os"
	"reflect"
	"slices"

	"github.com/emiago/diago/media"
	"github.com/pion/ice/v2"
	"github.com/pion/interceptor"
	"github.com/pion/interceptor/pkg/packetdump"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	webrtcsdp "github.com/pion/sdp/v3"
	"github.com/pion/webrtc/v3"
)

// For debug:
// PIONS_LOG_INFO=all

var defaultWebrtcConfig = webrtc.Configuration{
	ICEServers: []webrtc.ICEServer{
		{
			URLs: []string{"stun:stun.l.google.com:19302"},
		},
	},
	ICETransportPolicy:   webrtc.ICETransportPolicyAll,
	BundlePolicy:         webrtc.BundlePolicyMaxBundle,
	SDPSemantics:         webrtc.SDPSemanticsUnifiedPlanWithFallback,
	ICECandidatePoolSize: 5,
}

var defaultWebrtcAPI *webrtc.API

type WebrtcAPIConfig struct {
	Config       webrtc.Configuration
	ICEIPs       []net.IP
	NetworkTypes []webrtc.NetworkType
	ICETCPMux    ice.TCPMux

	DisableActiveTCP                          *bool
	DisableCertificateFingerprintVerification *bool
	IncludeLoopbackCandidate                  *bool

	IPFilter       func(net.IP) bool
	RegisterCodecs func(*webrtc.MediaEngine) error
	Interceptors   func() (*interceptor.Registry, error)
}

func init() {
	var err error
	defaultWebrtcAPI, err = NewWebrtcAPI([]net.IP{})
	if err != nil {
		panic(err)
	}
}

func SetWebrtcAPI(a *webrtc.API) {
	defaultWebrtcAPI = a
}

func NewWebrtcAPI(iceIPs []net.IP) (*webrtc.API, error) {
	return NewWebrtcAPIFromConfig(WebrtcAPIConfig{ICEIPs: iceIPs})
}

func NewWebrtcAPIFromConfig(cfg WebrtcAPIConfig) (*webrtc.API, error) {
	if !reflect.ValueOf(cfg.Config).IsZero() {
		defaultWebrtcConfig = cfg.Config
	}

	var webrtcMedia = webrtc.MediaEngine{}
	registerCodecs := cfg.RegisterCodecs
	if registerCodecs == nil {
		registerCodecs = webrtcRegisterCodecs
	}
	if err := registerCodecs(&webrtcMedia); err != nil {
		return nil, err
	}

	settEng := webrtc.SettingEngine{}
	disableActiveTCP := true
	if cfg.DisableActiveTCP != nil {
		disableActiveTCP = *cfg.DisableActiveTCP
	}
	if disableActiveTCP {
		settEng.DisableActiveTCP(true)
	}
	if len(cfg.NetworkTypes) > 0 {
		settEng.SetNetworkTypes(cfg.NetworkTypes)
	}
	if cfg.ICETCPMux != nil {
		settEng.SetICETCPMux(cfg.ICETCPMux)
	}
	disableFPVerification := true
	if cfg.DisableCertificateFingerprintVerification != nil {
		disableFPVerification = *cfg.DisableCertificateFingerprintVerification
	}
	if disableFPVerification {
		settEng.DisableCertificateFingerprintVerification(true)
	}

	iceIPs := cfg.ICEIPs
	hasLocal := slices.ContainsFunc(iceIPs, func(ip net.IP) bool { return ip.IsLoopback() })
	if cfg.IncludeLoopbackCandidate != nil {
		hasLocal = *cfg.IncludeLoopbackCandidate
	}
	settEng.SetIncludeLoopbackCandidate(hasLocal)
	ipFilter := cfg.IPFilter
	if ipFilter == nil {
		ipFilter = func(i net.IP) bool {
			if iceIPs == nil {
				return true
			}
			listensOnIP := true
			for _, lip := range iceIPs {
				if lip.IsUnspecified() {
					return true
				}

				isIPv6 := lip.To4() == nil
				isIPv6Int := i.To4() == nil
				if !(lip.Equal(i) && isIPv6 == isIPv6Int) {
					listensOnIP = false
				}
			}
			return listensOnIP
		}
	}
	settEng.SetIPFilter(ipFilter)

	rtpDebug := os.Getenv("RTP_DEBUG") == "true"
	rtcpDebug := os.Getenv("RTCP_DEBUG") == "true"
	i := &interceptor.Registry{}
	if rtpDebug || rtcpDebug {
		si, _ := packetdump.NewSenderInterceptor(
			packetdump.PacketLog(&packetLogger{
				rtpDebug:  rtpDebug,
				rtcpDebug: rtcpDebug,
				direction: "WebRTC Sent",
			}),
		)
		ri, _ := packetdump.NewReceiverInterceptor(
			packetdump.PacketLog(&packetLogger{
				rtpDebug:  rtpDebug,
				rtcpDebug: rtcpDebug,
				direction: "WebRTC Recv",
			}),
		)
		i.Add(si)
		i.Add(ri)
	}

	if err := webrtc.RegisterDefaultInterceptors(&webrtcMedia, i); err != nil {
		return nil, err
	}

	api := webrtc.NewAPI(
		webrtc.WithMediaEngine(&webrtcMedia),
		webrtc.WithSettingEngine(settEng),
		webrtc.WithInterceptorRegistry(i),
	)
	return api, nil
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

	if filterCodecs != nil {
		localCodecs := make([]media.Codec, 0, len(filterCodecs))
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
		return "", fmt.Errorf("unknown codec payload type %d", f)
	}
}
