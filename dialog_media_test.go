// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"errors"
	"fmt"
	"net"
	"testing"

	"github.com/emiago/diago/media"
	"github.com/emiago/diago/media/sdp"
	"github.com/emiago/sipgo/sip"
	"github.com/stretchr/testify/require"
)

// fakeServerTransaction records the response handleMediaUpdate builds.
type fakeServerTransaction struct {
	res *sip.Response
}

func (t *fakeServerTransaction) Respond(res *sip.Response) error      { t.res = res; return nil }
func (t *fakeServerTransaction) Acks() <-chan *sip.Request            { return nil }
func (t *fakeServerTransaction) OnCancel(f sip.FnTxCancel) bool       { return false }
func (t *fakeServerTransaction) OnTerminate(f sip.FnTxTerminate) bool { return false }
func (t *fakeServerTransaction) Terminate()                           {}
func (t *fakeServerTransaction) Done() <-chan struct{}                { return nil }
func (t *fakeServerTransaction) Err() error                           { return nil }

func newReInvite(t *testing.T, body []byte) *sip.Request {
	t.Helper()
	req := sip.NewRequest(sip.INVITE, sip.Uri{User: "alice", Host: "127.0.0.1"})
	req.AppendHeader(&sip.ContactHeader{Address: sip.Uri{User: "bob", Host: "127.0.0.2"}})
	if body != nil {
		req.SetBody(body)
	}
	return req
}

func newMediaSessionForTest(t *testing.T) *media.MediaSession {
	t.Helper()
	sess := &media.MediaSession{
		Codecs: []media.Codec{media.CodecAudioUlaw},
		Laddr:  net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0},
		Mode:   sdp.ModeSendrecv,
	}
	require.NoError(t, sess.Init())
	t.Cleanup(func() { sess.Close() })
	return sess
}

// fakePortAllocator hands out ports from a fixed list and records releases.
type fakePortAllocator struct {
	ports    []int
	next     int
	err      error
	released []int
}

func (a *fakePortAllocator) AllocateRTPPort() (int, error) {
	if a.err != nil {
		return 0, a.err
	}
	p := a.ports[a.next]
	a.next++
	return p, nil
}

func (a *fakePortAllocator) ReleaseRTPPort(port int) { a.released = append(a.released, port) }

// freeEvenPort returns an even port that is free together with its RTCP companion.
func freeEvenPort(t *testing.T, ip net.IP) int {
	t.Helper()
	for i := 0; i < 50; i++ {
		c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: ip, Port: 0})
		require.NoError(t, err)
		port := c.LocalAddr().(*net.UDPAddr).Port
		c.Close()
		if port%2 != 0 {
			continue
		}
		// The RTCP companion must be free too, as MediaSession binds the pair.
		c2, err := net.ListenUDP("udp4", &net.UDPAddr{IP: ip, Port: port + 1})
		if err != nil {
			continue
		}
		c2.Close()
		return port
	}
	t.Fatal("no free even port pair found")
	return 0
}

func TestInitMediaSessionRTPPortAllocator(t *testing.T) {
	bindIP := net.IPv4(127, 0, 0, 1)

	t.Run("BindsAllocatedPortAndReleasesOnClose", func(t *testing.T) {
		// A probed port can be taken by anything else before Init binds it, so a
		// lost race is retried rather than reported as a failure.
		var d *DialogMedia
		var alloc *fakePortAllocator
		var port int
		for attempt := 0; ; attempt++ {
			port = freeEvenPort(t, bindIP)
			alloc = &fakePortAllocator{ports: []int{port}}
			d = &DialogMedia{}
			err := d.initMediaSessionFromConf(MediaConfig{
				Codecs:           []media.Codec{media.CodecAudioUlaw},
				bindIP:           bindIP,
				RTPPortAllocator: alloc,
			})
			if err == nil {
				break
			}
			require.Less(t, attempt, 10, "could not bind a probed free port: %v", err)
		}
		// The allocated port is what actually got bound, not an OS ephemeral one.
		require.Equal(t, port, d.mediaSession.Laddr.Port)
		require.Empty(t, alloc.released, "port released before Close")

		require.NoError(t, d.Close())
		require.Equal(t, []int{port}, alloc.released)

		// Close is latched, so the allocator is never handed the port twice.
		require.NoError(t, d.Close())
		require.Equal(t, []int{port}, alloc.released)
	})

	// A failed Init never reaches Close, so the port has to go back right away
	// or it leaks out of the pool for the life of the process.
	t.Run("ReleasesWhenInitFails", func(t *testing.T) {
		alloc := &fakePortAllocator{ports: []int{0}}
		d := &DialogMedia{}
		err := d.initMediaSessionFromConf(MediaConfig{
			Codecs: []media.Codec{media.CodecAudioUlaw},
			// TEST-NET-3, assigned to no interface, so the bind always fails.
			bindIP:           net.IPv4(203, 0, 113, 1),
			RTPPortAllocator: alloc,
		})
		require.Error(t, err)
		require.Equal(t, []int{0}, alloc.released)
	})

	// An allocator with nothing left fails the call rather than silently falling
	// back to an unbounded ephemeral port.
	t.Run("AllocationErrorFailsInit", func(t *testing.T) {
		alloc := &fakePortAllocator{err: errors.New("pool exhausted")}
		d := &DialogMedia{}
		err := d.initMediaSessionFromConf(MediaConfig{
			Codecs:           []media.Codec{media.CodecAudioUlaw},
			bindIP:           bindIP,
			RTPPortAllocator: alloc,
		})
		require.ErrorContains(t, err, "pool exhausted")
		require.Nil(t, d.mediaSession)
		require.Empty(t, alloc.released, "nothing was allocated, so nothing to release")
	})

	// Nil allocator keeps the historical OS/globals behaviour.
	t.Run("NilAllocatorLeavesPortToOS", func(t *testing.T) {
		d := &DialogMedia{}
		require.NoError(t, d.initMediaSessionFromConf(MediaConfig{
			Codecs: []media.Codec{media.CodecAudioUlaw},
			bindIP: bindIP,
		}))
		defer d.Close()
		require.Nil(t, d.releaseRTPPort)
		require.NotZero(t, d.mediaSession.Laddr.Port)
	})
}

