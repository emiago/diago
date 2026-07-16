// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package media

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/emiago/diago/media/sdp"
	"github.com/emiago/sipgo/sip"
	"github.com/pion/dtls/v3"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/srtp/v3"
)

// Negotiation failures. Each one means the remote SDP and our configuration have
// no overlap, which is a property of the peer's offer and not a fault in this
// process. They are exported so a caller can tell the two apart with errors.Is:
// an offer we cannot meet should be answered with 488 Not Acceptable Here, while
// an internal error should not. The wrapped text carries the specifics.
var (
	// ErrNoCommonCodec means the audio formats do not intersect: the peer
	// offered no codec we support, or none that could be parsed.
	ErrNoCommonCodec = errors.New("no common codec")

	// ErrNoCommonCrypto means the media is negotiable but the keying is not.
	// The peer asked for secure RTP and no SRTP context could be built from what
	// it offered. Kept apart from ErrNoCommonCodec because a codec mismatch is a
	// capability gap while this is a security policy gap.
	ErrNoCommonCrypto = errors.New("no common crypto")

	// ErrNoCommonMedia means the m= line itself is unusable before codecs are
	// reached: a transport we do not speak, or an address and port that cannot
	// carry RTP.
	ErrNoCommonMedia = errors.New("no common media")
)

var (
	// RTPPortStart and RTPPortEnd allows defining rtp port range for media
	RTPPortStart  = 0
	RTPPortEnd    = 0
	rtpPortOffset = atomic.Int32{}

	// When reading RTP use at least MTU size. Increase this
	RTPBufSize = 1500

	RTPDebug  = false
	RTCPDebug = false

	// RTPProfileSAVPDisable disables offering RTP/SAVP and keeps standard RTP/AVP for backward compatibilit needs
	//
	// Experimental
	RTPProfileSAVPDisable = false

	// RTPNAT options
	RTPNATDisabled = 0
	RTPNATSymetric = 1

	// SDP codec exchanges
	// 0 (Default) - answerer prefers offerer order recomended by rfc
	// 1 - answerer prefers local order
	SDPCodecPreferLocalOrder int = 0
)

func logRTPRead(m *MediaSession, raddr net.Addr, p *rtp.Packet) {
	if RTPDebug {
		s := raddr.String()

		DefaultLogger().Debug(fmt.Sprintf("RTP read %s < %s:\n%s", m.Laddr.String(), s, p.String()))
	}
}

func logRTPWrite(m *MediaSession, p *rtp.Packet) {
	if RTPDebug {
		DefaultLogger().Debug(fmt.Sprintf("RTP write %s > %s:\n%s", m.Laddr.String(), m.Raddr.String(), p.String()))
	}
}

func logRTCPRead(m *MediaSession, pkts []rtcp.Packet) {
	if RTCPDebug {
		laddr := m.rtcpConn.LocalAddr()
		for _, p := range pkts {
			DefaultLogger().Debug(fmt.Sprintf("RTCP read %s < %s:\n%s", laddr.String(), m.rtcpRaddr.String(), StringRTCP(p)))
		}
	}
}

func logRTCPWrite(m *MediaSession, p rtcp.Packet) {
	if RTCPDebug {
		laddr := m.rtcpConn.LocalAddr()
		DefaultLogger().Debug(fmt.Sprintf("RTCP write %s > %s:\n%s", laddr.String(), m.rtcpRaddr.String(), StringRTCP(p)))
	}
}

// MediaSession represents active media session with RTP/RTCP
// TODO: multiple media descriptions.
// Consider https://datatracker.ietf.org/doc/rfc3388/ for grouping multiple media
//
// Design:
// - It identfies single session Laddr <-> Raddr
// - With multi descriptions, or reinvites it should be forked and create new media Session
//
// NOTE: Not thread safe, read only after SDP negotiation or have locking in place
//       Object should be immutable, that is post session changes like codecs, remote addr would require Forking object with Fork call.

type MediaSession struct {
	// SDP stuff
	// TODO:
	// 1. make this list of codecs as we need to match also sample rate and ptime
	// 2. rtp session when matching incoming packet sample rate for RTCP should use this

	// Codecs are initial list of Codecs that would be used in SDP generation
	Codecs []Codec

	// RemoteSDPIsAnswer declares that the next body passed to RemoteSDP answers
	// an offer we made, rather than offering to us. It is a property of the body
	// and not of the dialog: a UAC offers on its INVITE and answers an inbound
	// re-INVITE, so the role changes within one session.
	//
	// It only decides who we are for SDPCodecPreferLocalOrder, which applies to
	// the answerer alone. Default false means the body is an offer and we are the
	// answerer, which is the common case and the previous behaviour.
	RemoteSDPIsAnswer bool

	sdp []byte

	// Mode is sdp mode. Check consts sdp.ModeRecvOnly etc...
	Mode string
	// Laddr our local address which has full IP and port after media session creation
	Laddr net.UDPAddr
	// Raddr is our target remote address. Normally it is resolved by SDP parsing.
	Raddr net.UDPAddr
	// ExternalIP that should be used for building SDP
	ExternalIP net.IP

	SecureRTP int // 0 none, 1 - SDES, 2 - DTLS
	// TODO support multile for offering
	SRTPAlg uint16

	// DTLSConf used for DTLS
	DTLSConf DTLSConfig

	// ICEConf enables ICE (RFC 8445) on this session. It is only honoured
	// together with SecureRTP = SecureRTPModeDTLS, which is the WebRTC media
	// profile. An ICE session binds one socket instead of two and forces
	// rtcp-mux, because ICE nominates a single candidate pair.
	ICEConf *ICEConfig

	// DTLSRole forces the offer/answer role instead of inferring it from
	// whether a remote address is known. Check DTLSEndpointRole.
	DTLSRole DTLSEndpointRole

	// mode set after negotiation
	mode string

	// filterCodecs is common list of codecs after negotiation
	filterCodecs []Codec
	rtpConn      net.PacketConn
	rtcpConn     net.PacketConn
	rtcpRaddr    net.UDPAddr
	writeRTPBuf  []byte

	// SRTP
	localCtxSRTP  *srtp.Context
	remoteCtxSRTP *srtp.Context
	srtpRemoteTag int

	// RTP NAT enables handling RTP behind NAT. Checkout also RTPSourceLock
	RTPNAT          int // 0 - disabled, 1 - Learn source change (RTP Symetric)
	learnedRTPFrom  atomic.Pointer[net.UDPAddr]
	learnedRTCPFrom atomic.Pointer[net.UDPAddr]

	// ReadRTPFromAddr is set after Read operation. NOT THREAD SAFE and should be only used together with Read
	// It can be used to validate source of RTP packet
	ReadRTPFromAddr net.Addr
	// remoteProto is the media protocol from the remote SDP offer (e.g. "RTP/AVP", "RTP/SAVP")
	remoteProto string

	// DTLS
	dtlsConn *dtls.Conn
	// dtlsTr is the transport dtlsConn was built on. It is held so the handshake
	// can hand the socket back once the keying material is exported.
	dtlsTr *dtlsKeyExchangeConn

	// ICE
	iceAgent *ICEAgent
	// iceUDPConn is the single socket an ICE session binds. Ownership passes
	// to iceAgent once its UDP mux wraps it.
	iceUDPConn *net.UDPConn
	// iceMux splits the nominated ICE pair into DTLS, RTP and RTCP. It is
	// installed once connectivity checks pass, and rtpConn/rtcpConn then point
	// into it.
	iceMux         *iceMux
	rtcpMux        bool
	remoteICEUfrag string
	remoteICEPwd   string

	onFinalize func() error

	sessionID      uint64
	sessionVersion uint64
}

// iceEnabled reports whether this session negotiates ICE. ICE is only
// supported on the DTLS profile: the SDES and plain RTP paths keep the two
// socket layout, and nothing signals ICE for them.
func (s *MediaSession) iceEnabled() bool {
	return s.ICEConf != nil && s.SecureRTP == SecureRTPModeDTLS
}

