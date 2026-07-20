// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2026, Emir Aganovic

package mediaweb

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emiago/diago/media"
	"github.com/emiago/diago/media/sdp"
	"github.com/emiago/dtls/v3"
	"github.com/pion/ice/v4"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	webrtcsdp "github.com/pion/sdp/v3"
	"github.com/pion/srtp/v3"
	"github.com/pion/stun/v3"
)

// MediaSessionWebrtcConfig contains transport settings which do not belong to
// the codec-level media API. ICEURLs accepts STUN and TURN URLs understood by
// Pion (for example stun:stun.example.org:3478).
type MediaSessionWebrtcConfig struct {
	ICEURLs         []string
	NetworkTypes    []ice.NetworkType
	PortMin         uint16
	PortMax         uint16
	IncludeLoopback bool
	InterfaceFilter func(interfaceName string) bool
	DTLS            media.DTLSConfig
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
	Codecs []media.Codec
	Mode   string

	Config MediaSessionWebrtcConfig

	mu              sync.Mutex
	agent           *ice.Agent
	localCandidates []ice.Candidate
	localUfrag      string
	localPwd        string
	remoteUfrag     string
	remotePwd       string
	localSetup      string
	codec           media.Codec
	filterCodecs    []media.Codec
	iceConn         *ice.Conn
	mux             *webRTCPacketMux
	dtlsConn        *dtls.Conn
	localCtxSRTP    *srtp.Context
	remoteCtxSRTP   *srtp.Context
	ready           bool
	closed          bool
	writeRTPBuf     []byte
	readRTPFromAddr net.Addr
}

// Init creates the ICE agent and waits for candidate gathering to finish. SIP
// offer/answer has no standard trickle-ICE exchange, so the SDP returned later
// contains a complete candidate set and a=end-of-candidates.
func (m *MediaSessionWebrtc) Init(ctx context.Context, conf MediaSessionWebrtcConfig) error {
	m.mu.Lock()
	if m.agent != nil {
		m.mu.Unlock()
		return fmt.Errorf("webrtc media session is already initialized")
	}
	m.Config = conf
	m.mu.Unlock()

	if len(conf.DTLS.Certificates) == 0 {
		return fmt.Errorf("webrtc media session requires a DTLS certificate")
	}
	if len(m.Codecs) == 0 {
		return fmt.Errorf("webrtc media session requires at least one codec")
	}

	networkTypes := conf.NetworkTypes
	if len(networkTypes) == 0 {
		networkTypes = []ice.NetworkType{ice.NetworkTypeUDP4, ice.NetworkTypeUDP6}
	}
	opts := []ice.AgentOption{ice.WithNetworkTypes(networkTypes)}
	if conf.PortMin != 0 || conf.PortMax != 0 {
		if conf.PortMin == 0 || conf.PortMax < conf.PortMin {
			return fmt.Errorf("invalid ICE UDP port range %d-%d", conf.PortMin, conf.PortMax)
		}
		opts = append(opts, ice.WithPortRange(conf.PortMin, conf.PortMax))
	}
	if conf.IncludeLoopback {
		opts = append(opts, ice.WithIncludeLoopback())
	}
	if conf.InterfaceFilter != nil {
		opts = append(opts, ice.WithInterfaceFilter(conf.InterfaceFilter))
	}
	if len(conf.ICEURLs) > 0 {
		urls := make([]*stun.URI, 0, len(conf.ICEURLs))
		for _, rawURL := range conf.ICEURLs {
			u, err := stun.ParseURI(rawURL)
			if err != nil {
				return fmt.Errorf("parse ICE URL %q: %w", rawURL, err)
			}
			urls = append(urls, u)
		}
		opts = append(opts, ice.WithUrls(urls))
	}

	agent, err := ice.NewAgentWithOptions(opts...)
	if err != nil {
		return fmt.Errorf("create ICE agent: %w", err)
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
		return fmt.Errorf("set ICE candidate handler: %w", err)
	}
	ufrag, pwd, err := agent.GetLocalUserCredentials()
	if err != nil {
		_ = agent.Close()
		return fmt.Errorf("get local ICE credentials: %w", err)
	}
	m.mu.Lock()
	m.agent = agent
	m.localUfrag = ufrag
	m.localPwd = pwd
	m.mu.Unlock()

	if err = agent.GatherCandidates(); err != nil {
		_ = m.Close()
		return fmt.Errorf("gather ICE candidates: %w", err)
	}
	select {
	case <-ctx.Done():
		_ = m.Close()
		return fmt.Errorf("gather ICE candidates: %w", ctx.Err())
	case <-gathered:
	}
	m.mu.Lock()
	count := len(m.localCandidates)
	m.mu.Unlock()
	if count == 0 {
		_ = m.Close()
		return fmt.Errorf("ICE gathered no local candidates")
	}
	return nil
}

