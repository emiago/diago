// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package media

import (
	"bytes"
	"io"
	"net"
	"testing"

	"github.com/emiago/sipgo/fakes"
	"github.com/pion/rtp"
	"github.com/pion/srtp/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These tests pin RFC 3711 section 3.3: a packet that fails SRTP authentication
// MUST be discarded, and the session MUST keep reading.
//
// Why they exist: this was the audio bug. Asterisk emits exactly one unprotected
// RTP packet about a millisecond after the DTLS handshake completes -- a 172-byte
// datagram (12-byte header + 160 bytes of PCMU, no auth tag) that arrives on the
// media socket before its own SRTP send context is armed. ReadRTP treated the
// resulting authentication failure as a transport error and returned it. One
// packet upstream, telephony/audio_stream.go saw the read fail and stopped
// reading for the rest of the call. A single stray datagram killed audio on a
// session that was otherwise perfectly healthy, and it did so silently.
//
// The distinction that matters, and that these tests hold: a *transport* error
// is the session's -- there is nothing left to read, so it propagates. A packet
// that merely fails to authenticate is one bad packet among good ones. Treating
// the second like the first hands any peer that can reach the port -- or any
// peer with a benign implementation quirk, which is what Asterisk has -- a
// one-datagram mute button for the whole call.

// srtpPeers returns a send context and a receiver session keyed with it, so that
// sendCtx encrypts exactly what receiver.remoteCtxSRTP decrypts.
//
// The keys are built directly rather than by running Init and an SDES exchange,
// as TestMediaSRTP does. Init allocates a real socket from a 5000:5010 range,
// and the sessions in this package are never closed, so going that route makes
// these tests fail with "no available ports in range" depending on what ran
// first. ReadRTP's discard behaviour needs neither a socket nor a negotiation --
// only a receive context and something to read from -- so not asking for them
// keeps the tests hermetic and order-independent.
func srtpPeers(t *testing.T) (sendCtx *srtp.Context, receiver *MediaSession) {
	t.Helper()

	// Deterministic key material: this exercises authentication, not key
	// agreement, and a fixed key makes a failure reproducible.
	key := bytes.Repeat([]byte{0x2a}, 16)
	salt := bytes.Repeat([]byte{0x3b}, 14)
	profile := srtp.ProtectionProfileAes128CmHmacSha1_80

	sendCtx, err := srtp.CreateContext(key, salt, profile)
	require.NoError(t, err, "building the sender's SRTP context")

	recvCtx, err := srtp.CreateContext(key, salt, profile)
	require.NoError(t, err, "building the receiver's SRTP context")

	receiver = &MediaSession{
		Laddr:         net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)},
		Codecs:        []Codec{CodecAudioUlaw},
		SecureRTP:     1,
		SRTPAlg:       SRTPProfileAes128CmHmacSha1_80,
		remoteCtxSRTP: recvCtx,
	}

	return sendCtx, receiver
}

// asteriskPlainRTP builds the packet that caused the outage: version 2, payload
// type 0 (PCMU), a 12-byte header and 160 bytes of payload for 172 total, and no
// SRTP auth tag. The observed datagram was len=172 b0=128 pt=0 ver=2.
func asteriskPlainRTP(t *testing.T) []byte {
	t.Helper()

	raw, err := (&rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    0,
			SequenceNumber: 4242,
			Timestamp:      160,
			SSRC:           0x11223344,
		},
		Payload: make([]byte, 160),
	}).Marshal()
	require.NoError(t, err)
	require.Len(t, raw, 172, "the regression packet is 12 header bytes plus 160 of PCMU")
	require.EqualValues(t, 128, raw[0], "first byte must be 0x80: version 2, no padding, no extension")

	return raw
}

// feedRTP gives the session a socket that yields exactly the given datagrams.
// io.Pipe is used rather than a bytes.Buffer because each Read must return one
// whole write -- a Buffer would hand ReadFrom two packets in a single read and
// destroy the datagram framing the bug depends on.
func feedRTP(t *testing.T, s *MediaSession, datagrams ...[]byte) {
	t.Helper()

	reader, writer := io.Pipe()
	t.Cleanup(func() { _ = writer.Close() })

	s.rtpConn = &fakes.UDPConn{
		LAddr:  s.Laddr,
		RAddr:  net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 5060},
		Reader: reader,
	}

	go func() {
		for _, d := range datagrams {
			if _, err := writer.Write(d); err != nil {
				return
			}
		}
	}()
}