// dtlsEndpointRole resolves the signalling role. Without an explicit
// DTLSRole it is inferred the same way LocalSDP picks a=setup: knowing the
// remote address already means we are answering.
func (s *MediaSession) dtlsEndpointRole() DTLSEndpointRole {
	if s.DTLSRole != DTLSEndpointRoleUnknown {
		return s.DTLSRole
	}
	if s.Raddr.IP != nil {
		return DTLSEndpointRoleAnswerer
	}
	return DTLSEndpointRoleOfferer
}

func NewMediaSession(ip net.IP, port int) (s *MediaSession, e error) {
	s = &MediaSession{
		Codecs: []Codec{
			CodecAudioUlaw, CodecAudioAlaw, CodecTelephoneEvent8000,
		},
		Mode: sdp.ModeSendrecv,
	}
	s.Laddr.IP = ip
	s.Laddr.Port = port

	return s, s.Init()
}

// Init should be called if session is created manually
// Use NewMediaSession for default building
func (s *MediaSession) Init() error {
	if len(s.Codecs) == 0 {
		return fmt.Errorf("media session: formats can not be empty")
	}

	if s.Mode == "" {
		return fmt.Errorf("media session: mode must be set")
	}

	if s.Laddr.IP == nil {
		return fmt.Errorf("media session: local addr must be set")
	}

	if s.SecureRTP > 0 && s.SRTPAlg == 0 {
		s.SRTPAlg = uint16(srtp.ProtectionProfileAes128CmHmacSha1_80)
	}

	// A session that already holds a connection and is initialized again is
	// rebinding: this is Fork followed by a new Laddr, which starts a new
	// listener below. ExternalIP is the address the OLD socket was reachable on,
	// so it cannot describe the new one and must be set again by the caller if it
	// still applies.
	if s.rtpConn != nil {
		s.ExternalIP = nil
	}

	// Try to listen on this ports
	if err := s.createListeners(&s.Laddr); err != nil {
		return err
	}

	if s.iceEnabled() {
		// Candidates must be gathered before LocalSDP can offer them, so the
		// agent is built here rather than lazily during negotiation.
		if err := s.initICE(); err != nil {
			return err
		}
	}

	return nil
}

// initICE builds the ICE agent over the session socket and gathers candidates.
//
// A failure is fatal for the session. There is no second transport to fall
// back to: an ICE session binds no RTP or RTCP socket of its own, so carrying
// on without an agent would leave it with no media path at all.
func (s *MediaSession) initICE() error {
	agent, err := NewICEAgent(*s.ICEConf)
	if err != nil {
		_ = s.iceUDPConn.Close()
		s.iceUDPConn = nil
		return fmt.Errorf("media session: ice agent: %w", err)
	}

	// From here the agent owns the socket through its UDP mux, and releasing
	// the agent is what releases the socket.
	if err := agent.Init(context.Background(), s.iceUDPConn); err != nil {
		_ = agent.Close()
		s.iceUDPConn = nil
		return fmt.Errorf("media session: ice init: %w", err)
	}
	s.iceAgent = agent
	return nil
}

func (s *MediaSession) InitWithListeners(lRTP net.PacketConn, lRTCP net.PacketConn, raddr *net.UDPAddr) {
	s.Mode = sdp.ModeSendrecv
	s.rtpConn = lRTP
	s.rtcpConn = lRTCP
	laddr, port, _ := sip.ParseAddr(lRTCP.LocalAddr().String())
	s.Laddr = net.UDPAddr{IP: net.ParseIP(laddr), Port: port}
	s.SetRemoteAddr(raddr)
}

func (s *MediaSession) String() string {
	return fmt.Sprintln(
		"rtp.connection", s.rtpConn.LocalAddr().String(),
		"rtcp.connection", s.rtcpConn.LocalAddr().String(),
		"remote.addr", s.Raddr.String(),
	)
}

// InitWithSDP allows creating media session with own SDP and bypassing other needs
func (s *MediaSession) InitWithSDP(localSDP []byte) error {
	s.sdp = localSDP
	sd := sdp.SessionDescription{}
	if err := sdp.Unmarshal(localSDP, &sd); err != nil {
		return fmt.Errorf("fail to parse received SDP: %w", err)
	}

	ci, err := sd.ConnectionInformation()
	if err != nil {
		return err
	}
	md, err := sd.MediaDescription("audio")
	if err != nil {
		return err
	}
	s.Laddr = net.UDPAddr{IP: ci.IP, Port: md.Port}
	s.Mode = sdp.ModeSendrecv
	// TODO check sendrecv from attributes
	codecs := make([]Codec, len(md.Formats))
	n, _ := CodecsFromSDPRead(md.Formats, sd.Values("a"), codecs)
	s.Codecs = codecs[:n]
	return nil
}

func (s *MediaSession) StopRTP(rw int8, dur time.Duration) error {
	t := time.Now().Add(dur)
	if rw&1 > 0 {
		//Read stop
		return s.rtpConn.SetReadDeadline(t)
	}
	if rw&2 > 0 {
		//Write stop
		return s.rtpConn.SetWriteDeadline(t)
	}
	return s.rtpConn.SetDeadline(t)
}

func (s *MediaSession) StartRTP(rw int8) error {
	if rw&1 > 0 {
		return s.rtpConn.SetReadDeadline(time.Time{})
	}
	if rw&2 > 0 {
		return s.rtpConn.SetWriteDeadline(time.Time{})
	}
	return s.rtpConn.SetDeadline(time.Time{})
}

// Fork is special call to be used in case when there is session update
// It preserves pointer to same conneciton but rest is removed
func (s *MediaSession) Fork() *MediaSession {
	cp := MediaSession{
		Laddr:          s.Laddr, // TODO clone it although it is read only
		rtpConn:        s.rtpConn,
		rtcpConn:       s.rtcpConn,
		Codecs:         slices.Clone(s.Codecs),
		Mode:           s.Mode,
		RTPNAT:         s.RTPNAT,
		sdp:            slices.Clone(s.sdp),
		sessionID:      s.sessionID,
		sessionVersion: s.sessionVersion,
		DTLSConf:       s.DTLSConf,
		ICEConf:        s.ICEConf,
		DTLSRole:       s.DTLSRole,
		rtcpMux:        s.rtcpMux,
		// The mux is shared like rtpConn and rtcpConn above, so a renegotiated
		// session keeps sending DTLS to the right stream. The agent is
		// deliberately not carried over: it owns the socket, and a fork closing
		// it would tear down the media of the session it was forked from. That
		// means a fork renegotiates over the existing pair rather than
		// restarting ICE.
		iceMux: s.iceMux,
		// ExternalIP is what LocalSDP publishes as the c= address, and LocalSDP
		// regenerates on every call for a negotiated session. Dropping it here
		// made the 200 OK answering any re-INVITE advertise the internal bind
		// address, so the peer sent RTP somewhere it could not route and the call
		// went one way from the first re-INVITE on, with a clean SIP trace.
		ExternalIP: s.ExternalIP,
		// The role carries over so the caller can set it once on the session it
		// owns. Fork is called inside the re-negotiation path, where the fork
		// itself is not reachable to configure.
		RemoteSDPIsAnswer: s.RemoteSDPIsAnswer,
	}
	return &cp
}

func (s *MediaSession) Close() error {
	// panic("calling close")
	var e1, e2, e3 error
	if s.rtcpConn != nil {
		e1 = s.rtcpConn.Close()
	}

	if s.rtpConn != nil {
		e2 = s.rtpConn.Close()
	}

	// The agent holds the session socket through its UDP mux and it owns the
	// gathering and connectivity check goroutines, so it must be released on
	// every path. A session that failed before Finalize never installed
	// rtpConn, so the closes above do not cover it.
	if s.iceAgent != nil {
		e3 = s.iceAgent.Close()
		s.iceAgent = nil
		s.iceUDPConn = nil
	}
	return errors.Join(e1, e2, e3)
}