func (m *MediaSessionWebrtc) Codec() media.Codec {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.codec.SampleRate != 0 {
		return m.codec
	}
	codec, _ := media.CodecAudioFromList(m.Codecs)
	return codec
}

func (m *MediaSessionWebrtc) CommonCodecs() []media.Codec {
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
	}
	codecs := m.Codecs
	if len(m.filterCodecs) > 0 {
		codecs = m.filterCodecs
	}
	return m.localSDPLocked(codecs, setup)
}

func (m *MediaSessionWebrtc) localSDPLocked(codecs []media.Codec, setup string) ([]byte, error) {
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
	mode := m.Mode
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
		"a=rtcp-rsize",
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
			return fmt.Errorf("parse remote ICE candidate: %w", candidateErr)
		}
		if candidate.Component() == ice.ComponentRTP {
			remoteCandidates = append(remoteCandidates, candidate)
		}
	}
	if len(remoteCandidates) == 0 {
		return fmt.Errorf("remote WebRTC SDP has no component-1 ICE candidates; trickle ICE is not supported")
	}

	remoteMode := sdp.ModeSendrecv
	for _, direction := range []string{sdp.ModeSendrecv, sdp.ModeSendonly, sdp.ModeRecvonly, "inactive"} {
		if _, exists := webRTCAttribute(desc.Attributes, md.Attributes, direction); exists {
			remoteMode = direction
			break
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	if m.agent == nil {
		return fmt.Errorf("webrtc media session is not initialized")
	}
	if m.iceConn != nil {
		return fmt.Errorf("remote WebRTC SDP is already configured; ICE restart is not supported")
	}
	for _, candidate := range remoteCandidates {
		if err = m.agent.AddRemoteCandidate(candidate); err != nil {
			return fmt.Errorf("add remote ICE candidate: %w", err)
		}
	}
	m.remoteUfrag = remoteUfrag
	m.remotePwd = remotePwd
	m.filterCodecs = common
	m.Codecs = slices.Clone(common)
	m.codec, _ = media.CodecAudioFromList(common)
	m.Mode = negotiateMediaDirection(remoteMode, m.Mode)

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
		return fmt.Errorf("start ICE connectivity checks: %w", err)
	}
	m.iceConn = conn
	m.mux = newWebRTCPacketMux(conn)

	remoteSetup := strings.ToLower(setup)
	localDTLSClient := false
	if offered {
		switch remoteSetup {
		case "passive":
			localDTLSClient = true
			m.localSetup = "active"
		case "active":
			m.localSetup = "passive"
		default:
			return fmt.Errorf("invalid DTLS setup role %q in answer", setup)
		}
	} else {
		if remoteSetup != "actpass" && remoteSetup != "passive" {
			return fmt.Errorf("invalid DTLS setup role %q in offer", setup)
		}
		localDTLSClient = true
		m.localSetup = "active"
	}
	dtlsConf := m.Config.DTLS.ToLibConf([]media.DTLSFingerprint{{
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
	agent := m.agent
	dtlsConn := m.dtlsConn
	m.mu.Unlock()
	if agent == nil || dtlsConn == nil {
		return fmt.Errorf("remote WebRTC SDP is not configured")
	}
	if err := agent.AwaitConnect(ctx); err != nil {
		return fmt.Errorf("ICE connectivity checks: %w", err)
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
	pair, _ := agent.GetSelectedCandidatePair()
	m.mu.Lock()
	m.localCtxSRTP = localContext
	m.remoteCtxSRTP = remoteContext
	m.ready = true
	if pair != nil {
		m.Laddr = pair.Local.Address()
		m.Raddr = pair.Remote.Address()
	}
	m.mu.Unlock()
	return nil
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
	if len(buf) < media.RTPBufSize {
		return 0, io.ErrShortBuffer
	}
	m.mu.Lock()
	mux, remoteContext, ready, mode := m.mux, m.remoteCtxSRTP, m.ready, m.Mode
	m.mu.Unlock()
	if !ready || mux == nil || remoteContext == nil {
		return 0, fmt.Errorf("WebRTC media is not finalized")
	}
	n, from, err := mux.rtp.ReadFrom(buf)
	if err != nil {
		return 0, err
	}
	decrypted, err := remoteContext.DecryptRTP(buf, buf[:n], &pkt.Header)
	if err != nil {
		return n, fmt.Errorf("decrypt WebRTC SRTP: %w", err)
	}
	n = len(decrypted)
	if err = rtpUnmarshalPayload(decrypted, pkt); err != nil {
		return n, fmt.Errorf("unmarshal WebRTC RTP: %w", err)
	}
	m.mu.Lock()
	m.readRTPFromAddr = from
	m.mu.Unlock()
	if mode == sdp.ModeSendonly || mode == "inactive" {
		return 0, nil
	}
	return n, nil
}

func (m *MediaSessionWebrtc) ReadRTPRaw(buf []byte) (int, error) {
	m.mu.Lock()
	mux := m.mux
	m.mu.Unlock()
	if mux == nil {
		return 0, fmt.Errorf("WebRTC RTP transport is not initialized")
	}
	n, _, err := mux.rtp.ReadFrom(buf)
	return n, err
}

func (m *MediaSessionWebrtc) WriteRTP(pkt *rtp.Packet) error {
	m.mu.Lock()
	if !m.ready || m.mux == nil || m.localCtxSRTP == nil {
		m.mu.Unlock()
		return fmt.Errorf("WebRTC media is not finalized")
	}
	if m.Mode == sdp.ModeRecvonly || m.Mode == "inactive" {
		m.mu.Unlock()
		return nil
	}
	if m.writeRTPBuf == nil {
		m.writeRTPBuf = make([]byte, media.RTPBufSize)
	}
	buf := m.writeRTPBuf
	ctx := m.localCtxSRTP
	mux := m.mux
	n, err := pkt.MarshalTo(buf)
	if err == nil {
		var data []byte
		data, err = ctx.EncryptRTP(buf, buf[:n], &pkt.Header)
		if err == nil {
			n, err = mux.rtp.WriteTo(data, nil)
			if err == nil && n != len(data) {
				err = io.ErrShortWrite
			}
		}
	}
	m.mu.Unlock()
	if err != nil {
		return fmt.Errorf("write WebRTC SRTP: %w", err)
	}
	return nil
}

func (m *MediaSessionWebrtc) ReadRTCP(buf []byte, pkts []rtcp.Packet) (int, error) {
	m.mu.Lock()
	mux, ctx, ready := m.mux, m.remoteCtxSRTP, m.ready
	m.mu.Unlock()
	if !ready || mux == nil || ctx == nil {
		return 0, fmt.Errorf("WebRTC media is not finalized")
	}
	n, _, err := mux.rtcp.ReadFrom(buf)
	if err != nil {
		return 0, err
	}
	data, err := ctx.DecryptRTCP(buf, buf[:n], nil)
	if err != nil {
		return 0, fmt.Errorf("decrypt WebRTC SRTCP: %w", err)
	}
	return rtcpUnmarshal(data, pkts)
}

func (m *MediaSessionWebrtc) WriteRTCP(pkt rtcp.Packet) error {
	m.mu.Lock()
	mux, ctx, ready := m.mux, m.localCtxSRTP, m.ready
	m.mu.Unlock()
	if !ready || mux == nil || ctx == nil {
		return fmt.Errorf("WebRTC media is not finalized")
	}
	data, err := pkt.Marshal()
	if err != nil {
		return err
	}
	buf := make([]byte, len(data)+64)
	data, err = ctx.EncryptRTCP(buf, data, nil)
	if err != nil {
		return fmt.Errorf("encrypt WebRTC SRTCP: %w", err)
	}
	n, err := mux.rtcp.WriteTo(data, nil)
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

func webRTCAudioCodecs(desc *webrtcsdp.SessionDescription, md *webrtcsdp.MediaDescription) ([]media.Codec, error) {
	attrs := make([]string, 0, len(desc.Attributes)+len(md.Attributes))
	for _, attr := range desc.Attributes {
		attrs = append(attrs, attr.String())
	}
	for _, attr := range md.Attributes {
		attrs = append(attrs, attr.String())
	}
	codecs := make([]media.Codec, len(md.MediaName.Formats))
	n, err := media.CodecsFromSDPRead(md.MediaName.Formats, attrs, codecs)
	if err != nil {
		return nil, fmt.Errorf("parse remote WebRTC codecs: %w", err)
	}
	return codecs[:n], nil
}

func commonWebRTCCodecs(remote, local []media.Codec) []media.Codec {
	common := make([]media.Codec, 0, len(remote))
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

func negotiateMediaDirection(remoteMode, localPreference string) string {
	if localPreference == "" {
		localPreference = sdp.ModeSendrecv
	}
	switch remoteMode {
	case "inactive":
		return "inactive"
	case sdp.ModeSendonly:
		if localPreference == "inactive" {
			return "inactive"
		}
		return sdp.ModeRecvonly
	case sdp.ModeRecvonly:
		if localPreference == "inactive" {
			return "inactive"
		}
		return sdp.ModeSendonly
	case sdp.ModeSendrecv:
		switch localPreference {
		case sdp.ModeSendrecv, sdp.ModeSendonly, sdp.ModeRecvonly, "inactive":
			return localPreference
		}
	}
	return sdp.ModeSendrecv
}

func dtlsSHA256Fingerprint(cert tls.Certificate) (string, error) {
	if len(cert.Certificate) == 0 {
		return "", fmt.Errorf("no certificate data found")
	}
	leaf, err := x509.ParseCertificate(cert.Certificate[0])
	if err != nil {
		return "", fmt.Errorf("parse certificate: %w", err)
	}
	hash := sha256.Sum256(leaf.Raw)
	hexString := strings.ToUpper(hex.EncodeToString(hash[:]))
	var fingerprint strings.Builder
	for i := 0; i < len(hexString); i += 2 {
		if i > 0 {
			fingerprint.WriteByte(':')
		}
		fingerprint.WriteString(hexString[i : i+2])
	}
	return fingerprint.String(), nil
}

func rtpUnmarshalPayload(buf []byte, packet *rtp.Packet) error {
	headerSize := packet.Header.MarshalSize()
	end := len(buf)
	if packet.Header.Padding {
		if end <= headerSize {
			return io.ErrUnexpectedEOF
		}
		packet.Header.PaddingSize = buf[end-1]
		end -= int(packet.Header.PaddingSize)
	} else {
		packet.Header.PaddingSize = 0
	}
	packet.PaddingSize = packet.Header.PaddingSize
	if end < headerSize {
		return io.ErrUnexpectedEOF
	}
	packet.Payload = buf[headerSize:end]
	return nil
}
