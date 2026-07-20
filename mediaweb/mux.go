// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2026, Emir Aganovic

package mediaweb

import (
	"errors"
	"io"
	"net"
	"sync"
	"time"

	"github.com/emiago/diago/media"
	"github.com/pion/transport/v4/packetio"
)

// webRTCPacketMux splits the single ICE connection into DTLS, SRTP and SRTCP
// packet connections. ICE has already consumed STUN packets before they reach
// this code. Keeping one transport here is important: a WebRTC browser uses
// BUNDLE and rtcp-mux, so opening a second UDP socket would bypass ICE.
type webRTCPacketMux struct {
	conn net.Conn
	dtls *webRTCMuxEndpoint
	rtp  *webRTCMuxEndpoint
	rtcp *webRTCMuxEndpoint
	once sync.Once
}

type webRTCMuxEndpoint struct {
	conn   net.Conn
	buffer *packetio.Buffer
}

func newWebRTCPacketMux(conn net.Conn) *webRTCPacketMux {
	newEndpoint := func() *webRTCMuxEndpoint {
		buffer := packetio.NewBuffer()
		buffer.SetLimitSize(1024 * 1024)
		return &webRTCMuxEndpoint{conn: conn, buffer: buffer}
	}
	m := &webRTCPacketMux{
		conn: conn,
		dtls: newEndpoint(),
		rtp:  newEndpoint(),
		rtcp: newEndpoint(),
	}
	go m.readLoop()
	return m
}

func (m *webRTCPacketMux) readLoop() {
	buf := make([]byte, 8192)
	for {
		n, err := m.conn.Read(buf)
		if err != nil {
			_ = m.Close()
			return
		}
		if n < 1 {
			continue
		}

		// RFC 7983 assigns non-overlapping first-byte ranges. RTP and RTCP
		// share 128..191; RFC 5761 distinguishes RTCP using packet types
		// 192..223 in the second byte. Negotiated RTP payload types must avoid
		// the corresponding ambiguous range, as required by rtcp-mux.
		var endpoint *webRTCMuxEndpoint
		switch {
		case buf[0] >= 20 && buf[0] <= 63:
			endpoint = m.dtls
		case buf[0] >= 128 && buf[0] <= 191 && n > 1 && buf[1] >= 192 && buf[1] <= 223:
			endpoint = m.rtcp
		case buf[0] >= 128 && buf[0] <= 191:
			endpoint = m.rtp
		default:
			continue
		}
		if _, err = endpoint.buffer.Write(buf[:n]); err != nil && !errors.Is(err, io.ErrClosedPipe) {
			media.DefaultLogger().Warn("WebRTC packet mux dropped packet", "error", err)
		}
	}
}

func (m *webRTCPacketMux) Close() error {
	var err error
	m.once.Do(func() {
		err = errors.Join(m.dtls.Close(), m.rtp.Close(), m.rtcp.Close(), m.conn.Close())
	})
	return err
}

func (e *webRTCMuxEndpoint) ReadFrom(p []byte) (int, net.Addr, error) {
	n, err := e.buffer.Read(p)
	return n, e.conn.RemoteAddr(), err
}

func (e *webRTCMuxEndpoint) WriteTo(p []byte, _ net.Addr) (int, error) {
	return e.conn.Write(p)
}

func (e *webRTCMuxEndpoint) Close() error        { return e.buffer.Close() }
func (e *webRTCMuxEndpoint) LocalAddr() net.Addr { return e.conn.LocalAddr() }
func (e *webRTCMuxEndpoint) SetDeadline(t time.Time) error {
	return errors.Join(e.SetReadDeadline(t), e.SetWriteDeadline(t))
}
func (e *webRTCMuxEndpoint) SetReadDeadline(t time.Time) error  { return e.buffer.SetReadDeadline(t) }
func (e *webRTCMuxEndpoint) SetWriteDeadline(t time.Time) error { return e.conn.SetWriteDeadline(t) }
