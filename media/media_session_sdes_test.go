// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2026, Emir Aganovic

package media

import (
	"bytes"
	"encoding/base64"
	"encoding/binary"
	"fmt"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emiago/diago/media/sdp"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/srtp/v3"
	"github.com/stretchr/testify/require"
)

// A packet transport with an observable read boundary makes rekey tests
// deterministic: the test knows ReadFrom was entered before updating the SDP.
type sdesTestConn struct {
	incoming    chan []byte
	written     chan sdesTestDatagram
	readStarted chan struct{}
	closed      chan struct{}
	closeOnce   sync.Once
	addr        net.UDPAddr
}

type sdesTestDatagram struct {
	data []byte
	addr net.Addr
}

func newSDESTestConn(addr net.UDPAddr) *sdesTestConn {
	return &sdesTestConn{
		incoming: make(chan []byte, 1024), written: make(chan sdesTestDatagram, 1024),
		readStarted: make(chan struct{}, 1024), closed: make(chan struct{}), addr: addr,
	}
}

func (c *sdesTestConn) ReadFrom(b []byte) (int, net.Addr, error) {
	select {
	case c.readStarted <- struct{}{}:
	default:
	}
	select {
	case p := <-c.incoming:
		return copy(b, p), &c.addr, nil
	case <-c.closed:
		return 0, nil, io.EOF
	}
}

func (c *sdesTestConn) WriteTo(b []byte, addr net.Addr) (int, error) {
	select {
	case c.written <- sdesTestDatagram{data: bytes.Clone(b), addr: addr}:
		return len(b), nil
	case <-c.closed:
		return 0, io.ErrClosedPipe
	}
}

func (c *sdesTestConn) Close() error {
	c.closeOnce.Do(func() { close(c.closed) })
	return nil
}
func (c *sdesTestConn) LocalAddr() net.Addr              { return &c.addr }
func (c *sdesTestConn) SetDeadline(time.Time) error      { return nil }
func (c *sdesTestConn) SetReadDeadline(time.Time) error  { return nil }
func (c *sdesTestConn) SetWriteDeadline(time.Time) error { return nil }

func newSDESTestSession(t *testing.T, port int) *MediaSession {
	t.Helper()
	addr := net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: port}
	m := &MediaSession{
		Laddr: addr, Codecs: []Codec{CodecAudioAlaw}, Mode: sdp.ModeSendrecv,
		SecureRTP: 1, SRTPAlg: SRTPProfileAes128CmHmacSha1_80,
		rtpConn: newSDESTestConn(addr), rtcpConn: newSDESTestConn(net.UDPAddr{IP: addr.IP, Port: port + 1}),
	}
	t.Cleanup(func() { require.NoError(t, m.Close()) })
	return m
}

func negotiateSDESTestPair(t *testing.T) (*MediaSession, *MediaSession) {
	t.Helper()
	a, b := newSDESTestSession(t, 10000), newSDESTestSession(t, 20000)
	require.NoError(t, b.RemoteSDP(a.LocalSDP()))
	require.NoError(t, a.RemoteSDP(b.LocalSDP()))
	return a, b
}

func sdesTestCrypto(t *testing.T, body []byte) string {
	t.Helper()
	for _, line := range strings.Split(string(body), "\r\n") {
		if strings.HasPrefix(line, "a=crypto:") {
			return line
		}
	}
	t.Fatal("SDP has no crypto attribute")
	return ""
}

func sdesTestOffer(addr net.UDPAddr, key []byte, tag int) []byte {
	return generateSDPForAudio(123, 124, "RTP/SAVP", addr.IP, addr.IP, addr.Port, sdp.ModeSendrecv,
		[]Codec{CodecAudioAlaw}, sdesInline{alg: "AES_CM_128_HMAC_SHA1_80", base64: base64.StdEncoding.EncodeToString(key), tag: tag}, nil)
}

func sdesTestContext(t *testing.T, key []byte) *srtp.Context {
	t.Helper()
	ctx, err := srtp.CreateContext(key[:16], key[16:], srtp.ProtectionProfileAes128CmHmacSha1_80)
	require.NoError(t, err)
	return ctx
}

func sdesTestPacket(seq uint16, ssrc uint32) *rtp.Packet {
	return &rtp.Packet{Header: rtp.Header{Version: 2, PayloadType: 8, SequenceNumber: seq, SSRC: ssrc, Timestamp: uint32(seq) * 160}, Payload: []byte("authenticated audio")}
}

func sdesTestReport(seq uint16, ssrc uint32) *rtcp.SenderReport {
	return &rtcp.SenderReport{SSRC: ssrc, RTPTime: 0x12345678, PacketCount: uint32(seq)}
}

func encryptSDESTestPacket(t *testing.T, ctx *srtp.Context, isRTCP bool, seq uint16) []byte {
	t.Helper()
	if isRTCP {
		data, err := sdesTestReport(seq, 37).Marshal()
		require.NoError(t, err)
		wire, err := ctx.EncryptRTCP(nil, data, nil)
		require.NoError(t, err)
		return wire
	}
	data, err := sdesTestPacket(seq, 37).Marshal()
	require.NoError(t, err)
	wire, err := ctx.EncryptRTP(nil, data, nil)
	require.NoError(t, err)
	return wire
}

type sdesTestRead struct {
	n       int
	seq     uint16
	payload []byte
	err     error
}

func readSDESTestPacket(m *MediaSession, isRTCP bool) sdesTestRead {
	if isRTCP {
		pkts := make([]rtcp.Packet, 5)
		n, err := m.ReadRTCP(make([]byte, RTPBufSize), pkts)
		result := sdesTestRead{n: n, err: err}
		if n > 0 && err == nil {
			report, ok := pkts[0].(*rtcp.SenderReport)
			if !ok || report.RTPTime != 0x12345678 {
				result.err = fmt.Errorf("unexpected authenticated RTCP packet: %v", pkts[0])
			} else {
				result.seq = uint16(report.PacketCount)
			}
		}
		return result
	}
	// Reusing a packet must not return its previous payload on authentication failure.
	pkt := rtp.Packet{Payload: []byte("previous payload")}
	n, err := m.ReadRTP(make([]byte, RTPBufSize), &pkt)
	return sdesTestRead{n: n, seq: pkt.SequenceNumber, payload: pkt.Payload, err: err}
}

func sdesTestTransport(m *MediaSession, isRTCP bool) *sdesTestConn {
	if isRTCP {
		return m.rtcpConn.(*sdesTestConn)
	}
	return m.rtpConn.(*sdesTestConn)
}

