// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2026, Emir Aganovic

package media

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emiago/diago/media/sdp"
	"github.com/emiago/dtls/v3"
	"github.com/pion/ice/v4"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	webrtcsdp "github.com/pion/sdp/v3"
	"github.com/pion/srtp/v3"
	"github.com/pion/stun/v3"
)

// ErrWebRTCICERestart reports that a subsequent SDP changed both remote ICE
// credentials and therefore needs a replacement MediaSessionWebrtc transport.
var ErrWebRTCICERestart = errors.New("remote WebRTC SDP starts a new ICE generation")

// MediaSessionWebrtcConfig contains transport settings which do not belong to
// the codec-level media API. ICEURLs accepts STUN and TURN URLs understood by
// Pion (for example stun:stun.example.org:3478).
type MediaSessionWebrtcConfig struct {
	ICEURLs        []string
	IPFamilies     []ICEIPFamily
	CandidateTypes []ICECandidateType
	PortMin        uint16
	PortMax        uint16
	Timeouts       ICETimeouts

	IncludeLoopback bool
	InterfaceFilter func(interfaceName string) bool
	// IPFilter controls which local addresses may become ICE candidates.
	IPFilter func(ip netip.Addr) bool
	// RemoteIPFilter controls which addresses from remote SDP are accepted.
	RemoteIPFilter func(ip netip.Addr) bool

	// NetworkTypes is kept for source compatibility. Only UDP4 and UDP6 are
	// accepted. New code should use IPFamilies so configuration is not tied to
	// the current ICE implementation.
	//
	// Deprecated: use IPFamilies.
	NetworkTypes []ice.NetworkType

	DTLS DTLSConfig
}

// MediaSessionWebrtc is the direct ICE + DTLS-SRTP media stack. It intentionally
// does not embed MediaSession: legacy RTP uses two known UDP destinations,
// whereas WebRTC must send every DTLS/SRTP/SRTCP packet through the candidate
// pair selected and consent-checked by ICE.
//
// Signalling lifecycle:
//
//   - Init gathers local candidates (non-trickle ICE).
//   - LocalSDP(false) creates an offer, or RemoteSDP(..., false) consumes one.
//   - RemoteSDP starts ICE as controlling for a local offer and controlled for
//     a local answer. It returns before connectivity checks finish.
//   - Finalize waits for ICE, performs DTLS, verifies the SDP fingerprint and
//     derives the SRTP keys. RTP must not be used before Finalize succeeds.
type MediaSessionWebrtc struct {
	Laddr  string
	Raddr  string
	Codecs []Codec
	Mode   string

	Config MediaSessionWebrtcConfig

	mu                         sync.Mutex
	agent                      *ice.Agent
	localCandidates            []ice.Candidate
	localUfrag                 string
	localPwd                   string
	remoteUfrag                string
	remotePwd                  string
	remoteFingerprintAlgorithm string
	remoteFingerprint          string
	localSetup                 string
	modePreference             string
	codec                      Codec
	filterCodecs               []Codec
	iceConn                    *ice.Conn
	mux                        *webRTCPacketMux
	dtlsConn                   *dtls.Conn
	localCtxSRTP               *srtp.Context
	remoteCtxSRTP              *srtp.Context
	localCtxSRTCP              *srtp.Context
	remoteCtxSRTCP             *srtp.Context
	rtcpReducedSize            bool
	pendingCodecs              []Codec
	pendingMode                string
	pendingRTCPReducedSize     bool
	ready                      bool
	closed                     bool
	writeRTPBuf                []byte
	writeRTCPBuf               []byte
	onICEStateChange           func(ice.ConnectionState)
}

// Fork creates a fresh WebRTC transport for a locally initiated ICE restart.
// Codec and configuration slices are copied because SDP negotiation mutates the
// session's codec list, while certificates and filters remain immutable config.
func (m *MediaSessionWebrtc) Fork(ctx context.Context) (*MediaSessionWebrtc, error) {
	m.mu.Lock()
	conf := m.Config
	conf.ICEURLs = slices.Clone(conf.ICEURLs)
	conf.IPFamilies = slices.Clone(conf.IPFamilies)
	conf.CandidateTypes = slices.Clone(conf.CandidateTypes)
	conf.NetworkTypes = slices.Clone(conf.NetworkTypes)
	conf.DTLS.Certificates = slices.Clone(conf.DTLS.Certificates)
	conf.DTLS.SRTPProfiles = slices.Clone(conf.DTLS.SRTPProfiles)
	conf.DTLS.EllipticCurves = slices.Clone(conf.DTLS.EllipticCurves)
	codecs := slices.Clone(m.Codecs)
	mode := m.Mode
	modePreference := m.modePreference
	onICEStateChange := m.onICEStateChange
	m.mu.Unlock()

	fork := &MediaSessionWebrtc{Codecs: codecs, Mode: mode, modePreference: modePreference}
	if err := fork.Init(ctx, conf, onICEStateChange); err != nil {
		return nil, err
	}
	return fork, nil
}