// encryptFor protects pkt with the sender's context, producing bytes the
// receiver's context accepts.
func encryptFor(t *testing.T, sendCtx *srtp.Context, pkt *rtp.Packet) []byte {
	t.Helper()

	raw, err := pkt.Marshal()
	require.NoError(t, err)

	enc, err := sendCtx.EncryptRTP(nil, raw, &pkt.Header)
	require.NoError(t, err)

	return enc
}

// TestReadRTPDiscardsPacketFailingSRTPAuth is the regression itself. A stray
// unprotected packet must not be visible to the caller at all: ReadRTP must skip
// it and return the next packet that authenticates. Before the fix this returned
// the decrypt error, and the audio path above it stopped reading for good.
func TestReadRTPDiscardsPacketFailingSRTPAuth(t *testing.T) {
	sender, receiver := srtpPeers(t)

	want := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    0,
			SequenceNumber: 4243,
			Timestamp:      320,
			SSRC:           0x11223344,
		},
		Payload: []byte("the call must survive the stray packet"),
	}

	// The exact production order: Asterisk's unprotected packet lands first, then
	// real media follows once its send context is armed.
	feedRTP(t, receiver, asteriskPlainRTP(t), encryptFor(t, sender, want))

	var got rtp.Packet
	n, err := receiver.ReadRTP(make([]byte, RTPBufSize), &got)

	require.NoError(t, err, "a packet failing SRTP auth must be discarded, not surfaced as a read error: "+
		"RFC 3711 section 3.3 requires discarding it, and returning it here stops the audio path "+
		"from ever reading again -- one stray datagram from the peer mutes the whole call")
	assert.Equal(t, want.Payload, got.Payload, "ReadRTP must return the next packet that authenticates")
	assert.Positive(t, n, "the discarded packet must not be reported as the read length")
	assert.EqualValues(t, 1, receiver.srtpAuthFailures.Load(),
		"the discard must be counted exactly once so the condition is visible in diagnostics")
}

// TestReadRTPDiscardsEveryFailingPacket pins that the discard is a loop and not a
// single-packet allowance. A peer that sends several unprotected packets -- or an
// attacker spraying the port -- must not be able to stall the reader either.
func TestReadRTPDiscardsEveryFailingPacket(t *testing.T) {
	sender, receiver := srtpPeers(t)

	want := &rtp.Packet{
		Header: rtp.Header{
			Version:        2,
			PayloadType:    0,
			SequenceNumber: 9000,
			Timestamp:      640,
			SSRC:           0x11223344,
		},
		Payload: []byte("still here"),
	}

	feedRTP(t, receiver,
		asteriskPlainRTP(t),
		asteriskPlainRTP(t),
		asteriskPlainRTP(t),
		encryptFor(t, sender, want),
	)

	var got rtp.Packet
	_, err := receiver.ReadRTP(make([]byte, RTPBufSize), &got)

	require.NoError(t, err, "the discard must loop: any single failing packet stopping the read is the bug")
	assert.Equal(t, want.Payload, got.Payload)
	assert.EqualValues(t, 3, receiver.srtpAuthFailures.Load(), "every discarded packet must be counted")
}

// TestReadRTPPropagatesTransportError pins the other half of the distinction, so
// that "discard and continue" cannot be over-applied. When the socket itself is
// done there is nothing left to read, and spinning the loop on it would burn a
// core for the life of the call. That error must still reach the caller.
func TestReadRTPPropagatesTransportError(t *testing.T) {
	_, receiver := srtpPeers(t)

	// No datagrams: the pipe writer is closed by cleanup, but close it now to
	// make the socket report EOF on the first read.
	feedRTP(t, receiver)

	if real := receiver.rtpConn; real != nil {
		if fake, ok := real.(*fakes.UDPConn); ok {
			if pr, ok := fake.Reader.(*io.PipeReader); ok {
				require.NoError(t, pr.Close())
			}
		}
	}

	var got rtp.Packet
	_, err := receiver.ReadRTP(make([]byte, RTPBufSize), &got)

	require.Error(t, err, "a transport error is the session's -- there is nothing left to read, "+
		"so it must propagate rather than spin the discard loop")
	assert.Zero(t, receiver.srtpAuthFailures.Load(), "a transport error is not an authentication failure")
}
