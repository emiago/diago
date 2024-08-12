// SPDX-License-Identifier: BSD-2-Clause
// Copyright (C) 2024 Emir Aganovic

package diago

import (
	"context"
	"io"
	"net"
	"testing"

	"github.com/emiago/media"
	"github.com/stretchr/testify/require"
)

func TestIntegrationDialogMediaPlaybackFile(t *testing.T) {
	sess, err := media.NewMediaSession(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	require.NoError(t, err)
	defer sess.Close()

	// TODO have RTPSession
	rtpWriter := media.NewRTPPacketWriterMedia(sess)
	sess.Raddr = &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9999}

	dialog := DialogMedia{
		// MediaSession: sess,
		RTPPacketWriter: rtpWriter,
	}

	udpDump, err := net.ListenUDP("udp4", sess.Raddr)
	require.NoError(t, err)
	defer udpDump.Close()

	go func() {
		io.ReadAll(udpDump)
	}()

	t.Run("withControl", func(t *testing.T) {
		playback, err := dialog.PlaybackControlCreate()
		require.NoError(t, err)

		errCh := make(chan error)
		go func() { errCh <- playback.PlayFile(context.TODO(), "testdata/demo-thanks.wav") }()
		playback.Pause()
		require.ErrorIs(t, <-errCh, io.EOF)
	})

	t.Run("default", func(t *testing.T) {
		playback, err := dialog.PlaybackCreate()
		require.NoError(t, err)

		err = playback.PlayFile(context.TODO(), "testdata/demo-thanks.wav")
		require.NoError(t, err)
		require.Greater(t, playback.totalWritten, 10000)
		t.Log("Written on RTP stream", playback.totalWritten)
	})
}