func receiveSDESTestPacket(t *testing.T, m *MediaSession, isRTCP bool, wire []byte, seq uint16, wantError bool) {
	t.Helper()
	sdesTestTransport(m, isRTCP).incoming <- bytes.Clone(wire)
	result := readSDESTestPacket(m, isRTCP)
	if wantError {
		require.Error(t, result.err)
		require.Zero(t, result.n)
		require.Empty(t, result.payload)
		return
	}
	require.NoError(t, result.err)
	require.Positive(t, result.n)
	require.Equal(t, seq, result.seq)
	if !isRTCP {
		require.Equal(t, []byte("authenticated audio"), result.payload)
	}
}

func exchangeSDESTestRTP(t *testing.T, from, to *MediaSession, seq uint16, ssrc uint32) {
	t.Helper()
	pkt := sdesTestPacket(seq, ssrc)
	require.NoError(t, from.WriteRTP(pkt))
	wire := <-sdesTestTransport(from, false).written
	plain, err := pkt.Marshal()
	require.NoError(t, err)
	require.Len(t, wire.data, len(plain)+10)
	require.NotEqual(t, plain[12:], wire.data[12:len(plain)])
	receiveSDESTestPacket(t, to, false, wire.data, seq, false)
}

func exchangeSDESTestRTCP(t *testing.T, from, to *MediaSession, index uint32, compound bool) {
	t.Helper()
	report := sdesTestReport(uint16(index), 51)
	if compound {
		require.NoError(t, from.WriteRTCPs([]rtcp.Packet{report, &rtcp.ReceiverReport{SSRC: 52}}))
	} else {
		require.NoError(t, from.WriteRTCP(report))
	}
	wire := <-sdesTestTransport(from, true).written
	// AES_CM_128_HMAC_SHA1_80 carries a four-byte E/index followed by ten auth bytes.
	gotIndex := binary.BigEndian.Uint32(wire.data[len(wire.data)-14 : len(wire.data)-10])
	require.Equal(t, uint32(0x80000000)|index, gotIndex)
	sdesTestTransport(to, true).incoming <- wire.data
	pkts := make([]rtcp.Packet, 5)
	n, err := to.ReadRTCP(make([]byte, RTPBufSize), pkts)
	require.NoError(t, err)
	wantCount := 1
	if compound {
		wantCount = 2
	}
	require.Equal(t, wantCount, n)
	require.Equal(t, report, pkts[0])
}

func TestSDESRefreshContinuity(t *testing.T) {
	for _, initiatingRole := range []string{"offerer", "answerer"} {
		for _, refreshAfterRollover := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/after_rollover=%v", initiatingRole, refreshAfterRollover), func(t *testing.T) {
				a, b := negotiateSDESTestPair(t)
				aKey, bKey := sdesTestCrypto(t, a.LocalSDP()), sdesTestCrypto(t, b.LocalSDP())
				aSend, bSend := a.sdes.local, b.sdes.local
				aRecv, bRecv := a.sdes.remote.ctx, b.sdes.remote.ctx
				refreshAt := 1
				if refreshAfterRollover {
					refreshAt = 2
				}
				for i, seq := range []uint16{65534, 65535, 0, 1, 2} {
					exchangeSDESTestRTP(t, a, b, seq, 101)
					exchangeSDESTestRTP(t, b, a, seq, 102)
					exchangeSDESTestRTCP(t, a, b, uint32(i+1), i%2 == 1)
					exchangeSDESTestRTCP(t, b, a, uint32(i+1), i%2 == 1)
					if i == refreshAt {
						initiator, responder := a, b
						if initiatingRole == "answerer" {
							initiator, responder = b, a
						}
						fresh, peer := initiator.Fork(), responder.Fork()
						require.Same(t, initiator.sdes, fresh.sdes)
						require.Equal(t, initiator.SecureRTP, fresh.SecureRTP)
						require.Equal(t, initiator.SRTPAlg, fresh.SRTPAlg)
						require.Equal(t, initiator.remoteProto, fresh.remoteProto)
						require.Equal(t, initiator.srtpRemoteTag, fresh.srtpRemoteTag)
						require.Equal(t, initiator.Raddr, fresh.Raddr)
						// A changed crypto tag selects metadata, rather than a new key/context.
						offer := bytes.ReplaceAll(fresh.LocalSDP(), []byte("crypto:1 "), []byte("crypto:7 "))
						require.NoError(t, peer.RemoteSDP(offer))
						answer := peer.LocalSDP()
						require.Contains(t, string(answer), "a=crypto:7 ")
						require.NoError(t, fresh.RemoteSDP(answer))
						require.Equal(t, 7, fresh.srtpRemoteTag)
						if initiatingRole == "answerer" {
							b, a = fresh, peer
						} else {
							a, b = fresh, peer
						}
					}
					// Repeated generation on both the original negotiation and its forks.
					for repeat := 0; repeat < 3; repeat++ {
						for _, pair := range []struct {
							m   *MediaSession
							key string
						}{{a, aKey}, {b, bKey}} {
							crypto := sdesTestCrypto(t, pair.m.LocalSDP())
							require.Equal(t, strings.Fields(pair.key)[2], strings.Fields(crypto)[2])
						}
					}
					require.Same(t, aSend, a.sdes.local)
					require.Same(t, bSend, b.sdes.local)
					require.Same(t, aRecv, a.sdes.remote.ctx)
					require.Same(t, bRecv, b.sdes.remote.ctx)
				}
				for _, pair := range []struct {
					ctx  *srtp.Context
					ssrc uint32
				}{{aSend, 101}, {bRecv, 101}, {bSend, 102}, {aRecv, 102}} {
					roc, exists := pair.ctx.ROC(pair.ssrc)
					require.True(t, exists)
					require.Equal(t, uint32(1), roc)
				}
				for _, ctx := range []*srtp.Context{aSend, bSend} {
					index, exists := ctx.Index(51)
					require.True(t, exists)
					require.Equal(t, uint32(5), index)
				}
			})
		}
	}
}