// SetRemoteAddr is helper to set Raddr and rtcp address.
// It is not thread safe
func (s *MediaSession) SetRemoteAddr(raddr *net.UDPAddr) {
	s.Raddr = *raddr
	s.rtcpRaddr = net.UDPAddr{
		IP:   raddr.IP,
		Port: raddr.Port + 1,
		Zone: raddr.Zone,
	}
}

// LocalSDP generates SDP based on local settings and remote SDP
// It should never be called in parallel to RemoteSDP, as it is expected serial process
func (s *MediaSession) LocalSDP() []byte {
	if len(s.sdp) > 0 {
		// If media session is static then just return sdp.
		return s.sdp
	}

	ip := s.Laddr.IP
	rtpPort := s.Laddr.Port
	connIP := s.ExternalIP
	if connIP == nil {
		connIP = ip
	}

	codecs := s.activeCodecs()

	var localSDES sdesInline
	rtpProfile := "RTP/AVP"
	if s.SecureRTP == 1 {
		// RFC 4568/8643: only include crypto when offering (no remote SDP yet)
		// or when the peer actually offered SRTP
		if s.Raddr.IP == nil || s.remoteCtxSRTP != nil {
			err := func() error {
				// TODO detect algorithm
				profile := srtp.ProtectionProfile(s.SRTPAlg)
				keysalt, keyLen, err := generateMasterKeySalt(profile)
				if err != nil {
					return err
				}
				masterKey, masterSalt := keysalt[:keyLen], keysalt[keyLen:]

				inline := base64.StdEncoding.EncodeToString(keysalt)
				localSDES = sdesInline{
					alg:    srtpProfileString(profile),
					base64: inline,
					tag:    1,
				}

				ctx, err := srtp.CreateContext(masterKey, masterSalt, profile)
				if err != nil {
					return fmt.Errorf("CreateContext failed: %v", err)
				}

				s.localCtxSRTP = ctx

				if s.srtpRemoteTag > 0 {
					// Match remote tag if exists
					localSDES.tag = s.srtpRemoteTag
				}

				// NOTE: For some compatibility reasons (like asterisk) it would be required that this stays on RTP/AVP
				// When the remote offer uses RTP/SAVP, we must mirror it per RFC 3264
				if !RTPProfileSAVPDisable || s.remoteProto == "RTP/SAVP" {
					rtpProfile = "RTP/SAVP"
				}
				return nil
			}()
			if err != nil {
				DefaultLogger().Error("Failed to setup SRTP context", "error", err)
			}
		}
	}

	var dtlsSet *dtlsSetup
	if s.SecureRTP == 2 {
		rtpProfile = "UDP/TLS/RTP/SAVP"
		dtlsSet = &dtlsSetup{
			setup:        "active",
			fingerprints: make([]sdpFingerprints, len(s.DTLSConf.Certificates)),
		}
		if s.Raddr.IP != nil {
			// We do have remote IP, so probably we are server
			//  lets be then passive roll
			dtlsSet.setup = "passive"
		}

		// Allow overriding
		if s.DTLSConf.SDPSetupRole != nil {
			dtlsSet.setup = s.DTLSConf.SDPSetupRole(s.Raddr.IP != nil)
		} else if s.DTLSRole != DTLSEndpointRoleUnknown {
			// An explicit role overrides the remote address heuristic above.
			dtlsSet.setup = "active"
			if s.DTLSRole == DTLSEndpointRoleAnswerer {
				dtlsSet.setup = "passive"
			}
		}
		// DTLS
		// This is only needed for self signed certificates?
		// https://datatracker.ietf.org/doc/html/rfc5763#section-2
		// 	 If Alice uses only self- signed certificates for the communication with Bob, a fingerprint is
		//    included in the SDP offer/answer exchange.
		// 		The fingerprint alone protects against active attacks on the media
		//    but not active attacks on the signaling.  In order to prevent active
		//    attacks on the signaling, "Enhancements for Authenticated Identity
		//    Management in the Session Initiation Protocol (SIP)" [RFC4474]
		for i, cert := range s.DTLSConf.Certificates {
			fingerprint, err := dtlsSHA256Fingerprint(cert)
			if err != nil {
				DefaultLogger().Error("Failed to generate dtls certificate fingerprint", "error", err)
				continue
			}
			dtlsSet.fingerprints[i] = sdpFingerprints{
				fingerprint: fingerprint,
				alg:         "SHA-256",
			}
		}

	}

	var iceSet *iceSetup
	if s.iceAgent != nil {
		ufrag, pwd := s.iceAgent.Credentials()
		candidates := s.iceAgent.Candidates()
		attrs := make([]string, 0, len(candidates))
		for _, c := range candidates {
			attrs = append(attrs, iceCandidateSDP(c))
		}
		iceSet = &iceSetup{
			ufrag:      ufrag,
			pwd:        pwd,
			candidates: attrs,
			rtcpMux:    s.rtcpMux,
		}
	}

	if s.sessionID == 0 {
		s.sessionID = GetCurrentNTPTimestamp()
		s.sessionVersion = s.sessionID
	} else {
		s.sessionVersion++
	}

	// handle media direction mode
	mode := s.mode
	if mode == "" {
		mode = s.Mode
	}

	return generateSDPForAudio(s.sessionID, s.sessionVersion, rtpProfile, ip, connIP, rtpPort, mode, codecs, localSDES, dtlsSet, iceSet)
}

