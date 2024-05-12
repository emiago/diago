package diago

import (
	"io"
	"net"
	"os"
	"testing"

	"github.com/emiago/sipgox"
	"github.com/stretchr/testify/require"
)

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