func TestSDESBlockedReadRekey(t *testing.T) {
	for _, isRTCP := range []bool{false, true} {
		t.Run(fmt.Sprintf("rtcp=%v", isRTCP), func(t *testing.T) {
			m := newSDESTestSession(t, 10000)
			addr := net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 20000}
			oldKey, newKey := bytes.Repeat([]byte{0x11}, 30), bytes.Repeat([]byte{0x22}, 30)
			require.NoError(t, m.RemoteSDP(sdesTestOffer(addr, oldKey, 1)))
			m.LocalSDP()
			sibling, updating := m.Fork(), m.Fork()
			result := make(chan sdesTestRead, 1)
			go func() { result <- readSDESTestPacket(m, isRTCP) }()
			select {
			case <-sdesTestTransport(m, isRTCP).readStarted:
			case <-time.After(5 * time.Second):
				t.Fatal("original session did not block in ReadFrom")
			}
			require.NoError(t, updating.RemoteSDP(sdesTestOffer(addr, newKey, 2)))
			ctx := sdesTestContext(t, newKey)
			sdesTestTransport(m, isRTCP).incoming <- encryptSDESTestPacket(t, ctx, isRTCP, 1)
			select {
			case got := <-result:
				require.NoError(t, got.err)
				require.Positive(t, got.n)
				require.Equal(t, uint16(1), got.seq)
				if !isRTCP {
					require.Equal(t, []byte("authenticated audio"), got.payload)
				}
			case <-time.After(5 * time.Second):
				t.Fatal("rekeyed original read did not finish")
			}
			// An independently existing fork sees the same update too.
			receiveSDESTestPacket(t, sibling, isRTCP, encryptSDESTestPacket(t, ctx, isRTCP, 2), 2, false)
		})
	}
}

func TestSDESReceiveOverlap(t *testing.T) {
	for _, startsWithRTCP := range []bool{false, true} {
		t.Run(fmt.Sprintf("starts_with_rtcp=%v", startsWithRTCP), func(t *testing.T) {
			m := newSDESTestSession(t, 10000)
			addr := net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 20000}
			oldKey, newKey := bytes.Repeat([]byte{0x11}, 30), bytes.Repeat([]byte{0x22}, 30)
			require.NoError(t, m.RemoteSDP(sdesTestOffer(addr, oldKey, 1)))
			var elapsed atomic.Int64
			epoch := time.Unix(1000, 0)
			m.sdes.now = func() time.Time { return epoch.Add(time.Duration(elapsed.Load())) }
			oldCtx, newCtx := sdesTestContext(t, oldKey), sdesTestContext(t, newKey)
			previous := m.sdes.remote
			fork := m.Fork()
			require.NoError(t, fork.RemoteSDP(sdesTestOffer(addr, newKey, 2)))
			active := m.sdes.remote
			require.Same(t, previous, m.sdes.working)
			elapsed.Store(int64(10 * time.Second))
			for _, isRTCP := range []bool{false, true} {
				// Arbitrary time since signaling does not expire the old key.
				receiveSDESTestPacket(t, m, isRTCP, encryptSDESTestPacket(t, oldCtx, isRTCP, 1), 1, false)
				for _, ctx := range []*srtp.Context{oldCtx, newCtx} {
					bad := encryptSDESTestPacket(t, ctx, isRTCP, 2)
					bad[len(bad)-1] ^= 0xff
					receiveSDESTestPacket(t, m, isRTCP, bad, 0, true)
					require.True(t, sdesTestRetirementDeadline(m, previous).IsZero(), "failed authentication must not start overlap")
					require.Same(t, active, m.sdes.remote)
				}
			}
			receiveSDESTestPacket(t, m, startsWithRTCP, encryptSDESTestPacket(t, newCtx, startsWithRTCP, 3), 3, false)
			deadline := epoch.Add(15 * time.Second)
			require.Equal(t, deadline, sdesTestRetirementDeadline(m, previous))
			elapsed.Store(int64(14 * time.Second))
			require.NoError(t, fork.RemoteSDP(sdesTestOffer(addr, newKey, 3)))
			require.Same(t, active, m.sdes.remote)
			require.Equal(t, deadline, sdesTestRetirementDeadline(m, previous), "same-key SDP must not restart overlap")
			for _, isRTCP := range []bool{false, true} {
				receiveSDESTestPacket(t, fork, isRTCP, encryptSDESTestPacket(t, oldCtx, isRTCP, 4), 4, false)
				for _, ctx := range []*srtp.Context{oldCtx, newCtx} {
					bad := encryptSDESTestPacket(t, ctx, isRTCP, 5)
					bad[len(bad)-1] ^= 0xff
					receiveSDESTestPacket(t, m, isRTCP, bad, 0, true)
					require.Equal(t, deadline, sdesTestRetirementDeadline(m, previous), "failed authentication must not extend overlap")
					require.Same(t, active, m.sdes.remote)
				}
				receiveSDESTestPacket(t, fork, isRTCP, encryptSDESTestPacket(t, newCtx, isRTCP, 6), 6, false)
				require.Equal(t, deadline, sdesTestRetirementDeadline(m, previous), "new-key traffic must not extend overlap")
			}
			elapsed.Store(int64(15 * time.Second))
			for _, isRTCP := range []bool{false, true} {
				receiveSDESTestPacket(t, m, isRTCP, encryptSDESTestPacket(t, oldCtx, isRTCP, 7), 0, true)
				receiveSDESTestPacket(t, fork, isRTCP, encryptSDESTestPacket(t, newCtx, isRTCP, 7), 7, false)
				require.Same(t, active, m.sdes.remote)
			}
		})
	}
}