// requireLocalSDP asserts the response replays our published media. LocalSDP
// regenerates the o= session-version on every call, so the bytes can not be
// compared to a second LocalSDP call; the port and codec identify it instead.
func requireLocalSDP(t *testing.T, d *DialogMedia, res *sip.Response) {
	t.Helper()
	require.Equal(t, "application/sdp", res.ContentType().Value())
	require.Contains(t, string(res.Body()),
		fmt.Sprintf("m=audio %d RTP/AVP 0", d.mediaSession.Laddr.Port))
}

// An offer-less re-INVITE is a request for an offer (RFC 3261 14.2): it must be
// answered with the SDP we already published, not rejected.
func TestHandleMediaUpdateOfferless(t *testing.T) {
	contactHDR := &sip.ContactHeader{Address: sip.Uri{User: "us", Host: "127.0.0.1"}}

	t.Run("ReplaysLocalSDP", func(t *testing.T) {
		d := &DialogMedia{mediaSession: newMediaSessionForTest(t)}
		tx := &fakeServerTransaction{}

		require.NoError(t, d.handleMediaUpdate(newReInvite(t, nil), tx, contactHDR))
		require.Equal(t, sip.StatusOK, tx.res.StatusCode)
		requireLocalSDP(t, d, tx.res)
	})

	// Content-Length: 0 can reach us as an empty but non nil body, which a nil
	// check lets through into the SDP parser. It finds no m= line and the peer is
	// told its legal request was rejected.
	t.Run("EmptyNonNilBodyReplaysLocalSDP", func(t *testing.T) {
		d := &DialogMedia{mediaSession: newMediaSessionForTest(t)}
		tx := &fakeServerTransaction{}

		require.NoError(t, d.handleMediaUpdate(newReInvite(t, []byte{}), tx, contactHDR))
		require.Equal(t, sip.StatusOK, tx.res.StatusCode)
		requireLocalSDP(t, d, tx.res)
	})

	// A request for an offer can not be answered with an SDP we never built. The
	// bodied path already rejects this; the offer-less path must not instead
	// dereference the nil session.
	t.Run("NoMediaSessionIsRejected", func(t *testing.T) {
		d := &DialogMedia{}
		tx := &fakeServerTransaction{}

		require.NoError(t, d.handleMediaUpdate(newReInvite(t, nil), tx, contactHDR))
		require.Equal(t, sip.StatusRequestTerminated, tx.res.StatusCode)
	})

	// Control: a re-INVITE that does carry an offer must still be negotiated,
	// never swallowed by the offer-less branch.
	t.Run("BodiedOfferIsStillNegotiated", func(t *testing.T) {
		d := &DialogMedia{}
		tx := &fakeServerTransaction{}

		require.NoError(t, d.handleMediaUpdate(newReInvite(t, []byte("v=0\r\n")), tx, contactHDR))
		require.Equal(t, sip.StatusRequestTerminated, tx.res.StatusCode)
		require.Contains(t, tx.res.Reason, "no media session present")
	})
}
