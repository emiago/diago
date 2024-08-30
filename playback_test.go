// SPDX-License-Identifier: MPL-2.0
// Copyright (C) 2024 Emir Aganovic

package diago

import (
	"io"
	"net"
	"os"
	"testing"

	"github.com/emiago/diago/audio"
	"github.com/emiago/diago/media"
	"github.com/stretchr/testify/require"
)

func TestIntegrationStreamWAV(t *testing.T) {
	fh, err := os.Open("testdata/files/demo-echodone.wav")
	require.NoError(t, err)
	sess, err := media.NewMediaSession(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	require.NoError(t, err)
	defer sess.Close()

	codec := media.CodecFromSession(sess)
	rtpWriter := media.NewRTPPacketWriter(sess, codec)
	sess.Raddr = &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9999}

	udpDump, err := net.ListenUDP("udp4", sess.Raddr)
	require.NoError(t, err)
	defer udpDump.Close()

	go func() {
		io.ReadAll(udpDump)
	}()

	written, err := streamWavRTP(fh, rtpWriter, codec)
	require.NoError(t, err)
	require.Greater(t, written, int64(10000))
}

func TestIntegrationPlaybackStreamWAV(t *testing.T) {
	fh, err := os.Open("testdata/files/demo-echodone.wav")
	require.NoError(t, err)
	sess, err := media.NewMediaSession(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	require.NoError(t, err)
	defer sess.Close()

	codec := media.CodecFromSession(sess)
	rtpWriter := media.NewRTPPacketWriter(sess, codec)
	sess.Raddr = &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9999}

	enc, err := audio.NewPCMEncoder(codec.PayloadType, rtpWriter)
	require.NoError(t, err)

	p := NewAudioPlayback(enc, codec)

	udpDump, err := net.ListenUDP("udp4", sess.Raddr)
	require.NoError(t, err)
	defer udpDump.Close()

	go func() {
		io.ReadAll(udpDump)
	}()

	written, err := p.streamWav(fh, rtpWriter)
	require.NoError(t, err)
	require.Greater(t, written, int64(10000))
}
