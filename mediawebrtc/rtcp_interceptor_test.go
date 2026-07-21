// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2026, Emir Aganovic

package mediawebrtc

import (
	"testing"

	"github.com/pion/interceptor"
	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/webrtc/v3"
	"github.com/stretchr/testify/require"
)

type rtcpInterceptorTestRTPReader struct {
	packet []byte
}

func (r *rtcpInterceptorTestRTPReader) Read(buf []byte, attributes interceptor.Attributes) (int, interceptor.Attributes, error) {
	return copy(buf, r.packet), attributes, nil
}

type rtcpInterceptorTestRTPWriter struct{}

func (rtcpInterceptorTestRTPWriter) Write(_ *rtp.Header, payload []byte, _ interceptor.Attributes) (int, error) {
	return len(payload), nil
}

type rtcpInterceptorTestRTCPReader struct {
	packet []byte
}

func (r *rtcpInterceptorTestRTCPReader) Read(buf []byte, attributes interceptor.Attributes) (int, interceptor.Attributes, error) {
	return copy(buf, r.packet), attributes, nil
}

type rtcpInterceptorTestRTCPWriter struct {
	packets []rtcp.Packet
}

func (w *rtcpInterceptorTestRTCPWriter) Write(pkts []rtcp.Packet, _ interceptor.Attributes) (int, error) {
	w.packets = append(w.packets, pkts...)
	return len(pkts), nil
}

func TestRTCPInterceptorCallbacks(t *testing.T) {
	sess := &MediaSession{}
	var readPacket rtcp.Packet
	var readStats RTPReadStats
	sess.OnReadRTCP(func(pkt rtcp.Packet, stats RTPReadStats) {
		readPacket = pkt
		readStats = stats
	})
	var writePacket rtcp.Packet
	var writeStats RTPWriteStats
	sess.OnWriteRTCP(func(pkt rtcp.Packet, stats RTPWriteStats) {
		writePacket = pkt
		writeStats = stats
	})

	i := &rtcpInterceptor{}
	i.hooks.Store(sess.rtcpHooks)

	remoteRTP := &rtp.Packet{
		Header:  rtp.Header{Version: 2, SSRC: 123, SequenceNumber: 10, PayloadType: 0},
		Payload: []byte{1, 2, 3},
	}
	remoteRTPRaw, err := remoteRTP.Marshal()
	require.NoError(t, err)
	remoteReader := i.BindRemoteStream(
		&interceptor.StreamInfo{ClockRate: 8000},
		&rtcpInterceptorTestRTPReader{packet: remoteRTPRaw},
	)
	_, _, err = remoteReader.Read(make([]byte, 1500), nil)
	require.NoError(t, err)

	receiverReport := &rtcp.ReceiverReport{SSRC: 321}
	remoteRTCPRaw, err := rtcp.Marshal([]rtcp.Packet{receiverReport})
	require.NoError(t, err)
	remoteRTCPReader := i.BindRTCPReader(&rtcpInterceptorTestRTCPReader{packet: remoteRTCPRaw})
	_, _, err = remoteRTCPReader.Read(make([]byte, 1500), nil)
	require.NoError(t, err)
	require.IsType(t, receiverReport, readPacket)
	require.Equal(t, uint32(123), readStats.SSRC)
	require.Equal(t, uint32(8000), readStats.SampleRate)
	require.Equal(t, uint64(1), readStats.PacketsCount)
	require.Equal(t, uint64(3), readStats.OctetCount)

	localWriter := i.BindLocalStream(
		&interceptor.StreamInfo{ClockRate: 8000},
		rtcpInterceptorTestRTPWriter{},
	)
	localHeader := &rtp.Header{Version: 2, SSRC: 456, SequenceNumber: 20, PayloadType: 0}
	_, err = localWriter.Write(localHeader, []byte{4, 5, 6, 7}, nil)
	require.NoError(t, err)

	senderReport := &rtcp.SenderReport{SSRC: 456}
	underlyingWriter := &rtcpInterceptorTestRTCPWriter{}
	localRTCPWriter := i.BindRTCPWriter(underlyingWriter)
	_, err = localRTCPWriter.Write([]rtcp.Packet{senderReport}, nil)
	require.NoError(t, err)
	require.IsType(t, senderReport, writePacket)
	require.Equal(t, uint32(456), writeStats.SSRC)
	require.Equal(t, uint64(1), writeStats.PacketsCount)
	require.Equal(t, uint64(4), writeStats.OctetCount)
	require.Equal(t, []rtcp.Packet{senderReport}, underlyingWriter.packets)
}

func TestMediaSessionRTCPInterceptorAttached(t *testing.T) {
	sess := &MediaSession{}
	require.NoError(t, sess.Init(webrtc.Configuration{
		ICEServers: []webrtc.ICEServer{{URLs: []string{"stun:127.0.0.1:9"}}},
	}))
	t.Cleanup(func() { require.NoError(t, sess.Close()) })

	written := make(chan rtcp.Packet, 1)
	sess.OnWriteRTCP(func(pkt rtcp.Packet, _ RTPWriteStats) {
		written <- pkt
	})
	report := &rtcp.ReceiverReport{SSRC: 789}
	_ = sess.PeerConnection().WriteRTCP([]rtcp.Packet{report})

	select {
	case pkt := <-written:
		require.IsType(t, report, pkt)
	default:
		t.Fatal("WebRTC RTCP interceptor did not invoke the write callback")
	}
}
