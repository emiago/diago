// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package media

import (
	"net"
	"testing"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/stretchr/testify/require"
)

const streamSSRC = 0x5a219183

// followRig is an RTP session in RTPNATFollow mode and the remote addresses its media can come from.
// "old" is the address in the SDP, "new" another port on the same IP.
type followRig struct {
	t *testing.T
	s *RTPSession

	rtpPeers  map[string]*net.UDPConn
	rtcpPeers map[string]*net.UDPConn
}

func newFollowRig(t *testing.T) *followRig {
	m, err := NewMediaSession(net.IPv4(127, 0, 0, 1), 0)
	require.NoError(t, err)
	t.Cleanup(func() { m.Close() })
	m.RTPNAT = RTPNATFollow

	r := &followRig{t: t, s: NewRTPSession(m), rtpPeers: map[string]*net.UDPConn{}, rtcpPeers: map[string]*net.UDPConn{}}
	r.rtpPeers["old"] = r.listen(net.IPv4(127, 0, 0, 1))
	r.rtpPeers["new"] = r.listen(net.IPv4(127, 0, 0, 1))
	r.rtcpPeers["oldRTCP"] = r.listen(net.IPv4(127, 0, 0, 1))
	r.rtcpPeers["newRTCP"] = r.listen(net.IPv4(127, 0, 0, 1))
	m.Raddr = *r.rtpPeers["old"].LocalAddr().(*net.UDPAddr)
	m.rtcpRaddr = *r.rtcpPeers["oldRTCP"].LocalAddr().(*net.UDPAddr)
	return r
}

func (r *followRig) listen(ip net.IP) *net.UDPConn {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: ip})
	require.NoError(r.t, err)
	r.t.Cleanup(func() { conn.Close() })
	return conn
}

// listenElsewhere adds a peer on another IP; the test is skipped where 127.0.0.2 is not routed to loopback.
func (r *followRig) listenElsewhere(name string, peers map[string]*net.UDPConn) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 2)})
	if err != nil {
		r.t.Skipf("no second loopback address: %v", err)
	}
	r.t.Cleanup(func() { conn.Close() })
	peers[name] = conn
}

func (r *followRig) readRTPFrom(peer string, ssrc uint32, seq uint16) {
	r.t.Helper()

	pkt := rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 0, SequenceNumber: seq, Timestamp: uint32(seq) * 160, SSRC: ssrc}, Payload: make([]byte, 160)}
	data, err := pkt.Marshal()
	require.NoError(r.t, err)
	_, err = r.rtpPeers[peer].WriteTo(data, r.s.Sess.rtpConn.LocalAddr())
	require.NoError(r.t, err)

	r.s.Sess.rtpConn.SetReadDeadline(time.Now().Add(time.Second))
	read := rtp.Packet{}
	_, err = r.s.ReadRTP(make([]byte, RTPBufSize), &read)
	require.NoError(r.t, err)
	require.Equal(r.t, seq, read.SequenceNumber)
}

// sendRTP writes one packet and returns the peers it reached.
func (r *followRig) sendRTP() []string {
	r.t.Helper()

	pkt := rtp.Packet{Header: rtp.Header{Version: 2, SSRC: 0xabcdef}, Payload: make([]byte, 160)}
	data, err := pkt.Marshal()
	require.NoError(r.t, err)
	_, err = r.s.Sess.WriteRTPRaw(data)
	require.NoError(r.t, err)

	return reachedPeers(r.rtpPeers, data)
}

func reachedPeers(peers map[string]*net.UDPConn, data []byte) []string {
	reached := []string{}
	for _, name := range []string{"old", "new", "stranger", "elsewhere", "oldRTCP", "newRTCP", "elsewhereRTCP"} {
		peer, ok := peers[name]
		if !ok {
			continue
		}

		peer.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
		if n, _, err := peer.ReadFrom(make([]byte, 1500)); err == nil && n == len(data) {
			reached = append(reached, name)
		}
	}
	return reached
}

func TestRTPNATFollowMovesWithTheStream(t *testing.T) {
	r := newFollowRig(t)
	for seq := uint16(1); seq <= 3; seq++ {
		r.readRTPFrom("old", streamSSRC, seq)
		require.Equal(t, []string{"old"}, r.sendRTP())
	}

	// The NAT re-maps the stream to another port: we follow its first packet there.
	r.readRTPFrom("new", streamSSRC, 4)
	require.Equal(t, []string{"new"}, r.sendRTP())
	r.readRTPFrom("new", streamSSRC, 5)
	require.Equal(t, []string{"new"}, r.sendRTP())

	// And back again, after a few lost packets.
	r.readRTPFrom("old", streamSSRC, 9)
	require.Equal(t, []string{"old"}, r.sendRTP())
}

