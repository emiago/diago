// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"io"
	"net"
	"os"
	"testing"

	"github.com/emiago/diago/audio"
	"github.com/emiago/diago/media"
	"github.com/emiago/diago/media/sdp"
	"github.com/stretchr/testify/assert"
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

	written, err := p.Play(fh, "audio/wav")
	require.NoError(t, err)
	require.Greater(t, written, int64(10000))
}

func TestIntegrationPlaybackFile(t *testing.T) {
	// udpDump, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	// require.NoError(t, err)
	// defer udpDump.Close()

	// go func() {
	// 	io.ReadAll(udpDump)
	// }()

	r, w := io.Pipe()
	go func() {
		io.ReadAll(r)
	}()

	dialog := &DialogServerSession{
		DialogMedia: DialogMedia{
			mediaSession: &media.MediaSession{Formats: sdp.NewFormats(sdp.FORMAT_TYPE_ULAW)},
			// audioReader:  bytes.NewBuffer(make([]byte, 9999)),
			audioWriter: w,
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
