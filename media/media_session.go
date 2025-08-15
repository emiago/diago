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

	// Mode is sdp mode. Check consts sdp.ModeRecvOnly etc...
	Mode string
	// Laddr our local address which has full IP and port after media session creation
	Laddr net.UDPAddr
	// Raddr is our target remote address. Normally it is resolved by SDP parsing.
	Raddr net.UDPAddr
	// ExternalIP that should be used for building SDP
	ExternalIP net.IP

	SecureRTP int // 0 none, 1 - SDES
	// TODO support multile for offering
	SRTPAlg uint16

	// filterCodecs is common list of codecs after negotiation
	filterCodecs []Codec
	rtpConn      net.PacketConn
	rtcpConn     net.PacketConn
	rtcpRaddr    net.UDPAddr
	writeRTPBuf  []byte

	// SRTP
	localCtxSRTP  *srtp.Context
	remoteCtxSRTP *srtp.Context

	// TODO: Support RTP Symetric
	rtpSymetric bool
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
// It preserves pointer to same conneciton but rest is remobed
// After this call it still expected that
func (s *MediaSession) Fork() *MediaSession {
	cp := MediaSession{
		Laddr:    s.Laddr, // TODO clone it although it is read only
		rtpConn:  s.rtpConn,
		rtcpConn: s.rtcpConn,
		Codecs:   slices.Clone(s.Codecs),
		Mode:     sdp.ModeSendrecv,
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

func (s *MediaSession) LocalSDP() []byte {
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
			}

			ctx, err := srtp.CreateContext(masterKey, masterSalt, profile)
			if err != nil {
				return fmt.Errorf("CreateContext failed: %v", err)
			}

			s.localCtxSRTP = ctx
			return nil
		}()
		if err != nil {
			DefaultLogger().Error("Failed to setup SRTP context", "error", err)
		}
	}

	return generateSDPForAudio(ip, connIP, rtpPort, s.Mode, codecs, localSDES)
}