func TestSDESRejectUnsafeUpdates(t *testing.T) {
	for _, forkUpdate := range []bool{false, true} {
		for _, change := range []string{"plaintext", "ip", "port", "malformed_crypto", "unsupported_crypto"} {
			t.Run(fmt.Sprintf("fork=%v/%s", forkUpdate, change), func(t *testing.T) {
				a, b := negotiateSDESTestPair(t)
				target := a
				if forkUpdate {
					target = a.Fork()
				}
				beforeAddr, beforeRTCPAddr := target.Raddr, target.rtcpRaddr
				beforeCrypto := sdesTestCrypto(t, target.LocalSDP())
				beforeCtx, beforeTag, beforeProto := a.sdes.remote, target.srtpRemoteTag, target.remoteProto
				beforeVersion, beforeMode := target.sessionVersion, target.mode
				beforeCodecs := append([]Codec(nil), target.filterCodecs...)
				body := string(b.LocalSDP())
				switch change {
				case "plaintext":
					body = strings.ReplaceAll(body, "RTP/SAVP", "RTP/AVP")
					body = strings.Replace(body, sdesTestCrypto(t, []byte(body))+"\r\n", "", 1)
				case "ip":
					body = strings.ReplaceAll(body, "c=IN IP4 127.0.0.1", "c=IN IP4 127.0.0.2")
				case "port":
					body = strings.ReplaceAll(body, "m=audio 20000 ", "m=audio 20002 ")
				case "malformed_crypto":
					body = strings.ReplaceAll(body, "inline:", "inline:!")
				case "unsupported_crypto":
					body = strings.ReplaceAll(body, "AES_CM_128_HMAC_SHA1_80", "UNSUPPORTED_SUITE")
				}
				require.Error(t, target.RemoteSDP([]byte(body)))
				require.Equal(t, beforeAddr, target.Raddr)
				require.Equal(t, beforeRTCPAddr, target.rtcpRaddr)
				require.Equal(t, beforeTag, target.srtpRemoteTag)
				require.Equal(t, beforeProto, target.remoteProto)
				require.Equal(t, beforeVersion, target.sessionVersion)
				require.Equal(t, beforeMode, target.mode)
				require.Equal(t, beforeCodecs, target.filterCodecs)
				require.Same(t, beforeCtx, a.sdes.remote)
				require.Equal(t, beforeCrypto, sdesTestCrypto(t, target.LocalSDP()))
				exchangeSDESTestRTP(t, a, b, 100, 101)
				exchangeSDESTestRTP(t, b, a, 100, 102)
				exchangeSDESTestRTCP(t, a, b, 1, false)
				exchangeSDESTestRTCP(t, b, a, 1, false)
			})
		}
	}
	for _, change := range []string{"same_address_same_key", "new_ip_new_key", "new_port_new_key"} {
		t.Run(change, func(t *testing.T) {
			m := newSDESTestSession(t, 10000)
			addr := net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 20000}
			key := bytes.Repeat([]byte{0x11}, 30)
			require.NoError(t, m.RemoteSDP(sdesTestOffer(addr, key, 1)))
			m.LocalSDP()
			oldContext := m.sdes.remote.ctx
			if change != "same_address_same_key" {
				key = bytes.Repeat([]byte{0x22}, 30)
				if change == "new_ip_new_key" {
					addr.IP = net.IPv4(127, 0, 0, 2)
				} else {
					addr.Port += 2
				}
			}
			fork := m.Fork()
			require.NoError(t, fork.RemoteSDP(sdesTestOffer(addr, key, 9)))
			require.True(t, udpAddrEqual(addr, fork.Raddr))
			require.Equal(t, addr.Port+1, fork.rtcpRaddr.Port)
			if change == "same_address_same_key" {
				require.Same(t, oldContext, fork.sdes.remote.ctx)
			} else {
				require.NotSame(t, oldContext, fork.sdes.remote.ctx)
			}
			ctx := sdesTestContext(t, key)
			receiveSDESTestPacket(t, m, false, encryptSDESTestPacket(t, ctx, false, 1), 1, false)
			receiveSDESTestPacket(t, m, true, encryptSDESTestPacket(t, ctx, true, 1), 1, false)
			require.NoError(t, fork.WriteRTP(sdesTestPacket(1, 90)))
			require.Equal(t, addr.String(), (<-sdesTestTransport(fork, false).written).addr.String())
		})
	}
}

func TestSDESInitialPlaintext(t *testing.T) {
	for _, localOffer := range []bool{false, true} {
		t.Run(fmt.Sprintf("offerer=%v", localOffer), func(t *testing.T) {
			m := newSDESTestSession(t, 10000)
			if localOffer {
				require.Contains(t, string(m.LocalSDP()), "a=crypto:")
			}
			earlierFork := m.Fork()
			addr := net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 20000}
			plain := generateSDPForAudio(1, 2, "RTP/AVP", addr.IP, addr.IP, addr.Port, sdp.ModeSendrecv, []Codec{CodecAudioAlaw}, sdesInline{}, nil)
			require.NoError(t, m.RemoteSDP(plain))
			// Earlier offer generation must not change the negotiated plaintext
			// send path, even though this fork still has no remote address.
			earlierFork.LocalSDP()
			for _, session := range []*MediaSession{m, m.Fork()} {
				body := string(session.LocalSDP())
				require.NotContains(t, body, "a=crypto:")
				require.Contains(t, body, "RTP/AVP")
				pkt := sdesTestPacket(1, 37)
				wire, err := pkt.Marshal()
				require.NoError(t, err)
				require.NoError(t, session.WriteRTP(pkt))
				require.Equal(t, wire, (<-sdesTestTransport(session, false).written).data)
				receiveSDESTestPacket(t, session, false, wire, 1, false)
				report := sdesTestReport(1, 37)
				wire, err = report.Marshal()
				require.NoError(t, err)
				require.NoError(t, session.WriteRTCP(report))
				require.Equal(t, wire, (<-sdesTestTransport(session, true).written).data)
				receiveSDESTestPacket(t, session, true, wire, 1, false)
			}
		})
	}
}

func TestSDESConcurrentRefresh(t *testing.T) {
	const iterations = 40
	a, b := negotiateSDESTestPair(t)
	// A second sender advertises a fresh key at the same endpoint.
	other := newSDESTestSession(t, a.Laddr.Port)
	require.NoError(t, other.RemoteSDP(b.LocalSDP()))
	otherOffer := other.LocalSDP()
	aOffer := a.LocalSDP()
	updating := b.Fork()
	require.NoError(t, updating.RemoteSDP(otherOffer))
	b.sdes.now = func() time.Time { return time.Unix(1000, 0) }
	rtpReaders := []*MediaSession{b, b.Fork()}
	rtcpReaders := []*MediaSession{b.Fork(), b.Fork()}
	writers := []*MediaSession{a.Fork(), other.Fork()}
	reverseWriters := []*MediaSession{b.Fork(), b.Fork()}
	var wg sync.WaitGroup
	errors := make(chan error, 1024)
	start := make(chan struct{})
	run := func(f func() error) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			if err := f(); err != nil {
				errors <- err
			}
		}()
	}
	for i := range writers {
		writer, reverse := writers[i], reverseWriters[i]
		ssrc := uint32(100 + i)
		run(func() error {
			for n := 0; n < iterations; n++ {
				if err := writer.WriteRTP(sdesTestPacket(uint16(n), ssrc)); err != nil {
					return err
				}
				wire := <-sdesTestTransport(writer, false).written
				sdesTestTransport(b, false).incoming <- wire.data
				if err := writer.WriteRTCP(sdesTestReport(uint16(n), ssrc)); err != nil {
					return err
				}
				wire = <-sdesTestTransport(writer, true).written
				sdesTestTransport(b, true).incoming <- wire.data
			}
			return nil
		})
		run(func() error {
			for n := 0; n < iterations; n++ {
				if err := reverse.WriteRTP(sdesTestPacket(uint16(n), ssrc)); err != nil {
					return err
				}
				if err := reverse.WriteRTCP(sdesTestReport(uint16(n), ssrc)); err != nil {
					return err
				}
			}
			return nil
		})
		for _, isRTCP := range []bool{false, true} {
			reader := rtpReaders[i]
			if isRTCP {
				reader = rtcpReaders[i]
			}
			run(func() error {
				for n := 0; n < iterations; n++ {
					got := readSDESTestPacket(reader, isRTCP)
					if got.err != nil {
						return got.err
					}
					if !isRTCP && !bytes.Equal(got.payload, []byte("authenticated audio")) {
						return fmt.Errorf("invalid decrypted payload")
					}
				}
				return nil
			})
		}
	}
	run(func() error {
		for n := 0; n < iterations*3; n++ {
			body := aOffer
			if n%2 == 1 {
				body = otherOffer
			}
			if err := updating.RemoteSDP(body); err != nil {
				return err
			}
			updating.LocalSDP()
		}
		return nil
	})
	close(start)
	done := make(chan struct{})
	go func() { wg.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("concurrent packet operations and rekeys did not complete")
	}
	close(errors)
	for err := range errors {
		require.NoError(t, err)
	}
	// The reverse direction uses the same shared local context while its receive
	// key changes. Every packet still authenticates at the original sender.
	for _, isRTCP := range []bool{false, true} {
		for n := 0; n < iterations*2; n++ {
			wire := <-sdesTestTransport(b, isRTCP).written
			sdesTestTransport(a, isRTCP).incoming <- wire.data
			got := readSDESTestPacket(a, isRTCP)
			require.NoError(t, got.err)
			require.Positive(t, got.n)
		}
	}
}