func TestRTPNATFollowNeverMovesToAnotherStream(t *testing.T) {
	r := newFollowRig(t)
	r.rtpPeers["stranger"] = r.listen(net.IPv4(127, 0, 0, 1))
	for seq := uint16(1); seq <= 3; seq++ {
		r.readRTPFrom("old", streamSSRC, seq)
	}

	// Another stream on the same IP, however long it runs, gets nothing.
	for seq := uint16(500); seq < 510; seq++ {
		r.readRTPFrom("stranger", 0x1234, seq)
		require.Equal(t, []string{"old"}, r.sendRTP(), "packet %d", seq)
	}

	// Nor did it disturb the stream, which still moves.
	r.readRTPFrom("new", streamSSRC, 4)
	require.Equal(t, []string{"new"}, r.sendRTP())
}

func TestRTPNATFollowNeedsTheNextSequence(t *testing.T) {
	r := newFollowRig(t)
	r.readRTPFrom("old", streamSSRC, 100)

	for _, seq := range []uint16{100, 99, 100 + maxDropout} {
		r.readRTPFrom("new", streamSSRC, seq)
		require.Equal(t, []string{"old"}, r.sendRTP(), "packet %d", seq)
	}
}

func TestRTPNATFollowStaysOnTheIP(t *testing.T) {
	r := newFollowRig(t)
	r.listenElsewhere("elsewhere", r.rtpPeers)
	r.readRTPFrom("old", streamSSRC, 1)

	// Even the stream's next packet does not take us to another host.
	r.readRTPFrom("elsewhere", streamSSRC, 2)
	require.Equal(t, []string{"old"}, r.sendRTP())
}

func TestRTPNATFollowLatchesOnTheFirstPacket(t *testing.T) {
	t.Run("from another address", func(t *testing.T) {
		r := newFollowRig(t)
		r.rtpPeers["stranger"] = r.listen(net.IPv4(127, 0, 0, 1))

		r.readRTPFrom("new", streamSSRC, 1)
		require.Equal(t, []string{"new"}, r.sendRTP())

		r.readRTPFrom("stranger", 0x1234, 2)
		require.Equal(t, []string{"new"}, r.sendRTP())
	})

	t.Run("from the SDP address", func(t *testing.T) {
		r := newFollowRig(t)
		r.rtpPeers["stranger"] = r.listen(net.IPv4(127, 0, 0, 1))

		// Unlike RTPNATSymetric, nothing is left to learn after the first packet.
		r.readRTPFrom("old", streamSSRC, 1)
		r.readRTPFrom("stranger", 0x1234, 2)
		require.Equal(t, []string{"old"}, r.sendRTP())
	})
}

func TestRTPNATSymetricStillLatchesOnce(t *testing.T) {
	r := newFollowRig(t)
	r.s.Sess.RTPNAT = RTPNATSymetric
	r.rtpPeers["stranger"] = r.listen(net.IPv4(127, 0, 0, 1))

	r.readRTPFrom("new", streamSSRC, 1)
	require.Equal(t, []string{"new"}, r.sendRTP())

	for seq := uint16(2); seq <= 6; seq++ {
		r.readRTPFrom("stranger", streamSSRC, seq)
	}
	require.Equal(t, []string{"new"}, r.sendRTP())
}

func TestRTPNATFollowRTCPFollowsTheStream(t *testing.T) {
	r := newFollowRig(t)
	r.listenElsewhere("elsewhereRTCP", r.rtcpPeers)
	r.readRTPFrom("old", streamSSRC, 1)

	read := make(chan struct{}, 1)
	r.s.OnReadRTCP(func(rtcp.Packet, RTPReadStats) { read <- struct{}{} })
	require.NoError(t, r.s.MonitorBackground())
	t.Cleanup(func() { r.s.MonitorClose() })

	readReportFrom := func(peer string, ssrc uint32) {
		t.Helper()
		data, err := rtcp.Marshal([]rtcp.Packet{&rtcp.ReceiverReport{SSRC: ssrc}})
		require.NoError(t, err)
		_, err = r.rtcpPeers[peer].WriteTo(data, r.s.Sess.rtcpConn.LocalAddr())
		require.NoError(t, err)
		select {
		case <-read:
		case <-time.After(time.Second):
			t.Fatal("RTCP report not read")
		}
	}
	sendRTCP := func() []string {
		t.Helper()
		data, err := rtcp.Marshal([]rtcp.Packet{&rtcp.ReceiverReport{SSRC: 0xabcdef}})
		require.NoError(t, err)
		_, err = r.s.Sess.WriteRTCPRaw(data)
		require.NoError(t, err)
		return reachedPeers(r.rtcpPeers, data)
	}

	require.Equal(t, []string{"oldRTCP"}, sendRTCP())

	readReportFrom("newRTCP", 0x1234)
	require.Equal(t, []string{"oldRTCP"}, sendRTCP(), "another stream's report moves nothing")

	readReportFrom("elsewhereRTCP", streamSSRC)
	require.Equal(t, []string{"oldRTCP"}, sendRTCP(), "a report from another host moves nothing")

	readReportFrom("newRTCP", streamSSRC)
	require.Equal(t, []string{"newRTCP"}, sendRTCP())
}
