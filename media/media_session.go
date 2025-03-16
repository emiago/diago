// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package media

import (
	"fmt"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/emiago/diago/media/sdp"
	"github.com/emiago/sipgo/sip"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
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

		slog.Debug(fmt.Sprintf("RTP read %s < %s:\n%s", m.Laddr.String(), s, p.String()))
	}
}

func logRTPWrite(m *MediaSession, p *rtp.Packet) {
	if RTPDebug {
		slog.Debug(fmt.Sprintf("RTP write %s > %s:\n%s", m.Laddr.String(), m.Raddr.String(), p.String()))
	}
}

func logRTCPRead(m *MediaSession, pkts []rtcp.Packet) {
	if RTCPDebug {
		for _, p := range pkts {
			slog.Debug(fmt.Sprintf("RTCP read %s < %s:\n%s", m.Laddr.String(), m.Raddr.String(), StringRTCP(p)))
		}
	}
}

func logRTCPWrite(m *MediaSession, p rtcp.Packet) {
	if RTCPDebug {
		slog.Debug(fmt.Sprintf("RTCP write %s > %s:\n%s", m.Laddr.String(), m.Raddr.String(), StringRTCP(p)))
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
type MediaSession struct {
	// SDP stuff
	// TODO:
	// 1. make this list of codecs as we need to match also sample rate and ptime
	// 2. rtp session when matching incoming packet sample rate for RTCP should use this
	Codecs []Codec
	// Mode is sdp mode. Check consts sdp.ModeRecvOnly etc...
	Mode string
	// Laddr our local address which has full IP and port after media session creation
	Laddr net.UDPAddr

	// Raddr is our target remote address. Normally it is resolved by SDP parsing.
	// Checkout SetRemoteAddr
	Raddr net.UDPAddr

	// ExternalIP that should be used for building SDP
	ExternalIP net.IP

	rtpConn   net.PacketConn
	rtcpConn  net.PacketConn
	rtcpRaddr net.UDPAddr

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
		s.rtpConn.SetReadDeadline(t)
	}
	if rw&2 > 0 {
		//Write stop
		s.rtpConn.SetWriteDeadline(t)
	}
	return s.rtpConn.SetDeadline(t)
}

func (s *MediaSession) StartRTP(rw int8) error {
	if rw&1 > 0 {
		s.rtpConn.SetReadDeadline(time.Time{})
	}
	if rw&2 > 0 {
		s.rtpConn.SetWriteDeadline(time.Time{})
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
		Codecs: []Codec{
			CodecAudioUlaw, CodecAudioAlaw, CodecTelephoneEvent8000,
		},
		Mode: sdp.ModeSendrecv,
	}
	return &cp
}

func (s *MediaSession) Close() {
	if s.rtcpConn != nil {
		s.rtcpConn.Close()
	}

	if s.rtpConn != nil {
		s.rtpConn.Close()
	}
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
	// *s.rtcpRaddr = *s.Raddr
	// s.rtcpRaddr.Port++
}

func (s *MediaSession) LocalSDP() []byte {
	ip := s.Laddr.IP
	rtpPort := s.Laddr.Port
	connIP := s.ExternalIP
	if connIP == nil {
		connIP = ip
	}
	// if s.ExternalIP != nil {
	// 	ip = s.ExternalIP
	// }

	return generateSDPForAudio(ip, connIP, rtpPort, s.Mode, s.Codecs)
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
	s.Codecs = filter
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

	// If from does not match our remote
	if err := RTPUnmarshal(buf[:n], pkt); err != nil {
		return n, err
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

	n, err = RTCPUnmarshal(buf[:nn], pkts)
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

	data, err := p.Marshal()
	if err != nil {
		return err
	}

	n, err := m.WriteRTPRaw(data)
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

	data, err := p.Marshal()
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

func selectFormats(sendCodecs []string, recvCodecs []string) []int {
	formats := make([]int, 0, cap(sendCodecs))
	parseErr := []error{}
	for _, cr := range recvCodecs {
		for _, cs := range sendCodecs {
			if cr == cs {
				f, err := strconv.Atoi(cs)
				// keep going
				if err != nil {
					parseErr = append(parseErr, err)
					continue
				}
				formats = append(formats, f)
			}
		}
	}
	return formats
}

func StringRTCP(p rtcp.Packet) string {

	switch r := p.(type) {
	case *rtcp.SenderReport:
		out := fmt.Sprintf("SenderReport from %x\n", r.SSRC)
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
		out := fmt.Sprintf("ReceiverReport from %x\n", r.SSRC)
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

func generateSDPForAudio(originIP net.IP, connectionIP net.IP, rtpPort int, mode string, codecs []Codec) []byte {
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
