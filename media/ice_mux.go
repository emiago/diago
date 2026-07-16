// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package media

import (
	"net"
	"sync"
	"time"

	"github.com/pion/ice/v4"
	"github.com/pion/transport/v3/packetio"
)

// iceMuxBufferLimit bounds how much each demultiplexed stream may queue before
// packets are dropped. A stalled consumer must not grow memory without bound,
// and media is worthless once it is late, so dropping is the correct loss mode.
const iceMuxBufferLimit = 1_000_000

// IsRTCP reports whether buf carries RTCP rather than RTP.
//
// RTP and RTCP share one port under rtcp-mux (RFC 5761). They are told apart by
// the payload type field: section 4 of that RFC reserves 64-95 for RTCP, which
// cannot collide with an RTP payload type once the marker bit is masked off.
func IsRTCP(buf []byte) bool {
	if len(buf) < 2 {
		return false
	}
	pt := buf[1] & 0x7f
	return pt >= 64 && pt <= 95
}

// isDTLS reports whether buf carries a DTLS record.
//
// RFC 5764 section 5.1.2 assigns the value of the first byte: 20-63 is DTLS,
// 128-191 is RTP or RTCP, and 0-3 is STUN. STUN is consumed by the ICE agent
// before it reaches the mux, so only the first two ranges are demultiplexed.
func isDTLS(buf []byte) bool {
	if len(buf) < 1 {
		return false
	}
	return buf[0] >= 20 && buf[0] <= 63
}

// iceMux fans one ICE connection out into DTLS, RTP and RTCP packet conns.
//
// ICE nominates a single candidate pair, so the session owns exactly one
// socket. DTLS-SRTP then requires the handshake, the media and the control
// traffic to share it. Each stream is presented as a net.PacketConn so that
// the rest of MediaSession, which is written against net.PacketConn, works
// over ICE without knowing about it.
type iceMux struct {
	conn  *ice.Conn
	raddr net.Addr

	dtls *muxConn
	rtp  *muxConn
	rtcp *muxConn

	closeOnce sync.Once
	wg        sync.WaitGroup
}

// newICEMux starts demultiplexing conn. raddr is the remote address ICE
// nominated and is reported to readers as the packet source.
func newICEMux(conn *ice.Conn, raddr net.Addr) *iceMux {
	m := &iceMux{conn: conn, raddr: raddr}
	m.dtls = newMuxConn(m)
	m.rtp = newMuxConn(m)
	m.rtcp = newMuxConn(m)

	m.wg.Add(1)
	go m.readLoop()
	return m
}

// readLoop is the only reader of the ICE connection. Every packet is
// classified and handed to the matching stream buffer.
func (m *iceMux) readLoop() {
	defer m.wg.Done()

	buf := make([]byte, RTPBufSize)
	for {
		n, err := m.conn.Read(buf)
		if err != nil {
			// Any read error is terminal. Continuing would spin this loop on a
			// permanently failed connection and burn a core for the life of the
			// call. Closing unblocks every parked consumer with io.EOF.
			DefaultLogger().Debug("ICE mux read loop stopped", "error", err)
			m.closeBuffers()
			return
		}
		if n == 0 {
			continue
		}

		pkt := buf[:n]
		var dst *muxConn
		switch {
		case isDTLS(pkt):
			dst = m.dtls
		case IsRTCP(pkt):
			dst = m.rtcp
		default:
			dst = m.rtp
		}

		if _, err := dst.buffer.Write(pkt); err != nil {
			// A full or closed buffer must not kill the other streams.
			DefaultLogger().Debug("ICE mux dropped packet", "error", err)
		}
	}
}

// closeBuffers unblocks consumers without touching the ICE connection.
func (m *iceMux) closeBuffers() {
	_ = m.dtls.buffer.Close()
	_ = m.rtp.buffer.Close()
	_ = m.rtcp.buffer.Close()
}

// Close stops the read loop, releases the ICE connection and unblocks readers.
// It is idempotent, which matters because MediaSession.Close closes both the
// RTP and the RTCP view of this mux.
func (m *iceMux) Close() error {
	var err error
	m.closeOnce.Do(func() {
		// Closing the ICE connection makes the parked Read in readLoop return,
		// which then closes the buffers and exits the goroutine.
		err = m.conn.Close()
		m.closeBuffers()
		m.wg.Wait()
	})
	return err
}

// muxConn is one demultiplexed stream presented as a net.PacketConn. Reads are
// served from the stream buffer, writes go straight to the shared ICE
// connection.
type muxConn struct {
	mux    *iceMux
	buffer *packetio.Buffer
}

func newMuxConn(m *iceMux) *muxConn {
	b := packetio.NewBuffer()
	b.SetLimitSize(iceMuxBufferLimit)
	return &muxConn{mux: m, buffer: b}
}

var _ net.PacketConn = (*muxConn)(nil)

// ReadFrom returns the next packet of this stream. The address is always the
// remote that ICE nominated, since that is the only source the pair accepts.
func (c *muxConn) ReadFrom(p []byte) (int, net.Addr, error) {
	n, err := c.buffer.Read(p)
	if err != nil {
		return 0, nil, err
	}
	return n, c.mux.raddr, nil
}

// WriteTo writes to the ICE connection. The address is ignored: ICE routes to
// the nominated pair, and diago hands it the same address anyway.
func (c *muxConn) WriteTo(p []byte, _ net.Addr) (int, error) {
	return c.mux.conn.Write(p)
}

func (c *muxConn) Close() error {
	return c.mux.Close()
}

func (c *muxConn) LocalAddr() net.Addr {
	return c.mux.conn.LocalAddr()
}

func (c *muxConn) SetReadDeadline(t time.Time) error {
	return c.buffer.SetReadDeadline(t)
}

// SetWriteDeadline applies to the shared ICE connection, so it affects every
// stream of this mux. They are one socket, so that is the honest behaviour.
func (c *muxConn) SetWriteDeadline(t time.Time) error {
	return c.mux.conn.SetWriteDeadline(t)
}

func (c *muxConn) SetDeadline(t time.Time) error {
	if err := c.SetReadDeadline(t); err != nil {
		return err
	}
	return c.SetWriteDeadline(t)
}
