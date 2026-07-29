// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package media

import (
	"bytes"
	"testing"

	"github.com/pion/rtp"
	"github.com/stretchr/testify/require"
)

// The observer must see every packet the session writes, with the header it
// actually sent — that is the whole reason it exists, since the header cannot be
// read back afterwards without racing another writer on the same session.
func TestMediaSessionOnWriteRTP(t *testing.T) {
	sess := fakeMediaSessionWriter(0, 1234, bytes.NewBuffer([]byte{}))

	type seen struct {
		seq uint16
		ts  uint32
		pt  uint8
	}
	var got []seen
	sess.OnWriteRTP(func(p *rtp.Packet) {
		got = append(got, seen{p.SequenceNumber, p.Timestamp, p.PayloadType})
	})

	for i := 0; i < 3; i++ {
		p := &rtp.Packet{
			Header:  rtp.Header{Version: 2, SequenceNumber: uint16(100 + i), Timestamp: uint32(160 * i), PayloadType: 8},
			Payload: []byte("payload"),
		}
		require.NoError(t, sess.WriteRTP(p))
	}

	require.Len(t, got, 3)
	for i, g := range got {
		require.Equal(t, uint16(100+i), g.seq)
		require.Equal(t, uint32(160*i), g.ts)
		require.Equal(t, uint8(8), g.pt)
	}
}

// Passing nil removes the observer, and a session that never had one writes just
// the same — the write path must not depend on an observer being present.
func TestMediaSessionOnWriteRTPNil(t *testing.T) {
	sess := fakeMediaSessionWriter(0, 1234, bytes.NewBuffer([]byte{}))

	calls := 0
	sess.OnWriteRTP(func(p *rtp.Packet) { calls++ })
	p := &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 8}, Payload: []byte("x")}
	require.NoError(t, sess.WriteRTP(p))
	require.Equal(t, 1, calls)

	sess.OnWriteRTP(nil)
	require.NoError(t, sess.WriteRTP(p))
	require.Equal(t, 1, calls, "the observer was removed and must not be called again")

	fresh := fakeMediaSessionWriter(0, 1234, bytes.NewBuffer([]byte{}))
	require.NoError(t, fresh.WriteRTP(p))
}

// Two sessions observe only their own traffic: the callback is per session, not
// a package-level hook.
func TestMediaSessionOnWriteRTPIsPerSession(t *testing.T) {
	a := fakeMediaSessionWriter(0, 1234, bytes.NewBuffer([]byte{}))
	b := fakeMediaSessionWriter(0, 1234, bytes.NewBuffer([]byte{}))

	var seenA, seenB int
	a.OnWriteRTP(func(p *rtp.Packet) { seenA++ })
	b.OnWriteRTP(func(p *rtp.Packet) { seenB++ })

	p := &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 8}, Payload: []byte("x")}
	require.NoError(t, a.WriteRTP(p))
	require.NoError(t, a.WriteRTP(p))
	require.NoError(t, b.WriteRTP(p))

	require.Equal(t, 2, seenA)
	require.Equal(t, 1, seenB)
}