func TestSDESSRTCPReceiveState(t *testing.T) {
	a, b := negotiateSDESTestPair(t)
	// Install a replay-protected receive context to make its retained SRTCP
	// receive state observable on the wire, in addition to context identity.
	key := b.sdes.remote.key
	ctx, err := srtp.CreateContext(key[:16], key[16:], srtp.ProtectionProfileAes128CmHmacSha1_80, srtp.SRTCPReplayProtection(64))
	require.NoError(t, err)
	b.sdes.remote.ctx = ctx
	require.NoError(t, a.WriteRTCP(sdesTestReport(1, 37)))
	first := (<-sdesTestTransport(a, true).written).data
	receiveSDESTestPacket(t, b, true, first, 1, false)
	for i := 0; i < 3; i++ {
		fork := b.Fork()
		require.NoError(t, fork.RemoteSDP(a.LocalSDP()))
		fork.LocalSDP()
		require.Same(t, ctx, fork.sdes.remote.ctx)
		receiveSDESTestPacket(t, fork, true, first, 0, true)
		b = fork
	}
	require.NoError(t, a.WriteRTCP(sdesTestReport(2, 37)))
	receiveSDESTestPacket(t, b, true, (<-sdesTestTransport(a, true).written).data, 2, false)
}

func TestSDESRefreshMediaDirection(t *testing.T) {
	a, b := negotiateSDESTestPair(t)
	fork := a.Fork()
	fork.Mode = sdp.ModeSendonly
	offer := fork.LocalSDP()
	require.Contains(t, string(offer), "a=sendonly\r\n")
	require.Contains(t, string(offer), "a=crypto:")
	answerer := b.Fork()
	require.NoError(t, answerer.RemoteSDP(offer))
	answer := answerer.LocalSDP()
	require.Contains(t, string(answer), "a=recvonly\r\n")
	require.NoError(t, fork.RemoteSDP(answer))
	exchangeSDESTestRTP(t, fork, answerer, 100, 101)
}

func sdesTestRetirementDeadline(_ *MediaSession, key *sdesReceiveKey) time.Time {
	return key.until
}

func TestSDESPendingRekeysPreserveWorkingKey(t *testing.T) {
	for _, isRTCP := range []bool{false, true} {
		t.Run(fmt.Sprintf("rtcp=%v", isRTCP), func(t *testing.T) {
			m := newSDESTestSession(t, 10000)
			addr := net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 20000}
			keyA, keyB, keyC := bytes.Repeat([]byte{0x11}, 30), bytes.Repeat([]byte{0x22}, 30), bytes.Repeat([]byte{0x33}, 30)
			require.NoError(t, m.RemoteSDP(sdesTestOffer(addr, keyA, 1)))
			var elapsed atomic.Int64
			epoch := time.Unix(1000, 0)
			m.sdes.now = func() time.Time { return epoch.Add(time.Duration(elapsed.Load())) }
			ctxA, ctxC := sdesTestContext(t, keyA), sdesTestContext(t, keyC)
			receiveSDESTestPacket(t, m, isRTCP, encryptSDESTestPacket(t, ctxA, isRTCP, 1), 1, false)
			working := m.sdes.working
			fork, sibling := m.Fork(), m.Fork()
			require.NoError(t, fork.RemoteSDP(sdesTestOffer(addr, keyB, 2)))
			require.NoError(t, fork.RemoteSDP(sdesTestOffer(addr, keyC, 3)))
			require.Same(t, working, m.sdes.working)
			elapsed.Store(int64(20 * time.Second))
			bad := encryptSDESTestPacket(t, ctxC, isRTCP, 1)
			bad[len(bad)-1] ^= 0xff
			receiveSDESTestPacket(t, m, isRTCP, bad, 0, true)
			receiveSDESTestPacket(t, sibling, isRTCP, encryptSDESTestPacket(t, ctxA, isRTCP, 2), 2, false)
			require.True(t, sdesTestRetirementDeadline(m, working).IsZero())
			require.Same(t, working, m.sdes.working)
			receiveSDESTestPacket(t, m, isRTCP, encryptSDESTestPacket(t, ctxC, isRTCP, 2), 2, false)
			require.Equal(t, epoch.Add(25*time.Second), sdesTestRetirementDeadline(m, working))
			elapsed.Store(int64(24 * time.Second))
			receiveSDESTestPacket(t, sibling, isRTCP, encryptSDESTestPacket(t, ctxA, isRTCP, 3), 3, false)
			elapsed.Store(int64(25 * time.Second))
			receiveSDESTestPacket(t, sibling, isRTCP, encryptSDESTestPacket(t, ctxA, isRTCP, 4), 0, true)
			receiveSDESTestPacket(t, m, isRTCP, encryptSDESTestPacket(t, ctxC, isRTCP, 3), 3, false)
		})
	}
}