// Init creates the ICE agent and waits for candidate gathering to finish. SIP
// offer/answer has no standard trickle-ICE exchange, so the SDP returned later
// contains a complete candidate set and a=end-of-candidates.
func (m *MediaSessionWebrtc) Init(
	ctx context.Context,
	conf MediaSessionWebrtcConfig,
	onICEStateChange ...func(ice.ConnectionState),
) error {
	if len(onICEStateChange) > 1 {
		return fmt.Errorf("only one ICE state handler can be configured")
	}
	m.mu.Lock()
	if m.agent != nil {
		m.mu.Unlock()
		return fmt.Errorf("webrtc media session is already initialized")
	}
	m.Config = conf
	if len(onICEStateChange) == 1 {
		m.onICEStateChange = onICEStateChange[0]
	}
	m.mu.Unlock()

	if len(conf.DTLS.Certificates) == 0 {
		return fmt.Errorf("webrtc media session requires a DTLS certificate")
	}
	if len(m.Codecs) == 0 {
		return fmt.Errorf("webrtc media session requires at least one codec")
	}

	networkTypes, err := webRTCNetworkTypes(conf)
	if err != nil {
		return &ICEError{Phase: ICEPhaseGathering, Err: err}
	}
	candidateTypes, err := webRTCCandidateTypes(conf)
	if err != nil {
		return &ICEError{Phase: ICEPhaseGathering, Err: err}
	}
	timeouts, err := normalizeICETimeouts(conf.Timeouts)
	if err != nil {
		return &ICEError{Phase: ICEPhaseGathering, Err: err}
	}
	opts := []ice.AgentOption{
		ice.WithNetworkTypes(networkTypes),
		ice.WithCandidateTypes(candidateTypes),
		ice.WithDisconnectedTimeout(timeouts.Disconnected),
		ice.WithFailedTimeout(timeouts.Failed),
		ice.WithKeepaliveInterval(timeouts.Keepalive),
	}
	if conf.PortMin != 0 || conf.PortMax != 0 {
		if conf.PortMin == 0 || conf.PortMax < conf.PortMin {
			return &ICEError{
				Phase: ICEPhaseGathering,
				Err:   fmt.Errorf("invalid UDP port range %d-%d", conf.PortMin, conf.PortMax),
			}
		}
		opts = append(opts, ice.WithPortRange(conf.PortMin, conf.PortMax))
	}
	if conf.IncludeLoopback {
		opts = append(opts, ice.WithIncludeLoopback())
	}
	if conf.InterfaceFilter != nil {
		opts = append(opts, ice.WithInterfaceFilter(conf.InterfaceFilter))
	}
	if filter := webRTCIPFilter(conf.IPFilter); filter != nil {
		opts = append(opts, ice.WithIPFilter(filter))
	}
	if filter := webRTCIPFilter(conf.RemoteIPFilter); filter != nil {
		opts = append(opts, ice.WithRemoteIPFilter(filter))
	}
	if len(conf.ICEURLs) > 0 {
		urls := make([]*stun.URI, 0, len(conf.ICEURLs))
		for _, rawURL := range conf.ICEURLs {
			u, err := stun.ParseURI(rawURL)
			if err != nil {
				return &ICEError{
					Phase: ICEPhaseGathering,
					Err:   fmt.Errorf("parse URL %q: %w", rawURL, err),
				}
			}
			urls = append(urls, u)
		}
		opts = append(opts, ice.WithUrls(urls))
	}

	agent, err := ice.NewAgentWithOptions(opts...)
	if err != nil {
		return &ICEError{Phase: ICEPhaseGathering, Err: fmt.Errorf("create ICE agent: %w", err)}
	}
	if err = agent.OnConnectionStateChange(func(state ice.ConnectionState) {
		m.notifyICEStateChange(state)
	}); err != nil {
		_ = agent.Close()
		return &ICEError{Phase: ICEPhaseGathering, Err: fmt.Errorf("set ICE state handler: %w", err)}
	}
	gathered := make(chan struct{})
	var gatherOnce sync.Once
	if err = agent.OnCandidate(func(candidate ice.Candidate) {
		if candidate == nil {
			gatherOnce.Do(func() { close(gathered) })
			return
		}
		m.mu.Lock()
		m.localCandidates = append(m.localCandidates, candidate)
		m.mu.Unlock()
	}); err != nil {
		_ = agent.Close()
		return &ICEError{Phase: ICEPhaseGathering, Err: fmt.Errorf("set candidate handler: %w", err)}
	}
	ufrag, pwd, err := agent.GetLocalUserCredentials()
	if err != nil {
		_ = agent.Close()
		return &ICEError{Phase: ICEPhaseGathering, Err: fmt.Errorf("get local credentials: %w", err)}
	}
	m.mu.Lock()
	m.agent = agent
	m.localUfrag = ufrag
	m.localPwd = pwd
	m.mu.Unlock()
	m.notifyICEStateChange(ice.ConnectionStateNew)

	if err = agent.GatherCandidates(); err != nil {
		_ = m.Close()
		return &ICEError{Phase: ICEPhaseGathering, Err: fmt.Errorf("gather candidates: %w", err)}
	}
	select {
	case <-ctx.Done():
		_ = m.Close()
		return &ICEError{Phase: ICEPhaseGathering, Err: ctx.Err()}
	case <-gathered:
	}
	m.mu.Lock()
	count := len(m.localCandidates)
	m.mu.Unlock()
	if count == 0 {
		_ = m.Close()
		return &ICEError{Phase: ICEPhaseGathering, Err: fmt.Errorf("no local candidates matched the configured policy")}
	}
	return nil
}