// RemoteSDP applies remote SDP.
// NOTE: It must called ONCE or single thread while negotiation happening.
// For multi negotiation Fork Must be called before
func (s *MediaSession) RemoteSDP(sdpReceived []byte) error {
	sd := sdp.SessionDescription{}
	if err := sdp.Unmarshal(sdpReceived, &sd); err != nil {
		return fmt.Errorf("fail to parse received SDP: %w", err)
	}

	si, err := sd.SessionInformation()
	if err != nil {
		return err
	}

	// 	the origin line MUST
	//    be different in the answer, since the answer is generated by a
	//    different entity.  In that case, the version number in the "o=" line
	//    of the answer is unrelated to the version number in the o line of the
	//    offer.
	// We are the answerer whenever the body being applied is an offer. The caller
	// tells us which it is, because it cannot be inferred from the session:
	// s.sessionID == 0 holds both for a UAS applying its first offer and for a
	// UAC applying the answer to its own INVITE, and Fork carries the session id
	// forward so a re-INVITE offer no longer looks like a first offer either.
	answerer := !s.RemoteSDPIsAnswer
	if s.sessionID != si.SessionID {
		s.sessionID = si.SessionID
		s.sessionVersion = si.SessionVersion + 1
	}
	// TODO: check below. For now we expect and handle only single audio media
	// For each "m=" line in the offer, there MUST be a corresponding "m="
	//    line in the answer.  The answer MUST contain exactly the same number
	//    of "m=" lines as the offer.
	md, err := sd.MediaDescription("audio")
	if err != nil {
		return err
	}

	// Confirm it is supported profile
	secureRequest := false
	switch md.Proto {
	case "RTP/AVP":
	case "RTP/SAVP":
		secureRequest = true
	case "UDP/TLS/RTP/SAVP":
	case "UDP/TLS/RTP/SAVPF":
		// secureRequest = true
	default:
		return fmt.Errorf("%w: unsupported media description protocol proto=%s", ErrNoCommonMedia, md.Proto)
	}
	s.remoteProto = md.Proto

	codecs := make([]Codec, len(md.Formats))
	attrs := sd.Values("a")
	n, err := CodecsFromSDPRead(md.Formats, attrs, codecs)
	if err != nil {
		if n == 0 {
			// Nothing parsed, break
			return err
		}

		return fmt.Errorf("reading codecs from SDP was not full: %w", err)
	}
	if n == 0 {
		return fmt.Errorf("%w: no codecs found in SDP", ErrNoCommonCodec)
	}

	// https://datatracker.ietf.org/doc/html/rfc3264#section-6.1
	// 	Although the answerer MAY list the formats in their desired order of
	//    preference, it is RECOMMENDED that unless there is a specific reason,
	//    the answerer list formats in the same relative order they were
	//    present in the offer.  In other words, if a stream in the offer lists
	//    audio codecs 8, 22 and 48, in that order, and the answerer only
	//    supports codecs 8 and 48, it is RECOMMENDED that, if the answerer has
	// 	  no reason to change it, the ordering of codecs in the answer be 8,
	//    48, and not 48, 8.  This helps assure that the same codec is used in
	//    both directions.
	if s.updateRemoteCodecs(codecs[:n], answerer) == 0 {
		return fmt.Errorf("%w: no supported codecs found", ErrNoCommonCodec)
	}

	ci, err := sd.ConnectionInformation()
	if err != nil {
		return err
	}

	// Refuse an RTP destination that cannot carry media instead of latching it.
	//
	// Port 0 is not a parse accident, it has a meaning: the stream is rejected
	// (RFC 3264 section 6). It also absorbs every unparseable port, because
	// MediaDescription discards strconv's error and leaves the zero value. Both
	// readings agree there is no audio to carry. A nil IP arrives the same way:
	// ConnectionInformation only validates the address for the IP4 and IP6
	// addrtypes, so any other addrtype parses clean and yields no IP at all.
	// Accepting either built a session pointed at nothing, which fails silently
	// on first write, so the call answered and then sat mute.
	if md.Port <= 0 || md.Port > 65535 {
		return fmt.Errorf("%w: audio media port is not usable port=%d", ErrNoCommonMedia, md.Port)
	}
	if ci.IP == nil {
		return fmt.Errorf("%w: audio connection address is not an IP addrtype=%s", ErrNoCommonMedia, ci.AddressType)
	}

	s.SetRemoteAddr(&net.UDPAddr{IP: ci.IP, Port: md.Port})

	// Check mode for media direction
	mode := sd.MediaDirection()
	s.mode = negotiateMediaDirection(mode, s.Mode)

	// Check for SDES
	for _, v := range attrs {
		if strings.HasPrefix(v, "crypto:") {
			vals := strings.Split(v, " ")
			if len(vals) < 3 {
				return fmt.Errorf("sdp: bad crypto attribute attr=%q", v)
			}
			// Parse crypto tag
			tagString := strings.TrimPrefix(vals[0], "crypto:")
			if s.srtpRemoteTag, err = strconv.Atoi(tagString); err != nil {
				return fmt.Errorf("bad crypto tag in %q", v)
			}

			// Parse algorithm
			alg := vals[1]
			profile := srtpProfileParse(alg)
			if profile == 0 {
				continue
			}

			// When this gets into array, we need to check do we want to support it
			if s.SRTPAlg != uint16(profile) {
				continue
			}

			inline := strings.TrimPrefix(vals[2], "inline:")

			keyBytes, err := base64.StdEncoding.DecodeString(inline)
			if err != nil {
				return fmt.Errorf("failed to decode SDES key: %v", err)
			}
			if len(keyBytes) != 30 {
				return fmt.Errorf("expected 30-byte key, got %d", len(keyBytes))
			}
			// Split into master key (16 bytes) and master salt (14 bytes)
			masterKey := keyBytes[:16]
			masterSalt := keyBytes[16:]

			ctx, err := srtp.CreateContext(masterKey, masterSalt, profile)
			if err != nil {
				return fmt.Errorf("CreateContext failed: %v", err)
			}
			s.remoteCtxSRTP = ctx

			break
		}

	}

	if s.iceEnabled() {
		if err := s.remoteICE(attrs); err != nil {
			return err
		}
	}

	// Check for DTLS
	if len(s.DTLSConf.Certificates) > 0 || s.SecureRTP == 2 {
		setup := ""
		fingerprints := make([]sdpFingerprints, 0, 1) // at least must be 1
		for _, v := range attrs {
			if strings.HasPrefix(v, "setup:") {
				setup = strings.TrimSpace(v[len("setup:"):])
				continue
			}

			// This may only needed to be done when answering call
			if strings.HasPrefix(v, "fingerprint:") {
				vals := strings.Split(v[len("fingerprint:"):], " ")
				if len(vals) < 2 {
					return fmt.Errorf("sdp: bad fingerprint attribute attr=%q", v)
				}
				alg := vals[0]
				fp := vals[1]
				// TODO fingerprint validation
				fingerprints = append(fingerprints, sdpFingerprints{
					alg:         alg,
					fingerprint: fp,
				})
			}
		}

		if setup == "" {
			return fmt.Errorf("Empty a=setup value attribute for dtls")
		}

		// THIS may need external or after SIP ACK establishment
		dtlsConf := s.DTLSConf.ToLibConf(fingerprints)
		role := "client"
		switch setup {
		case "actpass", "passive":
			// we are client, as remote wants to be server
		case "active":
			// we are server as remote wants to be client
			role = "server"

			// NOTE: setup:active allows the answer and the DTLS handshake to occur in parallel.
		default:
			return fmt.Errorf("unknown setup value %q", setup)
		}

		// dialDTLS builds the DTLS conn over the media transport.
		dialDTLS := func() error {
			var err error
			s.dtlsTr = s.dtlsTransport()
			if role == "server" {
				s.dtlsConn, err = dtls.Server(s.dtlsTr, &s.Raddr, dtlsConf)
			} else {
				s.dtlsConn, err = dtls.Client(s.dtlsTr, &s.Raddr, dtlsConf)
			}
			if err != nil {
				return fmt.Errorf("failed to setup dlts %s conn: %w", role, err)
			}
			return nil
		}

		// Without ICE the transport already exists, so the conn is built now:
		// a server conn must be able to buffer a ClientHello that arrives
		// between our answer and Finalize. An ICE session has no transport
		// until connectivity checks nominate a pair, so it defers this.
		if !s.iceEnabled() {
			if err := dialDTLS(); err != nil {
				return err
			}
		}

		s.onFinalize = func() error {
			if s.iceEnabled() {
				if err := s.startICE(); err != nil {
					return err
				}
				if err := dialDTLS(); err != nil {
					return err
				}
			}

			DefaultLogger().Debug("Starting dtls handshake",
				"setup", setup,
				"role", role,
				"laddr", s.dtlsConn.LocalAddr().String(),
				"raddr", s.dtlsConn.RemoteAddr().String(),
			)
			if err := s.dtlsConn.Handshake(); err != nil {
				return fmt.Errorf("dtls conn handshake: %w", err)
			}

			// The exchange is over, so the stack must let the socket go before it
			// can take a single SRTP packet off it. This is the first statement
			// after the handshake for that reason: nothing may come between.
			if err := s.retireDTLS(); err != nil {
				return err
			}

			DefaultLogger().Debug("Handshake finished. Checking DTLS State")

			state, ok := s.dtlsConn.ConnectionState()
			if !ok {
				return fmt.Errorf("failed to get dtls client state")
			}

			// Setup now SRTP for encryption
			prof, _ := s.dtlsConn.SelectedSRTPProtectionProfile()
			p := srtp.ProtectionProfile(prof)
			masterKeyLen, err := p.KeyLen()
			if err != nil {
				return fmt.Errorf("dtls - failed to get master keylen: %w", err)
			}
			masterSaltLen, err := p.SaltLen()
			if err != nil {
				return fmt.Errorf("dtls - failed to get master saltlen: %w", err)
			}
			keyingMaterial, err := state.ExportKeyingMaterial("EXTRACTOR-dtls_srtp", nil, 2*(masterKeyLen+masterSaltLen))
			if err != nil {
				return fmt.Errorf("dtls - failed to export keying material: %w", err)
			}

			clientKey := keyingMaterial[:masterKeyLen]
			serverKey := keyingMaterial[masterKeyLen : 2*masterKeyLen]
			clientSalt := keyingMaterial[2*masterKeyLen : 2*masterKeyLen+masterSaltLen]
			serverSalt := keyingMaterial[2*masterKeyLen+masterSaltLen:]

			if role == "server" {
				// Change order
				clientKey, serverKey = serverKey, clientKey
				clientSalt, serverSalt = serverSalt, clientSalt
			}

			s.localCtxSRTP, err = srtp.CreateContext(clientKey, clientSalt, p)
			if err != nil {
				return fmt.Errorf("failed to create SRTP context: %w", err)
			}

			// s.localCtxSRTP, err = srtp.CreateContext(serverKey, serverSalt, p)
			s.remoteCtxSRTP, err = srtp.CreateContext(serverKey, serverSalt, p)
			if err != nil {
				return fmt.Errorf("failed to create SRTP context: %w", err)
			}

			if s.localCtxSRTP == nil && s.remoteCtxSRTP == nil {
				panic("no context setup")
			}

			DefaultLogger().Debug("DTLS SRTP setuped")
			return nil
		}
	}

	// Offerer path: we called LocalSDP() before RemoteSDP(), so localCtxSRTP
	// may already be set. If the remote answer has no crypto, clear it to
	// avoid sending encrypted RTP to a peer expecting plaintext.
	if s.localCtxSRTP != nil && s.remoteCtxSRTP == nil && !secureRequest {
		s.localCtxSRTP = nil
	}

	if secureRequest && s.remoteCtxSRTP == nil {
		return fmt.Errorf("%w: remote requested secure RTP, but no context is created proto=%s", ErrNoCommonCrypto, md.Proto)
	}
	return nil
}