func TestSDESOverlappingRekeyDeadlines(t *testing.T) {
	for _, isRTCP := range []bool{false, true} {
		t.Run(fmt.Sprintf("rtcp=%v", isRTCP), func(t *testing.T) {
			m := newSDESTestSession(t, 10000)
			addr := net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 20000}
			keyA, keyB, keyC := bytes.Repeat([]byte{0x11}, 30), bytes.Repeat([]byte{0x22}, 30), bytes.Repeat([]byte{0x33}, 30)
			require.NoError(t, m.RemoteSDP(sdesTestOffer(addr, keyA, 1)))
			var elapsed atomic.Int64
			epoch := time.Unix(1000, 0)
			m.sdes.now = func() time.Time { return epoch.Add(time.Duration(elapsed.Load())) }
			ctxA, ctxB, ctxC := sdesTestContext(t, keyA), sdesTestContext(t, keyB), sdesTestContext(t, keyC)
			receiveSDESTestPacket(t, m, isRTCP, encryptSDESTestPacket(t, ctxA, isRTCP, 1), 1, false)
			workingA := m.sdes.working
			fork := m.Fork()
			require.NoError(t, fork.RemoteSDP(sdesTestOffer(addr, keyB, 2)))
			elapsed.Store(int64(time.Second))
			receiveSDESTestPacket(t, m, isRTCP, encryptSDESTestPacket(t, ctxB, isRTCP, 1), 1, false)
			workingB := m.sdes.working
			require.Equal(t, epoch.Add(6*time.Second), sdesTestRetirementDeadline(m, workingA))
			elapsed.Store(int64(2 * time.Second))
			require.NoError(t, fork.RemoteSDP(sdesTestOffer(addr, keyC, 3)))
			elapsed.Store(int64(5 * time.Second))
			bad := encryptSDESTestPacket(t, ctxC, isRTCP, 1)
			bad[len(bad)-1] ^= 0xff
			receiveSDESTestPacket(t, m, isRTCP, bad, 0, true)
			receiveSDESTestPacket(t, m, isRTCP, encryptSDESTestPacket(t, ctxA, isRTCP, 2), 2, false)
			receiveSDESTestPacket(t, m, isRTCP, encryptSDESTestPacket(t, ctxB, isRTCP, 2), 2, false)
			require.Equal(t, epoch.Add(6*time.Second), sdesTestRetirementDeadline(m, workingA))
			receiveSDESTestPacket(t, m, isRTCP, encryptSDESTestPacket(t, ctxC, isRTCP, 2), 2, false)
			require.Equal(t, epoch.Add(6*time.Second), sdesTestRetirementDeadline(m, workingA))
			require.Equal(t, epoch.Add(10*time.Second), sdesTestRetirementDeadline(m, workingB))
			elapsed.Store(int64(6 * time.Second))
			receiveSDESTestPacket(t, m, isRTCP, encryptSDESTestPacket(t, ctxA, isRTCP, 3), 0, true)
			receiveSDESTestPacket(t, m, isRTCP, encryptSDESTestPacket(t, ctxB, isRTCP, 3), 3, false)
			elapsed.Store(int64(10 * time.Second))
			receiveSDESTestPacket(t, m, isRTCP, encryptSDESTestPacket(t, ctxB, isRTCP, 4), 0, true)
			receiveSDESTestPacket(t, m, isRTCP, encryptSDESTestPacket(t, ctxC, isRTCP, 3), 3, false)
		})
	}
}

func TestSDESRetainedKeyReselectionPreservesContext(t *testing.T) {
	for _, authenticateReplacement := range []bool{false, true} {
		t.Run(fmt.Sprintf("replacement_authenticated=%v", authenticateReplacement), func(t *testing.T) {
			m := newSDESTestSession(t, 10000)
			addr := net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 20000}
			keyA, keyB := bytes.Repeat([]byte{0x11}, 30), bytes.Repeat([]byte{0x22}, 30)
			require.NoError(t, m.RemoteSDP(sdesTestOffer(addr, keyA, 1)))
			m.sdes.now = func() time.Time { return time.Unix(1000, 0) }
			ctxA, ctxB := sdesTestContext(t, keyA), sdesTestContext(t, keyB)
			receiveCtx, err := srtp.CreateContext(keyA[:16], keyA[16:], srtp.ProtectionProfileAes128CmHmacSha1_80, srtp.SRTPReplayProtection(64), srtp.SRTCPReplayProtection(64))
			require.NoError(t, err)
			m.sdes.remote.ctx = receiveCtx
			for _, seq := range []uint16{65534, 65535, 0} {
				receiveSDESTestPacket(t, m, false, encryptSDESTestPacket(t, ctxA, false, seq), seq, false)
			}
			oldReport := encryptSDESTestPacket(t, ctxA, true, 1)
			receiveSDESTestPacket(t, m, true, oldReport, 1, false)
			fork, sibling := m.Fork(), m.Fork()
			require.NoError(t, fork.RemoteSDP(sdesTestOffer(addr, keyB, 2)))
			if authenticateReplacement {
				receiveSDESTestPacket(t, m, false, encryptSDESTestPacket(t, ctxB, false, 100), 100, false)
				receiveSDESTestPacket(t, m, true, encryptSDESTestPacket(t, ctxB, true, 1), 1, false)
			}
			working := m.sdes.working
			require.NoError(t, sibling.RemoteSDP(sdesTestOffer(addr, keyA, 3)))
			require.Same(t, receiveCtx, m.sdes.remote.ctx)
			// A replay must not activate the reselected key or reset replay state.
			receiveSDESTestPacket(t, m, true, oldReport, 0, true)
			require.Same(t, working, m.sdes.working)
			bad := encryptSDESTestPacket(t, ctxA, false, 1)
			bad[len(bad)-1] ^= 0xff
			receiveSDESTestPacket(t, m, false, bad, 0, true)
			require.Same(t, working, m.sdes.working)
			receiveSDESTestPacket(t, fork, false, encryptSDESTestPacket(t, ctxA, false, 2), 2, false)
			roc, exists := receiveCtx.ROC(37)
			require.True(t, exists)
			require.Equal(t, uint32(1), roc)
			receiveSDESTestPacket(t, fork, true, oldReport, 0, true)
			receiveSDESTestPacket(t, fork, true, encryptSDESTestPacket(t, ctxA, true, 2), 2, false)
		})
	}
}

func TestSDESRetainedKeyAddressChangeRejected(t *testing.T) {
	for _, changePort := range []bool{false, true} {
		t.Run(fmt.Sprintf("change_port=%v", changePort), func(t *testing.T) {
			m := newSDESTestSession(t, 10000)
			addr := net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 20000}
			keyA, keyB := bytes.Repeat([]byte{0x11}, 30), bytes.Repeat([]byte{0x22}, 30)
			require.NoError(t, m.RemoteSDP(sdesTestOffer(addr, keyA, 1)))
			m.sdes.now = func() time.Time { return time.Unix(1000, 0) }
			ctxA, ctxB := sdesTestContext(t, keyA), sdesTestContext(t, keyB)
			receiveSDESTestPacket(t, m, false, encryptSDESTestPacket(t, ctxA, false, 1), 1, false)
			fork := m.Fork()
			require.NoError(t, fork.RemoteSDP(sdesTestOffer(addr, keyB, 2)))
			receiveSDESTestPacket(t, m, false, encryptSDESTestPacket(t, ctxB, false, 1), 1, false)
			selected, working := m.sdes.remote, m.sdes.working
			changed := addr
			if changePort {
				changed.Port += 2
			} else {
				changed.IP = net.IPv4(127, 0, 0, 2)
			}
			require.Error(t, fork.RemoteSDP(sdesTestOffer(changed, keyA, 3)))
			require.True(t, udpAddrEqual(addr, fork.Raddr))
			require.Same(t, selected, m.sdes.remote)
			require.Same(t, working, m.sdes.working)
			receiveSDESTestPacket(t, m, false, encryptSDESTestPacket(t, ctxA, false, 2), 2, false)
			receiveSDESTestPacket(t, m, false, encryptSDESTestPacket(t, ctxB, false, 2), 2, false)
		})
	}
}