func (m *MediaSessionWebrtc) notifyICEStateChange(state ice.ConnectionState) {
	m.mu.Lock()
	handler := m.onICEStateChange
	m.mu.Unlock()
	if handler != nil {
		handler(state)
	}
}

func (m *MediaSessionWebrtc) Codec() Codec {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.codec.SampleRate != 0 {
		return m.codec
	}
	codec, _ := CodecAudioFromList(m.Codecs)
	return codec
}

func (m *MediaSessionWebrtc) CommonCodecs() []Codec {
	m.mu.Lock()
	defer m.mu.Unlock()
	return slices.Clone(m.filterCodecs)
}

// LocalSDP returns a complete, non-trickle WebRTC SDP. answered controls the
// DTLS setup attribute: an offer is actpass; an answer uses the role selected
// while parsing the remote offer.
func (m *MediaSessionWebrtc) LocalSDP(_ context.Context, answered bool) ([]byte, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.agent == nil || len(m.localCandidates) == 0 {
		return nil, fmt.Errorf("webrtc media session is not initialized")
	}
	setup := "actpass"
	if answered {
		if m.localSetup == "" {
			return nil, fmt.Errorf("remote WebRTC offer must be parsed before creating an answer")
		}
		setup = m.localSetup
	} else {
		m.modePreference = m.Mode
	}
	codecs := m.Codecs
	if len(m.filterCodecs) > 0 {
		codecs = m.filterCodecs
	}
	mode := m.Mode
	rtcpReducedSize := m.rtcpReducedSize
	if answered && len(m.pendingCodecs) > 0 {
		codecs = m.pendingCodecs
		mode = m.pendingMode
		rtcpReducedSize = m.pendingRTCPReducedSize
	}
	return m.localSDPLocked(codecs, setup, !answered || rtcpReducedSize, mode)
}