// remoteICE reads the remote ICE credentials and candidates out of the SDP
// attributes and feeds them to the agent. Connectivity checks are not started
// here: they belong to Finalize, once the offer/answer exchange is complete.
func (s *MediaSession) remoteICE(attrs []string) error {
	// Tracked separately from s.rtcpMux: that one records our own intent and
	// listenICE has already set it for every ICE session, so it says nothing
	// about what the remote agreed to.
	remoteRTCPMux := false

	for _, v := range attrs {
		switch {
		case strings.HasPrefix(v, "ice-ufrag:"):
			s.remoteICEUfrag = strings.TrimSpace(v[len("ice-ufrag:"):])
		case strings.HasPrefix(v, "ice-pwd:"):
			s.remoteICEPwd = strings.TrimSpace(v[len("ice-pwd:"):])
		case strings.HasPrefix(v, "candidate:"):
			if err := s.iceAgent.AddRemoteCandidate(strings.TrimSpace(v)); err != nil {
				// One malformed candidate must not fail the session: ICE can
				// still nominate a pair from the others.
				DefaultLogger().Warn("Skipping bad ICE candidate", "attr", v, "error", err)
			}
		case v == "rtcp-mux":
			remoteRTCPMux = true
		}
	}

	if s.remoteICEUfrag == "" || s.remoteICEPwd == "" {
		return fmt.Errorf("sdp: ICE enabled but remote sent no ice-ufrag/ice-pwd")
	}

	if !remoteRTCPMux {
		// An ICE session has a single nominated pair and therefore no second
		// port to put RTCP on. A peer that refuses rtcp-mux cannot be served.
		return fmt.Errorf("sdp: ICE requires rtcp-mux, remote did not offer it")
	}

	s.iceAgent.SetRemoteCredentials(s.remoteICEUfrag, s.remoteICEPwd)
	return nil
}

// startICE runs connectivity checks and installs the nominated pair as the
// session transport.
//
// The offerer is the controlling agent (RFC 8445 section 6.1). Once a pair is
// nominated, rtpConn and rtcpConn become views on the one ICE connection, so
// the rest of MediaSession reads and writes over ICE without further changes.
func (s *MediaSession) startICE() error {
	ctx, cancel := context.WithTimeout(context.Background(), ICEConnectTimeout)
	defer cancel()

	controlling := s.dtlsEndpointRole() == DTLSEndpointRoleOfferer
	conn, raddr, err := s.iceAgent.Connect(ctx, controlling)
	if err != nil {
		return err
	}

	s.SetRemoteAddr(raddr)
	// SetRemoteAddr puts RTCP on port+1. Under rtcp-mux it shares the RTP
	// address, and with ICE that is the only address the pair accepts.
	s.rtcpRaddr = *raddr

	s.iceMux = newICEMux(conn, raddr)
	s.rtpConn = s.iceMux.rtp
	s.rtcpConn = s.iceMux.rtcp
	return nil
}

// dtlsTransport returns the packet conn the DTLS handshake runs over. Without
// ICE that is the RTP socket, which is what carries SRTP afterwards. With ICE
// it is the DTLS stream of the mux, since the handshake shares the nominated
// pair with media and has to be separated from it.
//
// Either way the conn is only lent to the DTLS stack: see dtlsKeyExchangeConn.
func (s *MediaSession) dtlsTransport() *dtlsKeyExchangeConn {
	if s.iceMux != nil {
		return newDTLSKeyExchangeConn(s.iceMux.dtls)
	}
	return newDTLSKeyExchangeConn(s.rtpConn)
}

// retireDTLS releases the media socket once the handshake is done with it.
//
// DTLS-SRTP uses the association for nothing but the key exchange, and the
// socket it ran on is the one SRTP arrives on. Left alone the stack keeps its
// read loop parked there and consumes media, which it then discards as a
// malformed record per RFC 6347 section 4.1.2.7, so the loss is silent.
//
// detach comes first, so that the close_notify Close emits is dropped rather
// than sent: the peer keeps its association. Close is then what retires the
// read loop and the handshake goroutine, and it cannot reach the socket because
// detach has already taken it away.
func (s *MediaSession) retireDTLS() error {
	s.dtlsTr.detach()
	if err := s.dtlsConn.Close(); err != nil {
		return fmt.Errorf("dtls conn close: %w", err)
	}
	return nil
}

// Finalize finalizes negotiation and does verification
// Should be called only after exchage of SDP is done
func (s *MediaSession) Finalize() error {
	if s.onFinalize != nil {
		err := s.onFinalize()
		s.onFinalize = nil
		return err
	}
	return nil
}

// codecMatch reports whether local and remote describe the same audio format.
//
// The payload type is deliberately not compared. For dynamic formats each side
// picks its own number from 96-127 (RFC 3551 section 3), so comparing numbers
// drops every format the peer happens to number differently. What identifies a
// format is its rtpmap: encoding name, clock rate and channel count. Encoding
// names are case insensitive (RFC 4566 section 6).
//
// SampleDur is not compared either: it is ptime, a framing preference rather
// than part of the format's identity, and the local value is the one we keep.
func codecMatch(local Codec, remote Codec) bool {
	return strings.EqualFold(local.Name, remote.Name) &&
		local.SampleRate == remote.SampleRate &&
		local.NumChannels == remote.NumChannels
}

// negotiatedCodec builds the entry for a matched pair. It keeps our local
// framing but takes the peer's payload type: RFC 3264 section 6.1 says that if a
// codec was referenced with a specific payload type number in the offer, that
// same number should be used for it in the answer.
//
// This is the only place the peer's number is recorded, so it is what the RTP
// paths must gate on. They reach it through activeCodecs.
func negotiatedCodec(local Codec, remote Codec) Codec {
	local.PayloadType = remote.PayloadType
	return local
}

func (s *MediaSession) updateRemoteCodecs(codecs []Codec, answerer bool) int {
	if len(s.Codecs) == 0 {
		s.Codecs = codecs
		return len(codecs)
	}

	DefaultLogger().Debug("Remote Codecs Update", "local", s.Codecs, "remote", codecs, "answerer", answerer)

	// Some systems may like to answer with local order of preference
	if SDPCodecPreferLocalOrder > 0 && answerer {
		filter := make([]Codec, 0, len(codecs))
		for _, lc := range s.Codecs {
			for _, rc := range codecs {
				if codecMatch(lc, rc) {
					filter = append(filter, negotiatedCodec(lc, rc))
					break
				}
			}
		}
		s.filterCodecs = filter
		return len(s.filterCodecs)
	}

	// NOTE: codecs is not reused as the filter buffer. The negotiated entry is
	// built from the local codec, so writing it back over the remote list would
	// corrupt entries this loop has not read yet.
	filter := make([]Codec, 0, len(codecs))
	for _, rc := range codecs {
		for _, lc := range s.Codecs {
			if codecMatch(lc, rc) {
				filter = append(filter, negotiatedCodec(lc, rc))
				break
			}
		}
	}
	s.filterCodecs = filter
	return len(s.filterCodecs)
}

