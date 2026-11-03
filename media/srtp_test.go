// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package media

import (
	"bytes"
	"errors"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"

	"github.com/emiago/diago/media/sdp"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/srtp/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSDESKeyParam(t *testing.T) {
	const key = "8Dlz/SyzlAKCZwH49w5DX8S4pDa7Lw0n3LTI4t6Z"
	profile := srtp.ProtectionProfileAes128CmHmacSha1_80

	tests := []struct {
		name     string
		value    string
		lifetime uint64
		err      string
	}{
		{name: "plain", value: "inline:" + key},
		{name: "power of two", value: "inline:" + key + "|2^31", lifetime: 1 << 31},
		{name: "decimal", value: "inline:" + key + "|2147483648", lifetime: 1 << 31},
		{name: "case insensitive method", value: "INLINE:" + key},
		{name: "empty lifetime", value: "inline:" + key + "|", err: "lifetime is empty"},
		{name: "zero", value: "inline:" + key + "|0", err: "must be positive"},
		{name: "leading zero", value: "inline:" + key + "|01", err: "leading zeroes"},
		{name: "power with leading zero", value: "inline:" + key + "|2^01", err: "leading zeroes"},
		{name: "power too large", value: "inline:" + key + "|2^32", err: "exceeds the maximum"},
		{name: "decimal too large", value: "inline:" + key + "|2147483649", err: "exceeds the maximum"},
		{name: "MKI without lifetime", value: "inline:" + key + "|1:4", err: "MKI is not supported"},
		{name: "MKI with lifetime", value: "inline:" + key + "|2^20|1:4", err: "MKI is not supported"},
		{name: "too many inline fields", value: "inline:" + key + "|2^20|1:4|2:4", err: "invalid SDES inline key parameters"},
		{name: "multiple keys", value: "inline:" + key + ";inline:" + key, err: "multiple SDES keys are not supported"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			param, err := parseSDESKeyParam(tt.value, profile)
			if tt.err != "" {
				require.ErrorContains(t, err, tt.err)
				return
			}
			require.NoError(t, err)
			assert.Len(t, param.masterKey, 16)
			assert.Len(t, param.masterSalt, 14)
			assert.Equal(t, tt.lifetime, param.lifetime)
		})
	}
}

func lifetimeTestSession() *MediaSession {
	return &MediaSession{
		Laddr:  net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 4000},
		Codecs: []Codec{CodecAudioAlaw}, Mode: sdp.ModeSendrecv,
		SecureRTP: 1, SRTPAlg: SRTPProfileAes128CmHmacSha1_80,
	}
}

func lifetimeTestOffer(peer *MediaSession, lifetime string) []byte {
	lines := strings.Split(string(peer.LocalSDP()), "\r\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "a=crypto:") && lifetime != "" {
			lines[i] += "|" + lifetime
		}
	}
	return []byte(strings.Join(lines, "\r\n"))
}

func lifetimeTestPacket(t *testing.T, peer *MediaSession, sequence uint16, isRTCP bool) (plain, wire []byte) {
	t.Helper()
	var err error
	if isRTCP {
		plain, err = (&rtcp.SenderReport{SSRC: 42, RTPTime: uint32(sequence)}).Marshal()
	} else {
		plain, err = (&rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 8, SSRC: 42, SequenceNumber: sequence}, Payload: []byte("lifetime")}).Marshal()
	}
	require.NoError(t, err)
	if isRTCP {
		wire, err = peer.localCtxSRTP.EncryptRTCP(nil, plain, nil)
	} else {
		wire, err = peer.localCtxSRTP.EncryptRTP(nil, plain, nil)
	}
	require.NoError(t, err)
	return plain, wire
}

func lifetimeTestDecrypt(receiver *MediaSession, wire []byte, isRTCP bool) ([]byte, error) {
	if isRTCP {
		return receiver.remoteCtxSRTP.DecryptRTCP(nil, wire, nil)
	}
	return receiver.remoteCtxSRTP.DecryptRTP(nil, wire, nil)
}