func (m *MediaSessionWebrtc) localSDPLocked(codecs []Codec, setup string, includeRTCPReducedSize bool, mode string) ([]byte, error) {
	fingerprint, err := dtlsSHA256Fingerprint(m.Config.DTLS.Certificates[0])
	if err != nil {
		return nil, fmt.Errorf("DTLS certificate fingerprint: %w", err)
	}
	candidate := m.localCandidates[0]
	for _, c := range m.localCandidates[1:] {
		if c.Priority() > candidate.Priority() {
			candidate = c
		}
	}
	ip := net.ParseIP(candidate.Address())
	addressType := "IP4"
	connectionIP := "0.0.0.0"
	if ip != nil {
		connectionIP = ip.String()
		if ip.To4() == nil {
			addressType = "IP6"
		}
	}
	if mode == "" {
		mode = sdp.ModeSendrecv
	}

	formats := make([]string, 0, len(codecs))
	lines := []string{
		"v=0",
		fmt.Sprintf("o=diago %d %d IN %s %s", time.Now().UnixNano(), time.Now().UnixNano(), addressType, connectionIP),
		"s=diago",
		"t=0 0",
		"a=group:BUNDLE 0",
		"a=msid-semantic: WMS diago",
	}
	for _, codec := range codecs {
		formats = append(formats, strconv.Itoa(int(codec.PayloadType)))
	}
	lines = append(lines,
		fmt.Sprintf("m=audio %d UDP/TLS/RTP/SAVPF %s", candidate.Port(), strings.Join(formats, " ")),
		fmt.Sprintf("c=IN %s %s", addressType, connectionIP),
		"a=mid:0",
		"a=rtcp-mux",
	)
	if includeRTCPReducedSize {
		lines = append(lines, "a=rtcp-rsize")
	}
	lines = append(lines,
		"a="+mode,
		"a=ice-ufrag:"+m.localUfrag,
		"a=ice-pwd:"+m.localPwd,
		"a=fingerprint:sha-256 "+fingerprint,
		"a=setup:"+setup,
	)
	for _, codec := range codecs {
		channels := ""
		if codec.NumChannels > 1 {
			channels = "/" + strconv.Itoa(codec.NumChannels)
		}
		lines = append(lines, fmt.Sprintf("a=rtpmap:%d %s/%d%s", codec.PayloadType, codec.Name, codec.SampleRate, channels))
		if strings.EqualFold(codec.Name, "opus") {
			lines = append(lines, fmt.Sprintf("a=fmtp:%d useinbandfec=0", codec.PayloadType))
		} else if strings.EqualFold(codec.Name, "telephone-event") {
			lines = append(lines, fmt.Sprintf("a=fmtp:%d 0-16", codec.PayloadType))
		}
	}
	for _, c := range m.localCandidates {
		lines = append(lines, "a=candidate:"+c.Marshal())
	}
	lines = append(lines, "a=end-of-candidates", "a=msid:diago audio", "")
	return []byte(strings.Join(lines, "\r\n")), nil
}

