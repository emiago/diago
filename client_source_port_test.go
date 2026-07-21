// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"net"
	"testing"
	"time"

	"github.com/emiago/sipgo"
)

// newTestUA builds a UA whose transport layer starts with no listeners.
func newTestUA(t *testing.T) *sipgo.UserAgent {
	t.Helper()
	ua, err := sipgo.NewUA()
	if err != nil {
		t.Fatalf("NewUA: %v", err)
	}
	t.Cleanup(func() { _ = ua.Close() })
	return ua
}

// serveUDP brings a real UDP listener up on the UA and returns its port, so the
// test asserts against the transport layer's own view rather than a stub.
func serveUDP(t *testing.T, ua *sipgo.UserAgent) int {
	t.Helper()
	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	port := conn.LocalAddr().(*net.UDPAddr).Port
	go func() { _ = ua.TransportLayer().ServeUDP(conn) }()

	// ServeUDP registers the port from the serving goroutine.
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		for _, p := range ua.TransportLayer().ListenPorts("udp") {
			if p == port {
				return port
			}
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("listener on %d never registered with the transport layer", port)
	return 0
}

// TestClientSourcePort pins which local port a UDP client may source from.
//
// Sourcing from a port no listener holds is a fresh bind, not a reuse, so it
// fails against any other process on that port. This is reachable in production:
// a UA registers before it serves, so the configured port is unheld at exactly
// the moment the REGISTER goes out.
func TestClientSourcePort(t *testing.T) {
	t.Run("no listener: must not pin", func(t *testing.T) {
		ua := newTestUA(t)

		if got := clientSourcePort(ua, 25000); got != 0 {
			t.Fatalf("clientSourcePort = %d, want 0: pinning a port no listener "+
				"holds binds it from scratch and collides with whatever owns it", got)
		}
	})

	t.Run("listener on the same port: may pin", func(t *testing.T) {
		ua := newTestUA(t)
		port := serveUDP(t, ua)

		if got := clientSourcePort(ua, port); got != port {
			t.Fatalf("clientSourcePort = %d, want %d: a port we already listen on "+
				"is reusable and should be pinned", got, port)
		}
	})

	// The case a "do we listen on anything?" guard gets wrong: serving 5060 does
	// not make an unheld 25000 bindable.
	t.Run("listener on a different port: must not pin", func(t *testing.T) {
		ua := newTestUA(t)
		listening := serveUDP(t, ua)

		other := listening + 1
		if got := clientSourcePort(ua, other); got != 0 {
			t.Fatalf("clientSourcePort = %d, want 0: listening on %d says nothing "+
				"about whether %d is ours to bind", got, listening, other)
		}
	})

	t.Run("zero stays zero", func(t *testing.T) {
		ua := newTestUA(t)
		serveUDP(t, ua)

		if got := clientSourcePort(ua, 0); got != 0 {
			t.Fatalf("clientSourcePort = %d, want 0: an unset port is a request "+
				"for an ephemeral one", got)
		}
	})
}
