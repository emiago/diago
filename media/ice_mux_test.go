// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package media

import (
	"net"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIsRTCP(t *testing.T) {
	// RFC 5761 section 4 reserves payload types 64-95 for RTCP so that RTP and
	// RTCP can share one port without ambiguity.
	tests := []struct {
		name string
		buf  []byte
		want bool
	}{
		{"rtcp sender report 200", []byte{0x80, 200}, true},
		{"rtcp receiver report 201", []byte{0x80, 201}, true},
		{"rtcp bye 203", []byte{0x80, 203}, true},
		{"rtcp lower bound 64", []byte{0x80, 64}, true},
		{"rtcp upper bound 95", []byte{0x80, 95}, true},
		{"rtp pcmu payload 0", []byte{0x80, 0}, false},
		{"rtp pcma payload 8", []byte{0x80, 8}, false},
		{"rtp dynamic payload 96", []byte{0x80, 96}, false},
		{"rtp payload 63 below rtcp range", []byte{0x80, 63}, false},
		// The marker bit shares a byte with the payload type and must be
		// masked off before the range check.
		{"rtp payload 8 with marker bit set", []byte{0x80, 0x80 | 8}, false},
		{"rtcp 200 with top bit set", []byte{0x80, 0x80 | 200&0x7f}, true},
		{"too short", []byte{0x80}, false},
		{"empty", []byte{}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, IsRTCP(tc.buf))
		})
	}
}

func TestIsDTLS(t *testing.T) {
	// RFC 5764 section 5.1.2 assigns the first byte: 20-63 DTLS, 128-191
	// RTP/RTCP, 0-3 STUN.
	tests := []struct {
		name string
		buf  []byte
		want bool
	}{
		{"dtls lower bound 20", []byte{20, 0xfe}, true},
		{"dtls handshake 22", []byte{22, 0xfe, 0xfd}, true},
		{"dtls upper bound 63", []byte{63, 0x00}, true},
		{"rtp 128", []byte{128, 0}, false},
		{"rtcp 128 pt200", []byte{128, 200}, false},
		{"stun binding request", []byte{0x00, 0x01}, false},
		{"below dtls range 19", []byte{19, 0}, false},
		{"above dtls range 64", []byte{64, 0}, false},
		{"empty", []byte{}, false},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.want, isDTLS(tc.buf))
		})
	}
}

// udpPeer presents a bound UDP socket as a net.Conn aimed at one peer. It
// keeps packet boundaries, which net.Pipe would not, and it never rebinds a
// port, which dialing both ends of a pair would.
type udpPeer struct {
	*net.UDPConn
	peer *net.UDPAddr
}

func (p *udpPeer) Read(b []byte) (int, error) {
	n, _, err := p.UDPConn.ReadFromUDP(b)
	return n, err
}

func (p *udpPeer) Write(b []byte) (int, error) {
	return p.UDPConn.WriteToUDP(b, p.peer)
}

func (p *udpPeer) RemoteAddr() net.Addr { return p.peer }

// udpConnPair returns two UDP sockets pointed at each other, standing in for
// the nominated ICE pair. iceMux only needs a net.Conn, so this exercises the
// real read loop over a real socket.
func udpConnPair(t *testing.T) (local net.Conn, remote net.Conn) {
	t.Helper()

	a, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	require.NoError(t, err)
	b, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	require.NoError(t, err)

	t.Cleanup(func() {
		_ = a.Close()
		_ = b.Close()
	})

	return &udpPeer{UDPConn: a, peer: b.LocalAddr().(*net.UDPAddr)},
		&udpPeer{UDPConn: b, peer: a.LocalAddr().(*net.UDPAddr)}
}

func readWithTimeout(t *testing.T, c net.PacketConn) []byte {
	t.Helper()
	require.NoError(t, c.SetReadDeadline(time.Now().Add(2*time.Second)))
	buf := make([]byte, 1500)
	n, _, err := c.ReadFrom(buf)
	require.NoError(t, err)
	return buf[:n]
}