func TestSDESSupersededReselectionKeepsDeadline(t *testing.T) {
	for _, isRTCP := range []bool{false, true} {
		t.Run(fmt.Sprintf("rtcp=%v", isRTCP), func(t *testing.T) {
			m := newSDESTestSession(t, 10000)
			addr := net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 20000}
			keyA, keyB, keyC := bytes.Repeat([]byte{0x11}, 30), bytes.Repeat([]byte{0x22}, 30), bytes.Repeat([]byte{0x33}, 30)
			require.NoError(t, m.RemoteSDP(sdesTestOffer(addr, keyA, 1)))
			var elapsed atomic.Int64
			epoch := time.Unix(1000, 0)
			m.sdes.now = func() time.Time { return epoch.Add(time.Duration(elapsed.Load())) }
			ctxA, ctxB := sdesTestContext(t, keyA), sdesTestContext(t, keyB)
			receiveSDESTestPacket(t, m, isRTCP, encryptSDESTestPacket(t, ctxA, isRTCP, 1), 1, false)
			workingA := m.sdes.working
			fork := m.Fork()
			require.NoError(t, fork.RemoteSDP(sdesTestOffer(addr, keyB, 2)))
			receiveSDESTestPacket(t, m, isRTCP, encryptSDESTestPacket(t, ctxB, isRTCP, 1), 1, false)
			deadline := epoch.Add(5 * time.Second)
			elapsed.Store(int64(time.Second))
			require.NoError(t, fork.RemoteSDP(sdesTestOffer(addr, keyA, 3)))
			require.Same(t, workingA, m.sdes.remote)
			require.Equal(t, deadline, sdesTestRetirementDeadline(m, workingA))
			require.NoError(t, fork.RemoteSDP(sdesTestOffer(addr, keyC, 4)))
			elapsed.Store(int64(4 * time.Second))
			receiveSDESTestPacket(t, m, isRTCP, encryptSDESTestPacket(t, ctxA, isRTCP, 2), 2, false)
			require.Equal(t, deadline, sdesTestRetirementDeadline(m, workingA))
			elapsed.Store(int64(5 * time.Second))
			receiveSDESTestPacket(t, m, isRTCP, encryptSDESTestPacket(t, ctxA, isRTCP, 3), 0, true)
			receiveSDESTestPacket(t, m, isRTCP, encryptSDESTestPacket(t, ctxB, isRTCP, 2), 2, false)
		})
	}
}

func TestSDESSupersededPendingKeyOverlap(t *testing.T) {
	for _, isRTCP := range []bool{false, true} {
		t.Run(fmt.Sprintf("rtcp=%v", isRTCP), func(t *testing.T) {
			m := newSDESTestSession(t, 10000)
			addr := net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 20000}
			keyA, keyB, keyC := bytes.Repeat([]byte{0x11}, 30), bytes.Repeat([]byte{0x22}, 30), bytes.Repeat([]byte{0x33}, 30)
			require.NoError(t, m.RemoteSDP(sdesTestOffer(addr, keyA, 1)))
			var elapsed atomic.Int64
			epoch := time.Unix(1000, 0)
			m.sdes.now = func() time.Time { return epoch.Add(time.Duration(elapsed.Load())) }
			ctxA, ctxB, ctxC := sdesTestContext(t, keyA), sdesTestContext(t, keyB), sdesTestContext(t, keyC)
			receiveSDESTestPacket(t, m, isRTCP, encryptSDESTestPacket(t, ctxA, isRTCP, 1), 1, false)
			fork := m.Fork()
			require.NoError(t, fork.RemoteSDP(sdesTestOffer(addr, keyB, 2)))
			pendingB := m.sdes.remote
			require.NoError(t, fork.RemoteSDP(sdesTestOffer(addr, keyC, 3)))
			elapsed.Store(int64(20 * time.Second))
			receiveSDESTestPacket(t, m, isRTCP, encryptSDESTestPacket(t, ctxB, isRTCP, 1), 1, false)
			require.True(t, sdesTestRetirementDeadline(m, pendingB).IsZero())
			bad := encryptSDESTestPacket(t, ctxC, isRTCP, 1)
			bad[len(bad)-1] ^= 0xff
			receiveSDESTestPacket(t, m, isRTCP, bad, 0, true)
			require.True(t, sdesTestRetirementDeadline(m, pendingB).IsZero())
			receiveSDESTestPacket(t, m, isRTCP, encryptSDESTestPacket(t, ctxC, isRTCP, 2), 2, false)
			require.Equal(t, epoch.Add(25*time.Second), sdesTestRetirementDeadline(m, pendingB))
			elapsed.Store(int64(24 * time.Second))
			receiveSDESTestPacket(t, m, isRTCP, encryptSDESTestPacket(t, ctxB, isRTCP, 2), 2, false)
			elapsed.Store(int64(25 * time.Second))
			receiveSDESTestPacket(t, m, isRTCP, encryptSDESTestPacket(t, ctxB, isRTCP, 3), 0, true)
			receiveSDESTestPacket(t, m, isRTCP, encryptSDESTestPacket(t, ctxC, isRTCP, 3), 3, false)
		})
	}
}

