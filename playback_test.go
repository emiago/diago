// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"bytes"
	"io"
	"net"
	"os"
	"testing"

	"github.com/emiago/diago/media"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestIntegrationStreamWAV(t *testing.T) {
	fh, err := os.Open("testdata/files/demo-echodone.wav")
	require.NoError(t, err)
	sess, err := media.NewMediaSession(net.IPv4(127, 0, 0, 1), 0)
	require.NoError(t, err)
	defer sess.Close()

	codec := media.CodecFromSession(sess)
	rtpWriter := media.NewRTPPacketWriter(sess, codec)
	sess.Raddr = net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9999}

	udpDump, err := net.ListenUDP("udp4", &sess.Raddr)
	require.NoError(t, err)
	defer udpDump.Close()

	go func() {
		io.ReadAll(udpDump)
	}()

	p := NewAudioPlayback(rtpWriter, codec)

	written, err := p.Play(fh, "audio/wav")
	// written, err := streamWavRTP(fh, rtpWriter, codec)
	require.NoError(t, err)
	require.Greater(t, written, int64(10000))
}

func TestIntegrationPlaybackStreamWAV(t *testing.T) {
	fh, err := os.Open("testdata/files/demo-echodone.wav")
	require.NoError(t, err)
	sess, err := media.NewMediaSession(net.IPv4(127, 0, 0, 1), 0)
	require.NoError(t, err)
	defer sess.Close()

	codec := media.CodecFromSession(sess)
	sess.Raddr = net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9999}

	p := NewAudioPlayback(bytes.NewBuffer(make([]byte, 0)), codec)

	udpDump, err := net.ListenUDP("udp4", &sess.Raddr)
	require.NoError(t, err)
	defer udpDump.Close()

	go func() {
		io.ReadAll(udpDump)
	}()

	written, err := p.Play(fh, "audio/wav")
	require.NoError(t, err)
	require.Greater(t, written, int64(10000))
}

func TestIntegrationPlaybackFile(t *testing.T) {
	r, w := io.Pipe()
	go func() {
		io.ReadAll(r)
	}()

	dialog := &DialogServerSession{
		DialogMedia: DialogMedia{
			mediaSession: &media.MediaSession{Codecs: []media.Codec{media.CodecAudioUlaw}},
			// audioReader:  bytes.NewBuffer(make([]byte, 9999)),
			audioWriter:     w,
			RTPPacketWriter: media.NewRTPPacketWriter(nil, media.CodecAudioUlaw),
		},
	}

	t.Run("withControl", func(t *testing.T) {
		playback, err := dialog.PlaybackControlCreate()
		require.NoError(t, err)

		playback.Stop()
		written, err := playback.PlayFile("testdata/files/demo-echodone.wav")
		require.NoError(t, err)
		assert.EqualValues(t, 0, written)
	})

	t.Run("default", func(t *testing.T) {
		playback, err := dialog.PlaybackCreate()
		require.NoError(t, err)

		written, err := playback.PlayFile("testdata/files/demo-echodone.wav")
		require.NoError(t, err)
		require.Greater(t, written, int64(10000))
		t.Log("Written on RTP stream", written)
	})
}