func lifetimeTestCount(receiver *MediaSession, isRTCP bool) uint64 {
	if isRTCP {
		return receiver.remoteCtxSRTP.currentPolicy.rtcpPackets
	}
	return receiver.remoteCtxSRTP.currentPolicy.rtpPackets
}

func TestSDESLifetimeRefreshAndRekey(t *testing.T) {
	for _, isRTCP := range []bool{false, true} {
		t.Run(fmt.Sprintf("rtcp=%v", isRTCP), func(t *testing.T) {
			peer, receiver := lifetimeTestSession(), lifetimeTestSession()
			require.NoError(t, receiver.RemoteSDP(lifetimeTestOffer(peer, "2")))
			context := receiver.remoteCtxSRTP.current
			plain, wire := lifetimeTestPacket(t, peer, 65535, isRTCP)
			got, err := lifetimeTestDecrypt(receiver, wire, isRTCP)
			require.NoError(t, err)
			require.Equal(t, plain, got)

			plain, wire = lifetimeTestPacket(t, peer, 0, isRTCP)
			corrupt := bytes.Clone(wire)
			corrupt[len(corrupt)-1] ^= 0xff
			_, err = lifetimeTestDecrypt(receiver, corrupt, isRTCP)
			require.Error(t, err)
			require.Equal(t, uint64(1), lifetimeTestCount(receiver, isRTCP))

			fork := receiver.Fork()
			require.NoError(t, fork.RemoteSDP(lifetimeTestOffer(peer, "2^1")))
			require.Same(t, receiver.remoteCtxSRTP, fork.remoteCtxSRTP)
			require.Same(t, context, fork.remoteCtxSRTP.current)
			require.Nil(t, fork.remoteCtxSRTP.pending)
			got, err = lifetimeTestDecrypt(fork, wire, isRTCP)
			require.NoError(t, err)
			require.Equal(t, plain, got)
			_, wire = lifetimeTestPacket(t, peer, 1, isRTCP)
			_, err = lifetimeTestDecrypt(receiver, wire, isRTCP)
			require.ErrorIs(t, err, ErrSRTPKeyLifetimeExceeded)

			// Exhausting the current key must not prevent a fresh candidate from
			// authenticating, promoting, and using its independent packet budget.
			next := lifetimeTestSession()
			require.NoError(t, fork.RemoteSDP(lifetimeTestOffer(next, "1")))
			plain, wire = lifetimeTestPacket(t, next, 10, isRTCP)
			got, err = lifetimeTestDecrypt(receiver, wire, isRTCP)
			require.NoError(t, err)
			require.Equal(t, plain, got)
			require.Same(t, context, receiver.remoteCtxSRTP.previous)
			require.Equal(t, uint64(1), lifetimeTestCount(receiver, isRTCP))
			_, wire = lifetimeTestPacket(t, next, 11, isRTCP)
			_, err = lifetimeTestDecrypt(fork, wire, isRTCP)
			require.ErrorIs(t, err, ErrSRTPKeyLifetimeExceeded)
		})
	}
}