func TestSDESReselectedKeyCannotReviveAfterExpiry(t *testing.T) {
	for _, isRTCP := range []bool{false, true} {
		for _, readAtExpiry := range []bool{false, true} {
			t.Run(fmt.Sprintf("rtcp=%v/read_at_expiry=%v", isRTCP, readAtExpiry), func(t *testing.T) {
				m := newSDESTestSession(t, 10000)
				addr := net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 20000}
				keyA, keyB, keyC := bytes.Repeat([]byte{0x11}, 30), bytes.Repeat([]byte{0x22}, 30), bytes.Repeat([]byte{0x33}, 30)
				require.NoError(t, m.RemoteSDP(sdesTestOffer(addr, keyA, 1)))
				var elapsed atomic.Int64
				epoch := time.Unix(1000, 0)
				m.sdes.now = func() time.Time { return epoch.Add(time.Duration(elapsed.Load())) }
				ctxA, ctxB, ctxC := sdesTestContext(t, keyA), sdesTestContext(t, keyB), sdesTestContext(t, keyC)
				receiveSDESTestPacket(t, m, isRTCP, encryptSDESTestPacket(t, ctxA, isRTCP, 1), 1, false)
				originalA := m.sdes.working
				fork := m.Fork()
				require.NoError(t, fork.RemoteSDP(sdesTestOffer(addr, keyB, 2)))
				receiveSDESTestPacket(t, m, isRTCP, encryptSDESTestPacket(t, ctxB, isRTCP, 1), 1, false)
				workingB := m.sdes.working
				deadline := epoch.Add(5 * time.Second)
				elapsed.Store(int64(time.Second))
				require.NoError(t, fork.RemoteSDP(sdesTestOffer(addr, keyA, 3)))
				require.Same(t, originalA, m.sdes.remote)
				bad := encryptSDESTestPacket(t, ctxA, isRTCP, 2)
				bad[len(bad)-1] ^= 0xff
				receiveSDESTestPacket(t, m, isRTCP, bad, 0, true)
				require.Equal(t, deadline, originalA.until)
				if readAtExpiry {
					elapsed.Store(int64(5 * time.Second))
					// Expiry applies even though SDP still selects the retired key.
					receiveSDESTestPacket(t, m, isRTCP, encryptSDESTestPacket(t, ctxA, isRTCP, 3), 0, true)
					require.Same(t, originalA, m.sdes.remote)
					require.Same(t, workingB, m.sdes.working)
					receiveSDESTestPacket(t, m, isRTCP, encryptSDESTestPacket(t, ctxB, isRTCP, 2), 2, false)
				}
				elapsed.Store(int64(6 * time.Second))
				// Also cover SDP supersession before any packet prunes expired A.
				require.NoError(t, fork.RemoteSDP(sdesTestOffer(addr, keyC, 4)))
				require.Equal(t, deadline, originalA.until)
				receiveSDESTestPacket(t, m, isRTCP, encryptSDESTestPacket(t, ctxA, isRTCP, 4), 0, true)
				require.Same(t, workingB, m.sdes.working)
				receiveSDESTestPacket(t, m, isRTCP, encryptSDESTestPacket(t, ctxB, isRTCP, 3), 3, false)
				selectedC := m.sdes.remote
				// Neither a same-endpoint SDP replay nor another old-key packet can
				// recreate the expired context or reset its acceptance deadline.
				require.ErrorContains(t, fork.RemoteSDP(sdesTestOffer(addr, keyA, 5)), "expired receive key")
				require.Same(t, selectedC, m.sdes.remote)
				receiveSDESTestPacket(t, m, isRTCP, encryptSDESTestPacket(t, ctxC, isRTCP, 1), 1, false)
				elapsed.Store(int64(20 * time.Second))
				receiveSDESTestPacket(t, m, isRTCP, encryptSDESTestPacket(t, ctxA, isRTCP, 5), 0, true)
				receiveSDESTestPacket(t, m, isRTCP, encryptSDESTestPacket(t, ctxC, isRTCP, 2), 2, false)
			})
		}
	}
}

func TestSDESExpiredKeyIdentityRejected(t *testing.T) {
	for _, isRTCP := range []bool{false, true} {
		for _, change := range []string{"ip", "port", "same_endpoint"} {
			t.Run(fmt.Sprintf("rtcp=%v/%s", isRTCP, change), func(t *testing.T) {
				peer, m := negotiateSDESTestPair(t)
				addr := peer.Laddr
				keyA, keyB := bytes.Clone(m.sdes.remote.key), bytes.Repeat([]byte{0x22}, 30)
				var elapsed atomic.Int64
				epoch := time.Unix(1000, 0)
				m.sdes.now = func() time.Time { return epoch.Add(time.Duration(elapsed.Load())) }
				ctxA, ctxB := sdesTestContext(t, keyA), sdesTestContext(t, keyB)
				receiveSDESTestPacket(t, m, isRTCP, encryptSDESTestPacket(t, ctxA, isRTCP, 1), 1, false)
				fork := m.Fork()
				require.NoError(t, fork.RemoteSDP(sdesTestOffer(addr, keyB, 2)))
				receiveSDESTestPacket(t, m, isRTCP, encryptSDESTestPacket(t, ctxB, isRTCP, 1), 1, false)
				elapsed.Store(int64(6 * time.Second))
				// A valid current-key packet prunes A before the attempted SDP reuse.
				receiveSDESTestPacket(t, m, isRTCP, encryptSDESTestPacket(t, ctxB, isRTCP, 2), 2, false)
				require.Nil(t, m.sdes.retainedKey(keyA, srtp.ProtectionProfileAes128CmHmacSha1_80))
				changed := addr
				switch change {
				case "ip":
					changed.IP = net.IPv4(127, 0, 0, 2)
				case "port":
					changed.Port += 2
				}
				localCrypto := sdesTestCrypto(t, fork.LocalSDP())
				selected, working, sendCtx := m.sdes.remote, m.sdes.working, m.sdes.local
				beforeAddr, beforeRTCPAddr := fork.Raddr, fork.rtcpRaddr
				beforeTag, beforeProto, beforeVersion := fork.srtpRemoteTag, fork.remoteProto, fork.sessionVersion
				err := fork.RemoteSDP(sdesTestOffer(changed, keyA, 9))
				if change == "same_endpoint" {
					require.ErrorContains(t, err, "expired receive key")
				} else {
					require.ErrorContains(t, err, "remote address change requires a new receive key")
				}
				require.Equal(t, beforeAddr, fork.Raddr)
				require.Equal(t, beforeRTCPAddr, fork.rtcpRaddr)
				require.Equal(t, beforeTag, fork.srtpRemoteTag)
				require.Equal(t, beforeProto, fork.remoteProto)
				require.Equal(t, beforeVersion, fork.sessionVersion)
				require.Same(t, selected, m.sdes.remote)
				require.Same(t, working, m.sdes.working)
				require.Same(t, sendCtx, m.sdes.local)
				require.True(t, m.sdes.sendEnabled)
				require.Equal(t, localCrypto, sdesTestCrypto(t, fork.LocalSDP()))
				for _, receiveRTCP := range []bool{false, true} {
					receiveSDESTestPacket(t, m, receiveRTCP, encryptSDESTestPacket(t, ctxA, receiveRTCP, 3), 0, true)
					receiveSDESTestPacket(t, m, receiveRTCP, encryptSDESTestPacket(t, ctxB, receiveRTCP, 3), 3, false)
				}
				exchangeSDESTestRTP(t, fork, peer, 100, 90)
				exchangeSDESTestRTCP(t, fork, peer, 1, false)
			})
		}
	}
}
