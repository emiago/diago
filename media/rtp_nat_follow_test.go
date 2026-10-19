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

// followRig is a session in RTPNATFollow mode and the remote addresses its media can come from.
type followRig struct {
	t *testing.T
	m *MediaSession

	old, new, stranger *net.UDPConn
	oldRTCP, newRTCP   *net.UDPConn
	rtpPeers           map[string]*net.UDPConn
}

func newFollowRig(t *testing.T) *followRig {
	listen := func() *net.UDPConn {
		conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
		require.NoError(t, err)
		t.Cleanup(func() { conn.Close() })
		return conn
	}

	r := &followRig{t: t, old: listen(), new: listen(), stranger: listen(), oldRTCP: listen(), newRTCP: listen()}
	r.rtpPeers = map[string]*net.UDPConn{"old": r.old, "new": r.new, "stranger": r.stranger}

	m, err := NewMediaSession(net.IPv4(127, 0, 0, 1), 0)
	require.NoError(t, err)
	t.Cleanup(func() { m.Close() })

	m.RTPNAT = RTPNATFollow
	m.Raddr = *r.old.LocalAddr().(*net.UDPAddr)
	m.rtcpRaddr = *r.oldRTCP.LocalAddr().(*net.UDPAddr)
	r.m = m
	return r
}

func (r *followRig) readRTPFrom(peer *net.UDPConn, ssrc uint32, seq uint16) {
	r.t.Helper()

	pkt := rtp.Packet{Header: rtp.Header{Version: 2, SequenceNumber: seq, Timestamp: uint32(seq) * 160, SSRC: ssrc}, Payload: make([]byte, 160)}
	data, err := pkt.Marshal()
	require.NoError(r.t, err)
	_, err = peer.WriteTo(data, r.m.rtpConn.LocalAddr())
	require.NoError(r.t, err)

	r.m.rtpConn.SetReadDeadline(time.Now().Add(time.Second))
	read := rtp.Packet{}
	_, err = r.m.ReadRTP(make([]byte, RTPBufSize), &read)
	require.NoError(r.t, err)
	require.Equal(r.t, seq, read.SequenceNumber)
}

// sendRTP writes one packet and returns the peers it reached.
func (r *followRig) sendRTP() []string {
	r.t.Helper()

	pkt := rtp.Packet{Header: rtp.Header{Version: 2, SSRC: 0xabcdef}, Payload: make([]byte, 160)}
	data, err := pkt.Marshal()
	require.NoError(r.t, err)
	_, err = r.m.WriteRTPRaw(data)
	require.NoError(r.t, err)

	return reachedPeers(r.rtpPeers, data)
}