func TestSDESLifetimePendingExpiryAndFallback(t *testing.T) {
	for _, isRTCP := range []bool{false, true} {
		t.Run(fmt.Sprintf("rtcp=%v", isRTCP), func(t *testing.T) {
			peer, next, receiver := lifetimeTestSession(), lifetimeTestSession(), lifetimeTestSession()
			require.NoError(t, receiver.RemoteSDP(lifetimeTestOffer(peer, "3")))
			_, wire := lifetimeTestPacket(t, peer, 1, isRTCP)
			_, err := lifetimeTestDecrypt(receiver, wire, isRTCP)
			require.NoError(t, err)
			fork := receiver.Fork()
			offer := lifetimeTestOffer(next, "1")
			require.NoError(t, fork.RemoteSDP(offer))
			pending := receiver.remoteCtxSRTP.pending
			receiver.remoteCtxSRTP.mu.Lock()
			receiver.remoteCtxSRTP.pendingUntil = time.Now().Add(-time.Second)
			receiver.remoteCtxSRTP.mu.Unlock()
			_, wire = lifetimeTestPacket(t, peer, 2, isRTCP)
			_, err = lifetimeTestDecrypt(receiver, wire, isRTCP)
			require.NoError(t, err)
			require.Nil(t, receiver.remoteCtxSRTP.pending)
			require.Equal(t, uint64(2), lifetimeTestCount(receiver, isRTCP))

			require.NoError(t, fork.RemoteSDP(offer))
			require.NotSame(t, pending, receiver.remoteCtxSRTP.pending)
			_, wire = lifetimeTestPacket(t, next, 10, isRTCP)
			_, err = lifetimeTestDecrypt(receiver, wire, isRTCP)
			require.NoError(t, err)
			_, wire = lifetimeTestPacket(t, peer, 3, isRTCP)
			_, err = lifetimeTestDecrypt(fork, wire, isRTCP)
			require.NoError(t, err, "the old context keeps its remaining budget during grace")
			_, wire = lifetimeTestPacket(t, peer, 4, isRTCP)
			_, err = lifetimeTestDecrypt(receiver, wire, isRTCP)
			require.ErrorIs(t, err, ErrSRTPKeyLifetimeExceeded)
		})
	}
}

func TestSDESLifetimeUpdatePreservesCounters(t *testing.T) {
	peer, receiver := lifetimeTestSession(), lifetimeTestSession()
	require.NoError(t, receiver.RemoteSDP(lifetimeTestOffer(peer, "4")))
	_, wire := lifetimeTestPacket(t, peer, 1, false)
	_, err := lifetimeTestDecrypt(receiver, wire, false)
	require.NoError(t, err)
	context := receiver.remoteCtxSRTP.current
	for _, lifetime := range []string{"2", "4", "2^1", ""} {
		require.NoError(t, receiver.RemoteSDP(lifetimeTestOffer(peer, lifetime)))
		require.Same(t, context, receiver.remoteCtxSRTP.current)
		require.Equal(t, uint64(1), lifetimeTestCount(receiver, false))
		require.Equal(t, uint64(2), receiver.remoteCtxSRTP.currentPolicy.rtpLimit)
	}
	require.Error(t, receiver.RemoteSDP(lifetimeTestOffer(peer, "0")))
	require.Equal(t, uint64(1), lifetimeTestCount(receiver, false))
}

func TestSDESLifetimeConcurrentForks(t *testing.T) {
	peer, receiver := lifetimeTestSession(), lifetimeTestSession()
	require.NoError(t, receiver.RemoteSDP(lifetimeTestOffer(peer, "1")))
	fork := receiver.Fork()
	results := make(chan error, 4)
	start := make(chan struct{})
	for _, isRTCP := range []bool{false, true} {
		for sequence := uint16(1); sequence <= 2; sequence++ {
			plain, wire := lifetimeTestPacket(t, peer, sequence, isRTCP)
			go func() {
				<-start
				got, err := lifetimeTestDecrypt(fork, wire, isRTCP)
				if err == nil && !bytes.Equal(plain, got) {
					err = fmt.Errorf("decrypted packet changed")
				}
				results <- err
			}()
		}
	}
	close(start)
	successes := 0
	for i := 0; i < 4; i++ {
		if err := <-results; err == nil {
			successes++
		} else {
			require.True(t, errors.Is(err, ErrSRTPKeyLifetimeExceeded), "%v", err)
		}
	}
	require.Equal(t, 2, successes, "one RTP and one RTCP packet share each key's independent budgets")
	require.Equal(t, uint64(1), lifetimeTestCount(receiver, false))
	require.Equal(t, uint64(1), lifetimeTestCount(receiver, true))
}