// TestICEMuxDemultiplexes asserts each packet class reaches its own stream and
// no other. This is the property the single socket ICE layout depends on: the
// DTLS handshake, media and control all arrive on the nominated pair.
func TestICEMuxDemultiplexes(t *testing.T) {
	local, remote := udpConnPair(t)
	raddr := remote.LocalAddr()

	mux := newICEMux(local, raddr)
	t.Cleanup(func() { _ = mux.Close() })

	dtlsPkt := []byte{22, 0xfe, 0xfd, 0x01, 0x02}
	rtcpPkt := []byte{0x80, 200, 0x00, 0x06, 0xde, 0xad}
	rtpPkt := []byte{0x80, 8, 0x00, 0x01, 0xbe, 0xef}

	_, err := remote.Write(dtlsPkt)
	require.NoError(t, err)
	assert.Equal(t, dtlsPkt, readWithTimeout(t, mux.dtls), "DTLS record must reach the DTLS stream")

	_, err = remote.Write(rtcpPkt)
	require.NoError(t, err)
	assert.Equal(t, rtcpPkt, readWithTimeout(t, mux.rtcp), "RTCP must reach the RTCP stream")

	_, err = remote.Write(rtpPkt)
	require.NoError(t, err)
	assert.Equal(t, rtpPkt, readWithTimeout(t, mux.rtp), "RTP must reach the RTP stream")

	// Each stream got exactly its own packet: nothing crossed over.
	for name, c := range map[string]*muxConn{"dtls": mux.dtls, "rtcp": mux.rtcp, "rtp": mux.rtp} {
		require.NoError(t, c.SetReadDeadline(time.Now().Add(100*time.Millisecond)))
		buf := make([]byte, 1500)
		_, _, err := c.ReadFrom(buf)
		assert.Error(t, err, "%s stream should have no further packets", name)
	}
}

// TestICEMuxReportsNominatedAddr asserts readers see the address ICE settled
// on. MediaSession compares it against rtcpRaddr for RTP NAT handling.
func TestICEMuxReportsNominatedAddr(t *testing.T) {
	local, remote := udpConnPair(t)
	raddr := remote.LocalAddr()

	mux := newICEMux(local, raddr)
	t.Cleanup(func() { _ = mux.Close() })

	_, err := remote.Write([]byte{0x80, 8, 0x00, 0x01})
	require.NoError(t, err)

	require.NoError(t, mux.rtp.SetReadDeadline(time.Now().Add(2*time.Second)))
	buf := make([]byte, 1500)
	_, addr, err := mux.rtp.ReadFrom(buf)
	require.NoError(t, err)
	assert.Equal(t, raddr.String(), addr.String())
}

// TestICEMuxWriteGoesToPair asserts every stream writes to the one ICE
// connection, since all three share the nominated pair.
func TestICEMuxWriteGoesToPair(t *testing.T) {
	local, remote := udpConnPair(t)

	mux := newICEMux(local, remote.LocalAddr())
	t.Cleanup(func() { _ = mux.Close() })

	for _, c := range []*muxConn{mux.dtls, mux.rtp, mux.rtcp} {
		_, err := c.WriteTo([]byte{0x01, 0x02, 0x03}, nil)
		require.NoError(t, err)

		require.NoError(t, remote.SetReadDeadline(time.Now().Add(2*time.Second)))
		buf := make([]byte, 1500)
		n, err := remote.Read(buf)
		require.NoError(t, err)
		assert.Equal(t, []byte{0x01, 0x02, 0x03}, buf[:n])
	}
}

// TestICEMuxCloseUnblocksReaders is the teardown guard.
//
// A reader parked in ReadFrom must be released by Close, otherwise session
// teardown blocks on it for the life of the process. Close is also called once
// per stream by MediaSession.Close, so it has to be idempotent.
func TestICEMuxCloseUnblocksReaders(t *testing.T) {
	local, remote := udpConnPair(t)

	mux := newICEMux(local, remote.LocalAddr())

	parked := make(chan error, 3)
	for _, c := range []*muxConn{mux.dtls, mux.rtp, mux.rtcp} {
		go func(c *muxConn) {
			buf := make([]byte, 1500)
			_, _, err := c.ReadFrom(buf)
			parked <- err
		}(c)
	}
	// Let all three readers reach their blocking read.
	time.Sleep(100 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		// MediaSession.Close closes both the RTCP and RTP view of the mux.
		_ = mux.rtcp.Close()
		_ = mux.rtp.Close()
		_ = mux.Close()
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("mux Close did not return: teardown is blocked on a parked reader")
	}

	for i := 0; i < 3; i++ {
		select {
		case err := <-parked:
			assert.Error(t, err, "parked reader must be released with an error")
		case <-time.After(2 * time.Second):
			t.Fatal("parked reader was not released by Close")
		}
	}
}

// TestICEMuxReadLoopStopsOnConnError asserts a dead connection terminates the
// read loop instead of spinning on it, and that consumers are unblocked.
func TestICEMuxReadLoopStopsOnConnError(t *testing.T) {
	local, remote := udpConnPair(t)
	mux := newICEMux(local, remote.LocalAddr())

	// Kill the connection under the mux without going through mux.Close.
	require.NoError(t, local.Close())

	// The read loop must notice and release consumers rather than spin.
	err := make(chan error, 1)
	go func() {
		buf := make([]byte, 1500)
		_, _, e := mux.rtp.ReadFrom(buf)
		err <- e
	}()

	select {
	case e := <-err:
		assert.Error(t, e)
	case <-time.After(2 * time.Second):
		t.Fatal("read loop did not release consumers after connection error")
	}

	// Close must still return cleanly after the loop already exited.
	done := make(chan struct{})
	go func() { _ = mux.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close blocked after read loop already exited")
	}
}
