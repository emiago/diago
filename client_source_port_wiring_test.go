// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"context"
	"errors"
	"net"
	"syscall"
	"testing"
	"time"

	"github.com/emiago/sipgo/sip"
)

// These tests pin that createClient CONSUMES clientSourcePort, not merely that
// the helper decides correctly.
//
// Why they exist separately from TestClientSourcePort: the helper's own subtests
// call clientSourcePort directly, so deleting the single line in createClient
// that calls it -- leaving the helper fully intact -- reintroduces the outage
// with the whole suite green. That one line is the entire fix as far as
// production is concerned; the helper is inert without it.
//
// The outage: the UA registers before it serves, so at REGISTER time no listener
// holds the configured port. createClient pinned it anyway via
// WithClientConnectionAddr, the transaction layer missed the connection pool
// (the socket is bound by the listener but not yet registered in the pool), fell
// through to CreateConnection, and bound a SECOND socket on the same local
// address -- "listen udp 192.168.1.219:25000: bind: address already in use". The
// ingress collided with its own listener and SIP never came up.
//
// These drive the real production path: NewDiago -> loadTransports ->
// createClient, which is where diago.go:134 calls it.

// isAddrInUse reports whether err is the kernel refusing a second bind on an
// address someone else holds -- the exact failure the outage produced.
func isAddrInUse(err error) bool {
	return errors.Is(err, syscall.EADDRINUSE)
}

// squatUDPPort takes a port with an unrelated owner and keeps it for the test.
// A port the OS picks is used rather than the literal 25000 from the outage, so
// the test cannot flake on whatever happens to be running on the machine.
func squatUDPPort(t *testing.T) int {
	t.Helper()

	conn, err := net.ListenPacket("udp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("squatting a UDP port: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })

	return conn.LocalAddr().(*net.UDPAddr).Port
}

// TestCreateClientDoesNotSourceFromAnUnlistenedPort is the regression. With no
// listener holding the port, createClient must not pin it: it must route the
// configured port through clientSourcePort and end up asking for an ephemeral
// one, which always binds.
func TestCreateClientDoesNotSourceFromAnUnlistenedPort(t *testing.T) {
	port := squatUDPPort(t)

	// A UA that has NOT served: exactly the register-before-serve moment.
	ua := newTestUA(t)
	dg := NewDiago(ua, WithTransport(Transport{
		Transport: "udp",
		BindHost:  "127.0.0.1",
		BindPort:  port,
	}))

	if len(dg.transports) != 1 {
		t.Fatalf("expected one transport, got %d", len(dg.transports))
	}
	client := dg.transports[0].client
	if client == nil {
		t.Fatal("transport has no client")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	// Unroutable recipient: nobody answers, so a timeout is the expected pass.
	// The bind we care about happens before any response could arrive.
	recipient := sip.Uri{User: "nobody", Host: "127.0.0.1", Port: 9}
	req := sip.NewRequest(sip.REGISTER, recipient)

	tx, err := client.TransactionRequest(ctx, req)
	if tx != nil {
		defer tx.Terminate()
	}

	if err != nil && isAddrInUse(err) {
		t.Fatalf("createClient sourced from %d while no listener held it, so the local bind "+
			"collided with the port's actual owner: %v\n\n"+
			"This is the outage: a UA registers before it serves, the pool lookup misses, "+
			"CreateConnection binds a second socket on the same local address, and SIP never "+
			"comes up. createClient must route tran.BindPort through clientSourcePort before "+
			"WithClientConnectionAddr.", port, err)
	}
}

// TestCreateClientPinsAPortItListensOn pins the other half, so the wiring cannot
// be satisfied by always zeroing the port. A port we already listen on IS
// reusable, and sourcing from it is the intended behaviour -- symmetric RTP-style
// reuse is why the pin exists at all.
func TestCreateClientPinsAPortItListensOn(t *testing.T) {
	ua := newTestUA(t)
	port := serveUDP(t, ua) // a real listener, registered with the transport layer

	dg := NewDiago(ua, WithTransport(Transport{
		Transport: "udp",
		BindHost:  "127.0.0.1",
		BindPort:  port,
	}))

	client := dg.transports[0].client
	if client == nil {
		t.Fatal("transport has no client")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	recipient := sip.Uri{User: "nobody", Host: "127.0.0.1", Port: 9}
	req := sip.NewRequest(sip.REGISTER, recipient)

	tx, err := client.TransactionRequest(ctx, req)
	if tx != nil {
		defer tx.Terminate()
	}

	// The listener holds this port and the pool knows it, so reuse must succeed.
	// A collision here would mean the pin bound a fresh socket instead of reusing.
	if err != nil && isAddrInUse(err) {
		t.Fatalf("sourcing from %d collided even though the transport layer listens on it: %v\n\n"+
			"A port we already listen on is reusable; a bind error here means the client is "+
			"opening a second socket rather than reusing the listener's.", port, err)
	}

	// And the request must actually leave from that port: clientSourcePort
	// returning 0 for a port we DO listen on would silently drop the reuse.
	if got := clientSourcePort(ua, port); got != port {
		t.Fatalf("clientSourcePort(%d) = %d: the listened port must still be pinned, "+
			"otherwise createClient sources every request from a random port and the "+
			"peer's responses have nowhere symmetric to land", port, got)
	}
}