// CommonCodecs returns common codecs if negotiation is finished, that is Local and Remote SDP are exchanged
// NOTE: Not thread safe, should be called after negotiation Only!
func (s *MediaSession) CommonCodecs() []Codec {
	return s.filterCodecs
}

// activeCodecs returns the codecs this session runs on: what negotiation settled
// on once it has, and the local list until then.
//
// The distinction matters for dynamic formats, whose payload type is the peer's
// number rather than ours (RFC 3264 section 6.1). Only the negotiated entries
// carry that number, so the local list can not answer what is on the wire.
//
// The fallback is not just for the offer: Fork clones Codecs but deliberately
// not filterCodecs, so a re-negotiating session passes through here with nothing
// negotiated yet and must keep running on its local list.
//
// NOTE: Not thread safe. Negotiation writes filterCodecs, so this must not be
// called in parallel with RemoteSDP.
func (s *MediaSession) activeCodecs() []Codec {
	if len(s.filterCodecs) > 0 {
		return s.filterCodecs
	}
	return s.Codecs
}

// Listen creates listeners instead
func (s *MediaSession) createListeners(laddr *net.UDPAddr) error {
	// var err error

	if laddr.Port != 0 {
		return s.listen(laddr)
	}

	if laddr.Port == 0 && RTPPortStart > 0 && RTPPortEnd > RTPPortStart {
		// Get next available port
		port := RTPPortStart + int(rtpPortOffset.Load())
		var err error
		for ; port < RTPPortEnd; port += 2 {
			laddr.Port = port
			err = s.listen(laddr)
			if err == nil {
				break
			}
		}
		if err != nil {
			return fmt.Errorf("no available ports in range %d:%d: %w", RTPPortStart, RTPPortEnd, err)
		}
		// Add some offset so that we use more from range
		offset := (port + 2 - RTPPortStart) % (RTPPortEnd - RTPPortStart)
		rtpPortOffset.Store(int32(offset)) // Reset to zero with module
		return nil
	}

	// Because we want to go +2 with ports in racy situations this will always fail
	// So we need to add some control and retry if needed
	// We are always in race with other services so only try to offset to reduce retries
	var err error
	for retries := 0; retries < 10; retries += 1 {
		err = s.listen(laddr)
		if err == nil {
			break
		}
	}

	return err
}

// listen binds the session sockets. It is the per attempt step of
// createListeners, which retries it across a port range.
func (s *MediaSession) listen(laddr *net.UDPAddr) error {
	if s.iceEnabled() {
		return s.listenICE(laddr)
	}
	return s.listenRTPandRTCP(laddr)
}

// listenICE binds the single socket an ICE session uses.
//
// ICE nominates one candidate pair, so RTP, RTCP and the DTLS handshake all
// share one port. That makes rtcp-mux (RFC 5761) mandatory rather than
// optional, and it is why no RTCP socket is bound here. The socket is only
// held on iceUDPConn until the agent's UDP mux takes ownership of it; the
// agent gathers its host candidate from this very socket, which is what keeps
// the SDP m=audio port and the host candidate port identical.
func (s *MediaSession) listenICE(laddr *net.UDPAddr) error {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: laddr.IP, Port: laddr.Port})
	if err != nil {
		return err
	}
	s.iceUDPConn = conn
	// Update laddr as it can be empheral
	s.Laddr = *conn.LocalAddr().(*net.UDPAddr)
	s.rtcpMux = true
	return nil
}

func (s *MediaSession) listenRTPandRTCP(laddr *net.UDPAddr) error {
	var err error
	s.rtpConn, err = net.ListenUDP("udp", &net.UDPAddr{IP: laddr.IP, Port: laddr.Port})
	if err != nil {
		return err
	}
	laddr = s.rtpConn.LocalAddr().(*net.UDPAddr)

	s.rtcpConn, err = net.ListenUDP("udp", &net.UDPAddr{IP: laddr.IP, Port: laddr.Port + 1})
	if err != nil {
		s.rtpConn.Close()
		return err
	}

	// Update laddr as it can be empheral
	s.Laddr = *laddr
	return nil
}

// ReadRTP reads data from network and parses to pkt
// buffer is passed in order to avoid extra allocs
func (m *MediaSession) ReadRTP(buf []byte, pkt *rtp.Packet) (int, error) {
	if len(buf) < RTPBufSize {
		return 0, io.ErrShortBuffer
	}

	n, from, err := m.rtpConn.ReadFrom(buf)
	if err != nil {
		return 0, err
	}

	if m.remoteCtxSRTP != nil {
		decrypted, err := m.remoteCtxSRTP.DecryptRTP(buf, buf[:n], &pkt.Header)
		if err != nil {
			return n, fmt.Errorf("Read SRTP Decrypt error: %w", err)
		}
		if len(decrypted) > len(buf) {
			DefaultLogger().Warn("Growing Decrypted RTP buffer", "diff", len(decrypted)-len(buf))
		}

		buf = decrypted
		n = len(decrypted)

		// NOTE this is optimiation to avoid double unmarshaling RTP header
		if err := rtpUnmarshalPayload(buf, pkt); err != nil {
			return n, fmt.Errorf("rtp unmarshal failed: %w", err)
		}
	} else {
		if err := RTPUnmarshal(buf[:n], pkt); err != nil {
			return n, err
		}
	}

	logRTPRead(m, from, pkt)
	m.ReadRTPFromAddr = from

	// Handle NAT
	if m.RTPNAT == 1 && from.String() != m.Raddr.String() {
		// Moving this to RTP session could have simplify validation (sequence tracking), but for now here is more easier to maintain
		func() {
			// Make sure it is valid pkt
			if pkt.Version != 2 {
				return
			}

			fromAddr, ok := from.(*net.UDPAddr)
			if !ok {
				return
			}

			if m.learnedRTPFrom.CompareAndSwap(nil, fromAddr) {
				// Allow only first swap, rest is ignored
				DefaultLogger().Debug("RTP NAT switch to new source", "addr", from.String())
			}
		}()
	}

	if m.mode == sdp.ModeSendonly {
		// We allow parsing of pkt but we indicate that this pkt should not be consumed
		return 0, nil
	}

	return n, err
}

// return parsed rtp
func (m *MediaSession) readRTPParsed() (rtp.Packet, error) {
	p := rtp.Packet{}

	buf := make([]byte, 1600)

	n, err := m.ReadRTPRaw(buf)
	if err != nil {
		return p, err
	}

	if err := p.Unmarshal(buf[:n]); err != nil {
		return p, err
	}

	logRTPRead(m, &m.Raddr, &p)
	return p, err
}

// Deprecated
// Will be replaced with readRTPDeadlineNoAlloc in next releases
// func (m *MediaSession) ReadRTPDeadline(t time.Time) (rtp.Packet, error) {
// 	m.rtpConn.SetReadDeadline(t)
// 	return m.ReadRTP()
// }

func (m *MediaSession) ReadRTPRaw(buf []byte) (int, error) {
	n, from, err := m.rtpConn.ReadFrom(buf)

	if m.RTPNAT == 1 {
		addr, _ := from.(*net.UDPAddr)
		m.learnedRTPFrom.Store(addr)
	}
	return n, err
}

func (m *MediaSession) ReadRTPRawDeadline(buf []byte, t time.Time) (int, error) {
	m.rtpConn.SetReadDeadline(t)
	return m.ReadRTPRaw(buf)
}