// RemoteSDP parses an answer when offered is true, otherwise it parses an
// offer. Starting ICE here is non-blocking so a SIP answer can be sent before
// the browser is required to complete connectivity checks.
func (m *MediaSessionWebrtc) RemoteSDP(_ context.Context, body []byte, offered bool) error {
	var desc webrtcsdp.SessionDescription
	if err := desc.Unmarshal(body); err != nil {
		return fmt.Errorf("parse remote WebRTC SDP: %w", err)
	}
	md := findWebRTCAudio(&desc)
	if md == nil {
		return fmt.Errorf("remote WebRTC SDP has no audio media description")
	}
	proto := strings.Join(md.MediaName.Protos, "/")
	if !strings.EqualFold(proto, "UDP/TLS/RTP/SAVPF") && !strings.EqualFold(proto, "UDP/TLS/RTP/SAVP") {
		return fmt.Errorf("unsupported WebRTC audio transport %q", proto)
	}
	if _, ok := webRTCAttribute(desc.Attributes, md.Attributes, "rtcp-mux"); !ok {
		return fmt.Errorf("remote WebRTC SDP requires rtcp-mux")
	}
	_, remoteRTCPReducedSize := webRTCAttribute(desc.Attributes, md.Attributes, "rtcp-rsize")
	remoteRTCPReducedSize = remoteRTCPReducedSize && strings.EqualFold(proto, "UDP/TLS/RTP/SAVPF")
	remoteUfrag, ok := webRTCAttribute(desc.Attributes, md.Attributes, "ice-ufrag")
	if !ok || remoteUfrag == "" {
		return fmt.Errorf("remote WebRTC SDP has no ICE username fragment")
	}
	remotePwd, ok := webRTCAttribute(desc.Attributes, md.Attributes, "ice-pwd")
	if !ok || remotePwd == "" {
		return fmt.Errorf("remote WebRTC SDP has no ICE password")
	}
	setup, ok := webRTCAttribute(desc.Attributes, md.Attributes, "setup")
	if !ok {
		return fmt.Errorf("remote WebRTC SDP has no DTLS setup role")
	}
	fingerprintValue, ok := webRTCAttribute(desc.Attributes, md.Attributes, "fingerprint")
	if !ok {
		return fmt.Errorf("remote WebRTC SDP has no DTLS fingerprint")
	}
	fingerprintFields := strings.Fields(fingerprintValue)
	if len(fingerprintFields) != 2 {
		return fmt.Errorf("invalid remote DTLS fingerprint %q", fingerprintValue)
	}

	remoteCodecs, err := webRTCAudioCodecs(&desc, md)
	if err != nil {
		return err
	}
	m.mu.Lock()
	localCodecs := slices.Clone(m.Codecs)
	m.mu.Unlock()
	common := commonWebRTCCodecs(remoteCodecs, localCodecs)
	if len(common) == 0 {
		return fmt.Errorf("remote has no supported audio codec, remote=%v local=%v", remoteCodecs, localCodecs)
	}

	remoteCandidates := make([]ice.Candidate, 0, 4)
	for _, attr := range md.Attributes {
		if attr.Key != "candidate" {
			continue
		}
		candidate, candidateErr := ice.UnmarshalCandidate(attr.Value)
		if candidateErr != nil {
			return &ICEError{Phase: ICEPhaseConnection, Err: fmt.Errorf("parse remote candidate: %w", candidateErr)}
		}
		if candidate.Component() == ice.ComponentRTP {
			remoteCandidates = append(remoteCandidates, candidate)
		}
	}
	if len(remoteCandidates) == 0 {
		return &ICEError{
			Phase: ICEPhaseConnection,
			Err:   fmt.Errorf("remote WebRTC SDP has no component-1 candidates; trickle ICE is not supported"),
		}
	}

	remoteMode := sdp.ModeSendrecv
	for _, direction := range []string{sdp.ModeSendrecv, sdp.ModeSendonly, sdp.ModeRecvonly, "inactive"} {
		if _, exists := webRTCAttribute(desc.Attributes, md.Attributes, direction); exists {
			remoteMode = direction
			break
		}
	}
	remoteSetup := strings.ToLower(setup)
	localDTLSClient := false
	localSetup := ""
	if offered {
		switch remoteSetup {
		case "passive":
			localDTLSClient = true
			localSetup = "active"
		case "active":
			localSetup = "passive"
		default:
			return fmt.Errorf("invalid DTLS setup role %q in answer", setup)
		}
	} else {
		if remoteSetup != "actpass" && remoteSetup != "passive" {
			return fmt.Errorf("invalid DTLS setup role %q in offer", setup)
		}
		localDTLSClient = true
		localSetup = "active"
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.agent == nil {
		return fmt.Errorf("webrtc media session is not initialized")
	}
	if m.iceConn != nil {
		ufragChanged := remoteUfrag != m.remoteUfrag
		pwdChanged := remotePwd != m.remotePwd
		if ufragChanged != pwdChanged {
			return fmt.Errorf("remote ICE restart must change both username fragment and password")
		}
		if ufragChanged {
			return ErrWebRTCICERestart
		}
		if !strings.EqualFold(fingerprintFields[0], m.remoteFingerprintAlgorithm) ||
			!strings.EqualFold(fingerprintFields[1], m.remoteFingerprint) {
			return fmt.Errorf("remote DTLS fingerprint changed without an ICE restart")
		}
		if !offered && remoteSetup == "actpass" {
			localSetup = m.localSetup
		}
		if localSetup != m.localSetup {
			return fmt.Errorf("remote DTLS role changed without an ICE restart")
		}
		// A subsequent offer/answer with unchanged ICE credentials continues the
		// current ICE and DTLS transports. Stage only media-level state so a SIP
		// failure can leave the active stream untouched.
		m.pendingCodecs = slices.Clone(common)
		localPreference := m.modePreference
		if localPreference == "" {
			localPreference = m.Mode
		}
		m.pendingMode = negotiateMediaDirection(remoteMode, localPreference)
		m.pendingRTCPReducedSize = remoteRTCPReducedSize
		return nil
	}
	for _, candidate := range remoteCandidates {
		if err = m.agent.AddRemoteCandidate(candidate); err != nil {
			return &ICEError{Phase: ICEPhaseConnection, Err: fmt.Errorf("add remote candidate: %w", err)}
		}
	}
	m.remoteUfrag = remoteUfrag
	m.remotePwd = remotePwd
	m.remoteFingerprintAlgorithm = fingerprintFields[0]
	m.remoteFingerprint = fingerprintFields[1]
	m.rtcpReducedSize = remoteRTCPReducedSize
	m.filterCodecs = common
	m.Codecs = slices.Clone(common)
	m.codec, _ = CodecAudioFromList(common)
	if m.modePreference == "" {
		m.modePreference = m.Mode
	}
	m.Mode = negotiateMediaDirection(remoteMode, m.modePreference)

	var conn *ice.Conn
	if offered {
		// The offerer is the controlling ICE agent and nominates the pair.
		conn, err = m.agent.StartDial(remoteUfrag, remotePwd)
	} else {
		// The answerer is controlled. For actpass offers we choose active so
		// the answer can start the DTLS handshake without another round trip.
		conn, err = m.agent.StartAccept(remoteUfrag, remotePwd)
	}
	if err != nil {
		return &ICEError{Phase: ICEPhaseConnection, Err: fmt.Errorf("start connectivity checks: %w", err)}
	}
	m.iceConn = conn
	m.mux = newWebRTCPacketMux(conn)

	m.localSetup = localSetup
	dtlsConf := m.Config.DTLS.ToLibConf([]DTLSFingerprint{{
		Algorithm: fingerprintFields[0],
		Value:     fingerprintFields[1],
	}})
	if localDTLSClient {
		m.dtlsConn, err = dtls.Client(m.mux.dtls, conn.RemoteAddr(), dtlsConf)
	} else {
		m.dtlsConn, err = dtls.Server(m.mux.dtls, conn.RemoteAddr(), dtlsConf)
	}
	if err != nil {
		return fmt.Errorf("create DTLS transport: %w", err)
	}

	return nil
}

// Finalize completes ICE first and DTLS second. This order is the central
// WebRTC layering rule: ICE selects and maintains the network path; DTLS and
// all encrypted media then travel only over that path.
func (m *MediaSessionWebrtc) Finalize(ctx context.Context) error {
	m.mu.Lock()
	if m.ready {
		if len(m.pendingCodecs) > 0 {
			m.filterCodecs = m.pendingCodecs
			m.Codecs = slices.Clone(m.pendingCodecs)
			m.codec, _ = CodecAudioFromList(m.pendingCodecs)
			m.Mode = m.pendingMode
			m.rtcpReducedSize = m.pendingRTCPReducedSize
			m.pendingCodecs = nil
			m.pendingMode = ""
			m.pendingRTCPReducedSize = false
		}
		m.mu.Unlock()
		return nil
	}
	agent := m.agent
	dtlsConn := m.dtlsConn
	m.mu.Unlock()
	if agent == nil || dtlsConn == nil {
		return fmt.Errorf("remote WebRTC SDP is not configured")
	}
	if err := agent.AwaitConnect(ctx); err != nil {
		return &ICEError{Phase: ICEPhaseConnection, Err: err}
	}
	if err := dtlsConn.HandshakeContext(ctx); err != nil {
		return fmt.Errorf("DTLS handshake: %w", err)
	}
	state, ok := dtlsConn.ConnectionState()
	if !ok {
		return fmt.Errorf("get DTLS connection state")
	}
	profile, ok := dtlsConn.SelectedSRTPProtectionProfile()
	if !ok {
		return fmt.Errorf("DTLS peer did not negotiate an SRTP profile")
	}
	p := srtp.ProtectionProfile(profile)
	keyLen, err := p.KeyLen()
	if err != nil {
		return fmt.Errorf("get SRTP key length: %w", err)
	}
	saltLen, err := p.SaltLen()
	if err != nil {
		return fmt.Errorf("get SRTP salt length: %w", err)
	}
	material, err := state.ExportKeyingMaterial("EXTRACTOR-dtls_srtp", nil, 2*(keyLen+saltLen))
	if err != nil {
		return fmt.Errorf("export DTLS-SRTP keying material: %w", err)
	}
	clientKey := material[:keyLen]
	serverKey := material[keyLen : 2*keyLen]
	clientSalt := material[2*keyLen : 2*keyLen+saltLen]
	serverSalt := material[2*keyLen+saltLen:]
	// DTLS role, not ICE role, determines which half of the exporter is used.
	if m.localSetup == "passive" {
		clientKey, serverKey = serverKey, clientKey
		clientSalt, serverSalt = serverSalt, clientSalt
	}
	localContext, err := srtp.CreateContext(clientKey, clientSalt, p)
	if err != nil {
		return fmt.Errorf("create local SRTP context: %w", err)
	}
	remoteContext, err := srtp.CreateContext(serverKey, serverSalt, p)
	if err != nil {
		return fmt.Errorf("create remote SRTP context: %w", err)
	}
	localRTCPContext, err := srtp.CreateContext(clientKey, clientSalt, p)
	if err != nil {
		return fmt.Errorf("create local SRTCP context: %w", err)
	}
	remoteRTCPContext, err := srtp.CreateContext(serverKey, serverSalt, p)
	if err != nil {
		return fmt.Errorf("create remote SRTCP context: %w", err)
	}
	pair, _ := agent.GetSelectedCandidatePair()
	m.mu.Lock()
	m.localCtxSRTP = localContext
	m.remoteCtxSRTP = remoteContext
	m.localCtxSRTCP = localRTCPContext
	m.remoteCtxSRTCP = remoteRTCPContext
	m.ready = true
	if pair != nil {
		m.Laddr = pair.Local.Address()
		m.Raddr = pair.Remote.Address()
	}
	m.mu.Unlock()
	return nil
}

// Rollback drops a staged same-ICE-generation media update. It does not touch
// the active ICE, DTLS, SRTP or SRTCP transports.
func (m *MediaSessionWebrtc) Rollback() {
	m.mu.Lock()
	m.pendingCodecs = nil
	m.pendingMode = ""
	m.pendingRTCPReducedSize = false
	m.mu.Unlock()
}

func (m *MediaSessionWebrtc) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	mux := m.mux
	agent := m.agent
	m.mu.Unlock()
	var muxErr, agentErr error
	if mux != nil {
		muxErr = mux.Close()
	}
	if agent != nil {
		agentErr = agent.Close()
	}
	return errors.Join(muxErr, agentErr)
}