func reachedPeers(peers map[string]*net.UDPConn, data []byte) []string {
	reached := []string{}
	for _, name := range []string{"old", "new", "stranger", "oldRTCP", "newRTCP"} {
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

func TestRTPNATFollowMovesAfterProbation(t *testing.T) {
	r := newFollowRig(t)
	for seq := uint16(1); seq <= 3; seq++ {
		r.readRTPFrom(r.old, streamSSRC, seq)
		require.Equal(t, []string{"old"}, r.sendRTP())
	}

	// The NAT moves the stream: both get our RTP during the probation.
	for seq := uint16(4); seq <= 6; seq++ {
		r.readRTPFrom(r.new, streamSSRC, seq)
		require.Equal(t, []string{"old", "new"}, r.sendRTP(), "probation packet %d", seq)
	}
	r.readRTPFrom(r.new, streamSSRC, 7)
	require.Equal(t, []string{"new"}, r.sendRTP())

	// And back again.
	for seq := uint16(8); seq <= 10; seq++ {
		r.readRTPFrom(r.old, streamSSRC, seq)
		require.Equal(t, []string{"old", "new"}, r.sendRTP())
	}
	r.readRTPFrom(r.old, streamSSRC, 11)
	require.Equal(t, []string{"old"}, r.sendRTP())
}

func TestRTPNATFollowIgnoresFlap(t *testing.T) {
	r := newFollowRig(t)
	r.readRTPFrom(r.old, streamSSRC, 1)

	for seq := uint16(2); seq < 12; seq += 2 {
		r.readRTPFrom(r.new, streamSSRC, seq)
		require.Equal(t, []string{"old", "new"}, r.sendRTP())

		r.readRTPFrom(r.old, streamSSRC, seq+1)
		require.Equal(t, []string{"old"}, r.sendRTP(), "a packet from the current source ends the probation")
	}
}

func TestRTPNATFollowGivesStrangerNothing(t *testing.T) {
	r := newFollowRig(t)
	for seq := uint16(1); seq <= 3; seq++ {
		r.readRTPFrom(r.old, streamSSRC, seq)
	}

	// Another stream gets no copy during its probation and takes over only after it.
	for seq := uint16(500); seq < 503; seq++ {
		r.readRTPFrom(r.stranger, 0x1234, seq)
		require.Equal(t, []string{"old"}, r.sendRTP())
	}
	r.readRTPFrom(r.stranger, 0x1234, 503)
	require.Equal(t, []string{"stranger"}, r.sendRTP())
}

func TestRTPNATFollowRestartsProbationOnSequenceGap(t *testing.T) {
	r := newFollowRig(t)
	r.readRTPFrom(r.old, streamSSRC, 1)

	for _, seq := range []uint16{2, 3, 5, 6, 7} {
		r.readRTPFrom(r.new, streamSSRC, seq)
		require.Equal(t, []string{"old", "new"}, r.sendRTP(), "packet %d", seq)
	}

	r.readRTPFrom(r.new, streamSSRC, 8)
	require.Equal(t, []string{"new"}, r.sendRTP())
}

func TestRTPNATSymetricStillLatchesOnce(t *testing.T) {
	r := newFollowRig(t)
	r.m.RTPNAT = RTPNATSymetric

	r.readRTPFrom(r.new, streamSSRC, 1)
	require.Equal(t, []string{"new"}, r.sendRTP())

	for seq := uint16(2); seq <= 6; seq++ {
		r.readRTPFrom(r.stranger, streamSSRC, seq)
	}
	require.Equal(t, []string{"new"}, r.sendRTP())
}

func TestRTPNATFollowRTCPFollowsTheStream(t *testing.T) {
	r := newFollowRig(t)
	r.readRTPFrom(r.old, streamSSRC, 1)

	readReportFrom := func(peer *net.UDPConn, ssrc uint32) {
		t.Helper()
		data, err := rtcp.Marshal([]rtcp.Packet{&rtcp.ReceiverReport{SSRC: ssrc}})
		require.NoError(t, err)
		_, err = peer.WriteTo(data, r.m.rtcpConn.LocalAddr())
		require.NoError(t, err)

		r.m.rtcpConn.SetReadDeadline(time.Now().Add(time.Second))
		_, err = r.m.ReadRTCP(make([]byte, 1500), make([]rtcp.Packet, 5))
		require.NoError(t, err)
	}
	sendRTCP := func() []string {
		t.Helper()
		data, err := rtcp.Marshal([]rtcp.Packet{&rtcp.ReceiverReport{SSRC: 0xabcdef}})
		require.NoError(t, err)
		_, err = r.m.WriteRTCPRaw(data)
		require.NoError(t, err)
		return reachedPeers(map[string]*net.UDPConn{"oldRTCP": r.oldRTCP, "newRTCP": r.newRTCP}, data)
	}

	require.Equal(t, []string{"oldRTCP"}, sendRTCP())

	readReportFrom(r.newRTCP, 0x1234)
	require.Equal(t, []string{"oldRTCP"}, sendRTCP(), "another stream's report moves nothing")

	readReportFrom(r.newRTCP, streamSSRC)
	require.Equal(t, []string{"newRTCP"}, sendRTCP())
}