func (s *MediaSession) RemoteSDP(sdpReceived []byte) error {
	sd := sdp.SessionDescription{}
	if err := sdp.Unmarshal(sdpReceived, &sd); err != nil {
		return fmt.Errorf("fail to parse received SDP: %w", err)
	}

	md, err := sd.MediaDescription("audio")
	if err != nil {
		return err
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

	s.updateRemoteCodecs(codecs[:n])
	if len(s.Codecs) == 0 {
		return fmt.Errorf("no supported codecs found")
	}

	ci, err := sd.ConnectionInformation()
	if err != nil {
		return err
	}

	// Check for SDES
	for _, v := range attrs {
		if strings.HasPrefix(v, "crypto:1") {
			vals := strings.Split(v, " ")
			if len(vals) < 3 {
				return fmt.Errorf("sdp: bad crypto attribute attr=%q", v)
			}
			// Check do we support crypto alg
			alg := vals[1]

			var profile srtp.ProtectionProfile
			switch alg {
			case "AES_CM_128_HMAC_SHA1_80":
				profile = srtp.ProtectionProfileAes128CmHmacSha1_80
			// case "NULL_HMAC_SHA1_80":
			// 	profile = srtp.ProtectionProfileNullHmacSha1_80
			default:
				return fmt.Errorf("sdp: unknown crypto algorithm alg=%q", alg)
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

	s.SetRemoteAddr(&net.UDPAddr{IP: ci.IP, Port: md.Port})
	return nil
}

func (s *MediaSession) updateRemoteCodecs(codecs []Codec) {
	if len(s.Codecs) == 0 {
		s.Codecs = codecs
		return
	}

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
}

// CommonCodecs returns common codecs if negotiation is finished, that is Local and Remote SDP are exchanged
// NOTE: Not thread safe, should be called after negotiation or session must be Forked
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
			return fmt.Errorf("No available ports in range %d:%d: %w", RTPPortStart, RTPPortEnd, err)
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
		// TODO reuse pkt.Payload
		decrypted, err := m.remoteCtxSRTP.DecryptRTP(buf, buf[:n], &pkt.Header)
		if err != nil {
			return n, fmt.Errorf("srtp decrypt: %w", err)
		}
		if len(decrypted) > len(buf) {
			DefaultLogger().Warn("Growing Decrypted RTP buffer", "diff", len(decrypted)-len(buf))
		}

		buf = decrypted
		n = len(decrypted)

		// NOTE this is optimiation to avoid double unmarshaling RTP header
		headerN := pkt.Header.MarshalSize()
		if err := rtpUnmarshalPayload(headerN, buf, pkt); err != nil {
			return n, err
		}
	} else {
		if err := RTPUnmarshal(buf[:n], pkt); err != nil {
			return n, err
		}
	}

	// Problem is that this buffer is refferenced in rtp PACKET
	// if err := pkt.Unmarshal(buf[:n]); err != nil {
	// 	return err
	// }

	logRTPRead(m, from, pkt)
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
	n, _, err := m.rtpConn.ReadFrom(buf)
	return n, err
}

func (m *MediaSession) ReadRTPRawDeadline(buf []byte, t time.Time) (int, error) {
	m.rtpConn.SetReadDeadline(t)
	return m.ReadRTPRaw(buf)
}

// ReadRTCP is optimized reads and unmarshals RTCP packets. Buffers is only used for unmarshaling.
// Caller needs to be aware of size this buffer and allign with MTU
func (m *MediaSession) ReadRTCP(buf []byte, pkts []rtcp.Packet) (n int, err error) {
	nn, err := m.ReadRTCPRaw(buf)
	if err != nil {
		return n, err
	}
	data := buf[:nn]

	if m.remoteCtxSRTP != nil {
		data, err = m.remoteCtxSRTP.DecryptRTCP(data, data, nil)
		if err != nil {
			return 0, err
		}
	}

	n, err = RTCPUnmarshal(data, pkts)
	if err != nil {
		return 0, err
	}

	logRTCPRead(m, pkts[:n])
	return n, err
}

func (m *MediaSession) ReadRTCPRaw(buf []byte) (int, error) {
	if m.rtcpConn == nil {
		// just block
		select {}
	}
	n, _, err := m.rtcpConn.ReadFrom(buf)

	return n, err
}

func (m *MediaSession) ReadRTCPRawDeadline(buf []byte, t time.Time) (int, error) {
	if m.rtcpConn == nil {
		// just block
		select {}
	}
	n, _, err := m.rtcpConn.ReadFrom(buf)

	return n, err
}

func (m *MediaSession) WriteRTP(p *rtp.Packet) error {
	logRTPWrite(m, p)

	writeBuf := func() []byte {
		if m.writeRTPBuf == nil {
			m.writeRTPBuf = make([]byte, RTPBufSize)
		}
		return m.writeRTPBuf
	}()

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

func (m *MediaSession) WriteRTPRaw(data []byte) (n int, err error) {
	n, err = m.rtpConn.WriteTo(data, &m.Raddr)
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
		writeBuf := rtpBufPool.Get().([]byte)
		defer rtpBufPool.Put(writeBuf)

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
	n, err := m.rtcpConn.WriteTo(data, &m.rtcpRaddr)
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
}

func generateSDPForAudio(originIP net.IP, connectionIP net.IP, rtpPort int, mode string, codecs []Codec, sdes sdesInline) []byte {
	ntpTime := GetCurrentNTPTimestamp()

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
		fmt.Sprintf("o=- %d %d IN IP4 %s", ntpTime, ntpTime, originIP),
		"s=Sip Go Media",
		// "b=AS:84",
		fmt.Sprintf("c=IN IP4 %s", connectionIP),
		"t=0 0",
		fmt.Sprintf("m=audio %d RTP/AVP %s", rtpPort, strings.Join(fmts, " ")),
	}

	s = append(s, formatsMap...)
	s = append(s,
		"a=ptime:20", // Needed for opus
		"a=maxptime:20",
		"a="+string(mode))

	if sdes.alg != "" {
		s = append(s, fmt.Sprintf("a=crypto:1 %s inline:%s", sdes.alg, sdes.base64))
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