func (m *MediaSessionWebrtc) StartRTP(rw int8) error { return m.StopRTP(rw, 0) }

func (m *MediaSessionWebrtc) StopRTP(rw int8, duration time.Duration) error {
	m.mu.Lock()
	mux := m.mux
	m.mu.Unlock()
	if mux == nil {
		return fmt.Errorf("WebRTC RTP transport is not initialized")
	}
	deadline := time.Time{}
	if duration != 0 {
		deadline = time.Now().Add(duration)
	}
	if rw&1 != 0 || rw == 0 {
		return mux.rtp.SetReadDeadline(deadline)
	}
	return nil
}

func (m *MediaSessionWebrtc) ReadRTP(buf []byte, pkt *rtp.Packet) (int, error) {
	if len(buf) < RTPBufSize {
		return 0, io.ErrShortBuffer
	}
	if !m.ready || m.mux == nil || m.remoteCtxSRTP == nil {
		return 0, fmt.Errorf("WebRTC media is not finalized")
	}
	n, _, err := m.mux.rtp.ReadFrom(buf)
	if err != nil {
		return 0, err
	}
	decrypted, err := m.remoteCtxSRTP.DecryptRTP(buf, buf[:n], &pkt.Header)
	if err != nil {
		return n, fmt.Errorf("decrypt WebRTC SRTP: %w", err)
	}
	n = len(decrypted)
	if err = rtpUnmarshalPayload(decrypted, pkt); err != nil {
		return n, fmt.Errorf("unmarshal WebRTC RTP: %w", err)
	}
	if m.Mode == sdp.ModeSendonly || m.Mode == "inactive" {
		return 0, nil
	}
	return n, nil
}

