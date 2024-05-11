package diago

import (
	"context"
	"io"
	"net"
	"os"
	"testing"
	"time"

	"github.com/emiago/diago/audio"
	"github.com/emiago/sipgox"
	"github.com/stretchr/testify/require"
)

/* func TestIntegrationDialogMedia(t *testing.T) {
	fh, err := os.Open("testdata/demo-thanks.wav")
	require.NoError(t, err)
	sess, err := sipgox.NewMediaSession(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	require.NoError(t, err)
	defer sess.Close()

	rtpWriter := sipgox.NewRTPWriter(sess)
	sess.Raddr = &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9999}

	udpDump, err := net.ListenUDP("udp4", sess.Raddr)
	require.NoError(t, err)
	defer udpDump.Close()

	go func() {
		io.ReadAll(udpDump)
	}()

	_, err = copyWavRTP(fh, rtpWriter)
	require.NoError(t, err)
} */

func TestIntegrationStreamWAV(t *testing.T) {
	fh, err := os.Open("testdata/demo-thanks.wav")
	require.NoError(t, err)
	sess, err := sipgox.NewMediaSession(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	require.NoError(t, err)
	defer sess.Close()

	rtpWriter := sipgox.NewRTPWriter(sess)
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

func TestIntegrationStreamURLWAV(t *testing.T) {
	url := "https://latest.dev.babelforce.com/storage/c/3f312769-f0a8-45f3-b2d9-8e56998b3b26/prompt/5/c/5cadeacc36409649bcda06780a71ca4380e45d5d715c0b3c01a3563fb77f0df9.wav"
	sess, err := sipgox.NewMediaSession(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	require.NoError(t, err)
	defer sess.Close()

	dialog := DialogMedia{
		Session:   sess,
		RTPWriter: sipgox.NewRTPWriter(sess),
	}
	sess.SetRemoteAddr(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9999})

	// udpDump, err := net.ListenUDP("udp4", sess.Raddr)
	// require.NoError(t, err)
	// defer udpDump.Close()

	sessReceive, err := sipgox.NewMediaSession(sess.Raddr)
	require.NoError(t, err)

	defer sessReceive.Close()
	rtpReader := sipgox.NewRTPReader(sessReceive)
	pcmDecoder, _ := audio.NewPCMDecoder(rtpReader.PayloadType, rtpReader)

	go func() {
		data, _ := io.ReadAll(pcmDecoder)
		file, _ := os.OpenFile("/tmp/test-integration-url.wav", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0755)
		audio.WavWriteVoipPCM(file, data)
	}()

	err = dialog.PlaybackURL(context.TODO(), url)
	require.NoError(t, err)
	time.Sleep(2 * time.Second)
}