// ReadRTCP is optimized reads and unmarshals RTCP packets. Buffers is only used for unmarshaling.
// Caller needs to be aware of size this buffer and allign with MTU
func (m *MediaSession) ReadRTCP(buf []byte, pkts []rtcp.Packet) (n int, err error) {
	nn, from, err := m.ReadRTCPRaw(buf)
	if err != nil {
		return n, err
	}
	data := buf[:nn]

	if m.remoteCtxSRTP != nil {
		data, err = m.remoteCtxSRTP.DecryptRTCP(data, data, nil)
		if err != nil && false {
			// For some unknown cases Decryption could fail
			return 0, errors.Join(errRTCPFailedToUnmarshal, err)
		}
	}

	n, err = RTCPUnmarshal(data, pkts)
	if err != nil {
		return 0, err
	}

	if m.RTPNAT == 1 && from.String() != m.rtcpRaddr.String() {
		func() {
			fromAddr, ok := from.(*net.UDPAddr)
			if !ok {
				return
			}
			rtcpA := m.learnedRTCPFrom.Load()
			if rtcpA != nil {
				// learned
				return
			}
			rtpA := m.learnedRTPFrom.Load()

			// Switch RTCP if RTP switched?
			if rtpA != nil {
				DefaultLogger().Debug("RTCP New Source Learned", "addr", from.String())
				m.learnedRTCPFrom.Store(fromAddr)
			}
		}()
	}

	logRTCPRead(m, pkts[:n])
	return n, err
}

func (m *MediaSession) ReadRTCPRaw(buf []byte) (int, net.Addr, error) {
	if m.rtcpConn == nil {
		// just block
		return 0, nil, fmt.Errorf("no connection present")
	}
	n, a, err := m.rtcpConn.ReadFrom(buf)
	return n, a, err
}

func (m *MediaSession) ReadRTCPRawDeadline(buf []byte, t time.Time) (int, error) {
	if m.rtcpConn == nil {
		// just block
		return 0, fmt.Errorf("no connection present")
	}
	n, _, err := m.rtcpConn.ReadFrom(buf)

	return n, err
}

func (m *MediaSession) WriteRTP(p *rtp.Packet) error {
	if m.mode == sdp.ModeRecvonly {
		// We block here as we would violate our media direction
		return nil
	}

	logRTPWrite(m, p)

	writeBuf := m.getWriteBuf()

	n, err := p.MarshalTo(writeBuf)
	if err != nil {
		return fmt.Errorf("failed to marshal to write RTP buf: %w", err)
	}
	data := writeBuf[:n]
	// data, err := p.Marshal()

	if m.localCtxSRTP != nil {
		data, err = m.localCtxSRTP.EncryptRTP(writeBuf, data, &p.Header)
		if err != nil {
			return fmt.Errorf("failed to encrypt written RTP: %w", err)
		}
	}

	n, err = m.WriteRTPRaw(data)
	if err != nil {
		return err
	}

	if n != len(data) {
		return io.ErrShortWrite
	}
	return nil
}

func (m *MediaSession) getWriteBuf() []byte {
	if m.writeRTPBuf == nil {
		m.writeRTPBuf = make([]byte, RTPBufSize)
	}
	return m.writeRTPBuf
}

func (m *MediaSession) WriteRTPRaw(data []byte) (n int, err error) {
	addr := &m.Raddr
	if m.RTPNAT == 1 {
		if a := m.learnedRTPFrom.Load(); a != nil {
			addr = a
		}
	}

	n, err = m.rtpConn.WriteTo(data, addr)
	return
}

func (m *MediaSession) WriteRTCP(p rtcp.Packet) error {
	logRTCPWrite(m, p)

	// TODO: rtcp library needs to support MarshalTo v2
	// https://github.com/pion/rtcp/issues/127
	data, err := p.Marshal()
	if err != nil {
		return err
	}

	if m.localCtxSRTP != nil {
		// sync pool may not be best option and needs benchmarks.
		// but as RTCP is not realtime this can reduce allocations
		// TODO: should be reusable with above marshaling
		wbuf := rtpBufPool.Get()
		defer rtpBufPool.Put(wbuf)
		writeBuf := wbuf.([]byte)

		data, err = m.localCtxSRTP.EncryptRTCP(writeBuf, data, nil)
		if err != nil {
			return err
		}
	}

	n, err := m.WriteRTCPRaw(data)
	if err != nil {
		return err
	}

	if n != len(data) {
		return io.ErrShortWrite
	}
	return nil
}

func (m *MediaSession) WriteRTCPDeadline(p rtcp.Packet, deadline time.Time) error {
	m.rtcpConn.SetWriteDeadline(deadline)
	return m.WriteRTCP(p)
}

// Use this to write Multi RTCP packets if they can fit in MTU=1500
func (m *MediaSession) WriteRTCPs(pkts []rtcp.Packet) error {
	data, err := rtcpMarshal(pkts)
	if err != nil {
		return err
	}

	n, err := m.WriteRTCPRaw(data)
	if err != nil {
		return err
	}

	if n != len(data) {
		return io.ErrShortWrite
	}
	return nil
}

func (m *MediaSession) WriteRTCPRaw(data []byte) (int, error) {
	addr := &m.rtcpRaddr
	if m.RTPNAT == 1 {
		if a := m.learnedRTCPFrom.Load(); a != nil {
			addr = a
		}
	}

	n, err := m.rtcpConn.WriteTo(data, addr)
	return n, err
}

func StringRTCP(p rtcp.Packet) string {

	switch r := p.(type) {
	case *rtcp.SenderReport:
		h := r.Header()
		out := fmt.Sprintf("SenderReport from %x\n", r.SSRC)
		out += fmt.Sprintf("\tHeader: Count:%d Length:%d Type:%d\n", h.Count, h.Length, h.Type)
		out += fmt.Sprintf("\tNTPTime:\t%d\n", r.NTPTime)
		out += fmt.Sprintf("\tRTPTIme:\t%d\n", r.RTPTime)
		out += fmt.Sprintf("\tPacketCount:\t%d\n", r.PacketCount)
		out += fmt.Sprintf("\tOctetCount:\t%d\n", r.OctetCount)

		for _, i := range r.Reports {
			out += fmt.Sprintf("\tSSRC: %x Lost: %d/%d  LastSeq: %d LSR: %d.%d DLSR: %d\n", i.SSRC, i.FractionLost, i.TotalLost, i.LastSequenceNumber, i.LastSenderReport&0xFFFF0000, i.LastSenderReport&0x0000FFFF, i.Delay)
		}
		out += fmt.Sprintf("\tProfile Extension Data: %v\n", r.ProfileExtensions)

		return out
	case *rtcp.ReceiverReport:
		h := r.Header()
		out := fmt.Sprintf("ReceiverReport from %x\n", r.SSRC)
		out += fmt.Sprintf("\tHeader: Count:%d Length:%d Type:%d\n", h.Count, h.Length, h.Type)
		for _, i := range r.Reports {
			out += fmt.Sprintf("SSRC: %x\tLost: %d/%d\t LastSeq: %d\tLSR: %d.%d\tDLSR: %d\n", i.SSRC, i.FractionLost, i.TotalLost, i.LastSequenceNumber, i.LastSenderReport&0xFFFF0000, i.LastSenderReport&0x0000FFFF, i.Delay)
		}
		out += fmt.Sprintf("\tProfile Extension Data: %v\n", r.ProfileExtensions)
		return out
	}

	if s, ok := p.(fmt.Stringer); ok {
		return s.String()
	}
	return "can not stringify"
}

type sdesInline struct {
	alg    string
	base64 string
	tag    int
}

type sdpFingerprints struct {
	fingerprint string
	alg         string
}

type dtlsSetup struct {
	setup        string
	fingerprints []sdpFingerprints
}

type iceSetup struct {
	ufrag string
	pwd   string
	// candidates are rendered a=candidate values, without the "a=" prefix
	candidates []string
	rtcpMux    bool
}