func (m *MediaSessionWebrtc) ReadRTPRaw(buf []byte) (int, error) {
	if m.mux == nil {
		return 0, fmt.Errorf("WebRTC RTP transport is not initialized")
	}
	n, _, err := m.mux.rtp.ReadFrom(buf)
	return n, err
}

func (m *MediaSessionWebrtc) WriteRTP(pkt *rtp.Packet) error {
	if !m.ready || m.mux == nil || m.localCtxSRTP == nil {
		return fmt.Errorf("WebRTC media is not finalized")
	}
	if m.Mode == sdp.ModeRecvonly || m.Mode == "inactive" {
		return nil
	}

	if m.writeRTPBuf == nil {
		m.writeRTPBuf = make([]byte, RTPBufSize)
	}
	buf := m.writeRTPBuf
	n, err := pkt.MarshalTo(buf)
	if err == nil {
		var data []byte
		data, err = m.localCtxSRTP.EncryptRTP(buf, buf[:n], &pkt.Header)
		if err == nil {
			n, err = m.mux.rtp.WriteTo(data, nil)
			if err == nil && n != len(data) {
				err = io.ErrShortWrite
			}
		}
	}
	if err != nil {
		return fmt.Errorf("write WebRTC SRTP: %w", err)
	}
	return nil
}

