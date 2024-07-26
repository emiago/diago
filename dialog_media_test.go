package diago

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/emiago/media"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/emiago/sipgox"
	"github.com/stretchr/testify/require"
)

func TestIntegrationDialogMediaPlaybackFile(t *testing.T) {
	sess, err := media.NewMediaSession(&net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	require.NoError(t, err)
	defer sess.Close()

	// TODO have RTPSession
	rtpWriter := media.NewRTPWriterMedia(sess)
	sess.Raddr = &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 9999}

	dialog := DialogMedia{
		// MediaSession: sess,
		RTPWriter: rtpWriter,
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

func TestIntegrationDialogMediaPlaybackURL(t *testing.T) {
	// Create transaction users, as many as needed.
	ua, _ := sipgo.NewUA(
		sipgo.WithUserAgent("inbound"),
	)
	tu := NewDiago(ua, WithTransport(
		Transport{
			Transport: "udp",
			BindHost:  "127.0.0.1",
			BindPort:  5090,
		},
	))

	ctx := context.TODO()
	// media.RTPDebug = true

	urlStr := testStartAudioStreamServer(t)

	err := tu.ServeBackground(ctx, func(in *DialogServerSession) {
		in.Progress()
		in.Ringing()
		in.Answer()
		t.Log("Playing url ", urlStr)
		ctx := in.Context()
		if err := in.PlaybackURL(ctx, urlStr); err != nil {
			t.Error(err)
		}

		t.Log("Done playing", urlStr)
		in.Hangup(in.Context())
	})
	require.NoError(t, err)

	{
		ua, _ := sipgo.NewUA()
		phone := sipgox.NewPhone(ua, sipgox.WithPhoneListenAddr(
			sipgox.ListenAddr{
				Network: "udp",
				Addr:    "127.0.0.100:5090",
			},
		))
		dialog, err := phone.Dial(context.TODO(), sip.Uri{Host: "127.0.0.1", Port: 5090}, sipgox.DialOptions{})
		require.NoError(t, err)
		defer dialog.Close()

		rtpReader := media.NewRTPReaderMedia(dialog.MediaSession)

		go func() {
			defer dialog.Close()
			time.Sleep(5 * time.Second)
			dialog.Hangup(ctx)
		}()

		buf, err := io.ReadAll(rtpReader)
		require.NoError(t, err)
		require.Greater(t, len(buf), 10000)
	}
}

func testStartAudioStreamServer(t *testing.T) string {
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(200)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		fh, err := os.Open("testdata/demo-thanks.wav")
		if err != nil {
			return
		}

		// Get file info
		fileInfo, err := fh.Stat()
		if err != nil {
			http.Error(w, "Internal server error", http.StatusInternalServerError)
			return
		}

		w.Header().Add("content-type", "audio/wav")
		// w.Header().Add("cache-control", "max-age=10")
		// w.WriteHeader(http.StatusOK)
		t.Logf("Serving file %q", fh.Name())
		http.ServeContent(w, req, "audio/wav", fileInfo.ModTime(), fh)

		// _, err = io.Copy(w, fh)
		// if err != nil {
		// 	http.Error(w, err.Error(), http.StatusInternalServerError)
		// }
	})

	srv := http.Server{
		Addr:    "127.0.0.1:8080",
		Handler: mux,
	}

	l, err := net.Listen("tcp", srv.Addr)
	require.NoError(t, err)
	go srv.Serve(l)

	t.Cleanup(func() {
		srv.Shutdown(context.TODO())
	})
	return "http://" + srv.Addr + "/"
}