// formatRTPMap renders the a=rtpmap line describing codec, followed by its
// a=fmtp line where the format defines one.
//
// Dynamic formats are keyed by encoding name rather than by payload type. Each
// side picks its own number from 96-127 for them (RFC 3551 section 3) and an
// answer echoes the number the offerer used (RFC 3264 section 6.1), so a
// negotiated codec arrives here carrying the peer's number. What identifies the
// format is the encoding name (RFC 4855 section 3), and only the number is
// rendered from the codec. Keying on the number instead described a peer's
// telephone-event at 96 as opus, and dropped opus' fmtp at every number but 96.
//
// Static formats stay keyed by number: the RTP/AVP registry freezes those
// assignments (RFC 3551 section 6) and they can not be renumbered. They are
// checked after the dynamic names so that a peer mapping a dynamic format onto a
// static number is still described by what it is.
func formatRTPMap(codec Codec) []string {
	switch {
	case strings.EqualFold(codec.Name, CodecAudioOpus.Name):
		return []string{
			// RFC 7587 section 7 fixes the rtpmap clock at 48000 and the encoding
			// parameters at 2 for opus, whatever the stream actually carries. It
			// declares the decoder's capability, not that the stream is stereo.
			fmt.Sprintf("a=rtpmap:%d opus/48000/2", codec.PayloadType),
			// Providing 0 when FEC cannot be used on the receiving side is RECOMMENDED.
			// https://datatracker.ietf.org/doc/html/rfc7587
			fmt.Sprintf("a=fmtp:%d useinbandfec=0", codec.PayloadType),
		}
	case strings.EqualFold(codec.Name, CodecTelephoneEvent8000.Name):
		return []string{
			// The canonical form carries no encoding parameters suffix, which the
			// generic default below would append.
			fmt.Sprintf("a=rtpmap:%d telephone-event/8000", codec.PayloadType),
			// Events 0-16 are the DTMF digits, * and # (RFC 4733 section 3.2).
			fmt.Sprintf("a=fmtp:%d 0-16", codec.PayloadType),
		}
	}

	switch codec.PayloadType {
	case CodecAudioUlaw.PayloadType:
		return []string{"a=rtpmap:0 PCMU/8000"}
	case CodecAudioAlaw.PayloadType:
		return []string{"a=rtpmap:8 PCMA/8000"}
	case CodecAudioG722.PayloadType:
		// Clock is 8000 per RFC 3551 and the encoding parameters suffix is
		// omitted. The generic default below would append the channel count and
		// some peers do not match the non canonical form.
		return []string{"a=rtpmap:9 G722/8000"}
	}

	return []string{fmt.Sprintf("a=rtpmap:%d %s/%d/%d", codec.PayloadType, codec.Name, codec.SampleRate, codec.NumChannels)}
}

func generateSDPForAudio(sessionID uint64, sessionVersion uint64, rtpProfile string, originIP net.IP, connectionIP net.IP, rtpPort int, mode string, codecs []Codec, sdes sdesInline, dtlsSet *dtlsSetup, iceSet *iceSetup) []byte {
	// ntpTime := GetCurrentNTPTimestamp()

	fmts := make([]string, len(codecs))
	formatsMap := []string{}
	for i, f := range codecs {
		formatsMap = append(formatsMap, formatRTPMap(f)...)
		fmts[i] = strconv.Itoa(int(f.PayloadType))
	}

	// Support only ulaw and alaw
	// TODO optimize this with string builder
	s := []string{
		"v=0",
		fmt.Sprintf("o=- %d %d IN %s %s", sessionID, sessionVersion, sdpIP(originIP), originIP),
		"s=Sip Go Media",
		// "b=AS:84",
		fmt.Sprintf("c=IN %s %s", sdpIP(connectionIP), connectionIP),
		"t=0 0",
		fmt.Sprintf("m=audio %d %s %s", rtpPort, rtpProfile, strings.Join(fmts, " ")),
	}

	s = append(s, formatsMap...)
	s = append(s,
		"a=ptime:20", // Needed for opus
		"a=maxptime:20",
		"a="+string(mode))

	if sdes.alg != "" {
		s = append(s, fmt.Sprintf("a=crypto:%d %s inline:%s", sdes.tag, sdes.alg, sdes.base64))
	}

	if dtlsSet != nil {
		fingerprints := dtlsSet.fingerprints
		dtlsSetup := dtlsSet.setup

		s = append(s, "a=setup:"+dtlsSetup)
		s = append(s, "a=connection:new") // Cane be new or existing. Marks it needs new transport
		for _, d := range fingerprints {
			if d.fingerprint == "" {
				continue
			}
			s = append(s, fmt.Sprintf("a=fingerprint:%s %s", d.alg, d.fingerprint))
		}
	}

	if iceSet != nil {
		s = append(s, "a=ice-ufrag:"+iceSet.ufrag)
		s = append(s, "a=ice-pwd:"+iceSet.pwd)
		for _, c := range iceSet.candidates {
			s = append(s, "a="+c)
		}
		if iceSet.rtcpMux {
			// An ICE session has one nominated pair, so RTCP has no port of
			// its own to advertise.
			s = append(s, "a=rtcp-mux")
		}
	}

	// s := []string{
	// 	"v=0",
	// 	fmt.Sprintf("o=- %d %d IN IP4 %s", ntpTime, ntpTime, originIP),
	// 	"s=Sip Go Media",
	// 	// "b=AS:84",
	// 	fmt.Sprintf("c=IN IP4 %s", connectionIP),
	// 	"t=0 0",
	// 	fmt.Sprintf("m=audio %d RTP/AVP 96 97 98 99 3 0 8 9 120 121 122", rtpPort),
	// 	"a=" + string(mode),
	// 	"a=rtpmap:96 speex/16000",
	// 	"a=rtpmap:97 speex/8000",
	// 	"a=rtpmap:98 speex/32000",
	// 	"a=rtpmap:99 iLBC/8000",
	// 	"a=fmtp:99 mode=30",
	// 	"a=rtpmap:120 telephone-event/16000",
	// 	"a=fmtp:120 0-16",
	// 	"a=rtpmap:121 telephone-event/8000",
	// 	"a=fmtp:121 0-16",
	// 	"a=rtpmap:122 telephone-event/32000",
	// 	"a=rtcp-mux",
	// 	fmt.Sprintf("a=rtcp:%d IN IP4 %s", rtpPort+1, connectionIP),
	// }

	res := strings.Join(s, "\r\n") + "\r\n"
	return []byte(res)
}

func generateMasterKeySalt(profile srtp.ProtectionProfile) ([]byte, int, error) {
	keyLen, err := profile.KeyLen()
	if err != nil {
		return nil, 0, fmt.Errorf("srtp getting key len: %w", err)
	}

	saltLen, err := profile.SaltLen()
	if err != nil {
		return nil, 0, fmt.Errorf("srtp getting salt len: %w", err)
	}

	buf := make([]byte, keyLen+saltLen)
	if _, err := rand.Read(buf[:keyLen]); err != nil {
		return nil, 0, err
	}

	if _, err := rand.Read(buf[keyLen:]); err != nil {
		return nil, 0, err
	}
	return buf, keyLen, nil
}

func sdpIP(ip net.IP) string {
	if ip.To4() == nil {
		return "IP6"
	}
	return "IP4"
}

// negotiateMediaDirection computes our local direction based on the remote SDP offer/answer
// and our current preference. Defaults to sendrecv when nothing explicit is provided.
func negotiateMediaDirection(remoteMode, localPref string) string {
	if localPref == "" {
		localPref = sdp.ModeSendrecv
	}

	switch remoteMode {
	case "inactive":
		return "inactive"
	case sdp.ModeSendonly:
		if localPref == "inactive" {
			return "inactive"
		}
		// Offerer is sendonly, answer must be recvonly or inactive
		return sdp.ModeRecvonly
	case sdp.ModeRecvonly:
		if localPref == "inactive" {
			return "inactive"
		}
		// Offerer is recvonly, answer must be sendonly or inactive
		return sdp.ModeSendonly
	case sdp.ModeSendrecv:
		if localPref == sdp.ModeSendrecv || localPref == sdp.ModeSendonly || localPref == sdp.ModeRecvonly || localPref == "inactive" {
			return localPref
		}
		return sdp.ModeSendrecv
	default:
		return localPref
	}
}