func (m *MediaSessionWebrtc) ReadRTCP(buf []byte, pkts []rtcp.Packet) (int, error) {
	if !m.ready || m.mux == nil || m.remoteCtxSRTCP == nil {
		return 0, fmt.Errorf("WebRTC media is not finalized")
	}
	n, _, err := m.mux.rtcp.ReadFrom(buf)
	if err != nil {
		return 0, err
	}
	data, err := m.remoteCtxSRTCP.DecryptRTCP(buf, buf[:n], nil)
	if err != nil {
		return 0, fmt.Errorf("decrypt WebRTC SRTCP: %w", err)
	}
	return RTCPUnmarshal(data, pkts)
}

func (m *MediaSessionWebrtc) WriteRTCP(pkt rtcp.Packet) error {
	data, err := pkt.Marshal()
	if err != nil {
		return err
	}

	if !m.ready || m.mux == nil || m.localCtxSRTCP == nil {
		return fmt.Errorf("WebRTC media is not finalized")
	}

	needed := len(data) + 64
	if cap(m.writeRTCPBuf) < needed {
		m.writeRTCPBuf = make([]byte, needed)
	}
	buf := m.writeRTCPBuf[:needed]
	data, err = m.localCtxSRTCP.EncryptRTCP(buf, data, nil)
	if err != nil {
		return fmt.Errorf("encrypt WebRTC SRTCP: %w", err)
	}
	m.writeRTCPBuf = data[:cap(data)]
	n, err := m.mux.rtcp.WriteTo(data, nil)
	if err == nil && n != len(data) {
		err = io.ErrShortWrite
	}
	return err
}

func findWebRTCAudio(desc *webrtcsdp.SessionDescription) *webrtcsdp.MediaDescription {
	for _, md := range desc.MediaDescriptions {
		if md.MediaName.Media == "audio" && md.MediaName.Port.Value != 0 {
			return md
		}
	}
	return nil
}

func webRTCAttribute(session, media []webrtcsdp.Attribute, key string) (string, bool) {
	for _, attr := range media {
		if attr.Key == key {
			return attr.Value, true
		}
	}
	for _, attr := range session {
		if attr.Key == key {
			return attr.Value, true
		}
	}
	return "", false
}

func webRTCAudioCodecs(desc *webrtcsdp.SessionDescription, md *webrtcsdp.MediaDescription) ([]Codec, error) {
	attrs := make([]string, 0, len(desc.Attributes)+len(md.Attributes))
	for _, attr := range desc.Attributes {
		attrs = append(attrs, attr.String())
	}
	for _, attr := range md.Attributes {
		attrs = append(attrs, attr.String())
	}
	codecs := make([]Codec, len(md.MediaName.Formats))
	n, err := CodecsFromSDPRead(md.MediaName.Formats, attrs, codecs)
	if err != nil {
		return nil, fmt.Errorf("parse remote WebRTC codecs: %w", err)
	}
	return codecs[:n], nil
}

func commonWebRTCCodecs(remote, local []Codec) []Codec {
	common := make([]Codec, 0, len(remote))
	for _, rc := range remote {
		for _, lc := range local {
			if !strings.EqualFold(rc.Name, lc.Name) || rc.SampleRate != lc.SampleRate || rc.NumChannels != lc.NumChannels {
				continue
			}
			// The answer must echo the offer's payload type. Keep local packet
			// duration/capability data but use the negotiated remote PT.
			lc.PayloadType = rc.PayloadType
			common = append(common, lc)
			break
		}
	}
	return common
}
