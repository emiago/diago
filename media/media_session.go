// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package media

import (
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
	"github.com/emiago/dtls/v3"
	"github.com/emiago/sipgo/sip"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/srtp/v3"
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
	// DTLS
	dtlsConn *dtls.Conn

	onFinalize func() error

	sessionID      uint64
	sessionVersion uint64
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
	if s.Codecs == nil || len(s.Codecs) == 0 {
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

	// Try to listen on this ports
	if err := s.createListeners(&s.Laddr); err != nil {
		return err
	}

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
		Mode:           sdp.ModeSendrecv,
		RTPNAT:         s.RTPNAT,
		sdp:            slices.Clone(s.sdp),
		sessionID:      s.sessionID,
		sessionVersion: s.sessionVersion,
		DTLSConf:       s.DTLSConf,
	}
	return &cp
}

func (s *MediaSession) Close() error {
	var e1, e2 error
	if s.rtcpConn != nil {
		e1 = s.rtcpConn.Close()
	}

	if s.rtpConn != nil {
		e2 = s.rtpConn.Close()
	}
	return errors.Join(e1, e2)
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

	// https://datatracker.ietf.org/doc/html/rfc3264#section-6.1
	// 	Although the answerer MAY list the formats in their desired order of
	//    preference, it is RECOMMENDED that unless there is a specific reason,
	//    the answerer list formats in the same relative order they were
	//    present in the offer.  In other words, if a stream in the offer lists
	//    audio codecs 8, 22 and 48, in that order, and the answerer only
	//    supports codecs 8 and 48, it is RECOMMENDED that, if the answerer has
	codecs := s.Codecs
	if len(s.filterCodecs) > 0 {
		codecs = s.filterCodecs
	}

	var localSDES sdesInline
	rtpProfile := "RTP/AVP"
	if s.SecureRTP == 1 {
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
			if !RTPProfileSAVPDisable {
				rtpProfile = "RTP/SAVP"
			}
			return nil
		}()
		if err != nil {
			DefaultLogger().Error("Failed to setup SRTP context", "error", err)
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
			//  lets be then active roll
			dtlsSet.setup = "passive"
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

	if s.sessionID == 0 {
		s.sessionID = GetCurrentNTPTimestamp()
		s.sessionVersion = s.sessionID
	} else {
		s.sessionVersion++
	}

	return generateSDPForAudio(s.sessionID, s.sessionVersion, rtpProfile, ip, connIP, rtpPort, s.Mode, codecs, localSDES, dtlsSet)
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
		return fmt.Errorf("unsupported media description protocol proto=%s", md.Proto)
	}

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
		return fmt.Errorf("no codecs found in SDP")
	}

	if s.updateRemoteCodecs(codecs[:n]) == 0 {
		return fmt.Errorf("no supported codecs found")
	}

	ci, err := sd.ConnectionInformation()
	if err != nil {
		return err
	}
	s.SetRemoteAddr(&net.UDPAddr{IP: ci.IP, Port: md.Port})

	// Check for SDES
	for _, v := range attrs {
		if strings.HasPrefix(v, "crypto:") {
			vals := strings.Split(v, " ")
			if len(vals) < 3 {
				return fmt.Errorf("sdp: bad crypto attribute attr=%q", v)
			}
			// Parse crypto tag
			tagString := strings.TrimLeft(vals[0], "crypto:")
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
			// if s.dtlsConn == nil {
			// 	panic("No dtls connection")
			// }
			s.dtlsConn, err = dtls.Client(s.rtpConn, &s.Raddr, dtlsConf)
			if err != nil {
				return fmt.Errorf("failed to setup dlts client conn: %w", err)
			}

		case "active":
			role = "server"
			// we are server as remote wants to be client
			s.dtlsConn, err = dtls.Server(s.rtpConn, &s.Raddr, dtlsConf)
			if err != nil {
				return fmt.Errorf("failed to setup dlts server conn: %w", err)
			}
			// return nil

			// NOTE: setup:active allows the answer and the DTLS handshake to occur in parallel.
		default:
			return fmt.Errorf("unknown setup value %q", setup)
		}

		s.onFinalize = func() error {
			DefaultLogger().Debug("Starting dtls handshake",
				"setup", setup,
				"role", role,
				"laddr", s.dtlsConn.LocalAddr().String(),
				"raddr", s.dtlsConn.RemoteAddr().String(),
			)
			if err := s.dtlsConn.Handshake(); err != nil {
				return fmt.Errorf("dtls conn handshake: %w", err)
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

	if secureRequest && s.remoteCtxSRTP == nil {
		return fmt.Errorf("remote requested secure RTP, but no context is created proto=%s", md.Proto)
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

func (s *MediaSession) updateRemoteCodecs(codecs []Codec) int {
	if len(s.Codecs) == 0 {
		s.Codecs = codecs
		return len(codecs)
	}

	DefaultLogger().Debug("Remote Codecs Update", "local", s.Codecs, "remote", codecs)
	filter := codecs[:0] // reuse buffer
	for _, rc := range codecs {
		for _, c := range s.Codecs {
			if c == rc {
				filter = append(filter, c)
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

// Listen creates listeners instead
func (s *MediaSession) createListeners(laddr *net.UDPAddr) error {
	// var err error

	if laddr.Port != 0 {
		return s.listenRTPandRTCP(laddr)
	}

	if laddr.Port == 0 && RTPPortStart > 0 && RTPPortEnd > RTPPortStart {
		// Get next available port
		port := RTPPortStart + int(rtpPortOffset.Load())
		var err error
		for ; port < RTPPortEnd; port += 2 {
			laddr.Port = port
			err = s.listenRTPandRTCP(laddr)
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
		err = s.listenRTPandRTCP(laddr)
		if err == nil {
			break
		}
	}

	return err
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
		headerN := pkt.Header.MarshalSize()
		if err := rtpUnmarshalPayload(headerN, buf, pkt); err != nil {
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

func generateSDPForAudio(sessionID uint64, sessionVersion uint64, rtpProfile string, originIP net.IP, connectionIP net.IP, rtpPort int, mode string, codecs []Codec, sdes sdesInline, dtlsSet *dtlsSetup) []byte {
	// ntpTime := GetCurrentNTPTimestamp()

	fmts := make([]string, len(codecs))
	formatsMap := []string{}
	for i, f := range codecs {
		// TODO should we just go generic
		switch f.PayloadType {
		case CodecAudioUlaw.PayloadType:
			formatsMap = append(formatsMap, "a=rtpmap:0 PCMU/8000")
		case CodecAudioAlaw.PayloadType:
			formatsMap = append(formatsMap, "a=rtpmap:8 PCMA/8000")
		case CodecAudioOpus.PayloadType:
			formatsMap = append(formatsMap, "a=rtpmap:96 opus/48000/2")
			// Providing 0 when FEC cannot be used on the receiving side is RECOMMENDED.
			// https://datatracker.ietf.org/doc/html/rfc7587
			formatsMap = append(formatsMap, "a=fmtp:96 useinbandfec=0")
		case CodecTelephoneEvent8000.PayloadType:
			formatsMap = append(formatsMap, "a=rtpmap:101 telephone-event/8000")
			formatsMap = append(formatsMap, "a=fmtp:101 0-16")
		default:
			s := fmt.Sprintf("a=rtpmap:%d %s/%d/%d", f.PayloadType, f.Name, f.SampleRate, f.NumChannels)
			formatsMap = append(formatsMap, s)
		}
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
