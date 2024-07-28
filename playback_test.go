package diago

import (
	"io"
	"net"
	"os"
	"testing"

<<<<<<< HEAD
	"github.com/emiago/sipgox"
=======
	"github.com/emiago/diago/audio"
	"github.com/emiago/media"
>>>>>>> 7f3053f (fix: moved to media package)
	"github.com/stretchr/testify/require"
)

func TestIntegrationStreamWAV(t *testing.T) {
	fh, err := os.Open("testdata/demo-thanks.wav")
	require.NoError(t, err)
	sess, err := media.NewMediaSession(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	require.NoError(t, err)
	defer sess.Close()

	rtpWriter := media.NewRTPPacketWriterMedia(sess)
	sess.Raddr = &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9999}

	udpDump, err := net.ListenUDP("udp4", sess.Raddr)
	require.NoError(t, err)
	defer udpDump.Close()

	go func() {
		io.ReadAll(udpDump)
	}()

	written, err := streamWavRTP(fh, rtpWriter)
	require.NoError(t, err)
	require.Greater(t, written, 10000)
}

func TestIntegrationPlaybackStreamWAV(t *testing.T) {
	fh, err := os.Open("testdata/demo-thanks.wav")
	require.NoError(t, err)
	sess, err := media.NewMediaSession(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	require.NoError(t, err)
	defer sess.Close()

	rtpWriter := media.NewRTPPacketWriterMedia(sess)
	sess.Raddr = &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9999}

	enc, err := audio.NewPCMEncoder(rtpWriter.PayloadType, rtpWriter)
	require.NoError(t, err)

	p := Playback{
		writer:     enc,
		SampleRate: rtpWriter.SampleRate,
		SampleDur:  20 * time.Millisecond,
	}

	udpDump, err := net.ListenUDP("udp4", sess.Raddr)
	require.NoError(t, err)
	defer udpDump.Close()

	go func() {
		io.ReadAll(udpDump)
	}()

	written, err := p.streamWav(fh, rtpWriter)
	require.NoError(t, err)
	require.Greater(t, written, 10000)
}
