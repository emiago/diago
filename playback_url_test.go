// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/emiago/diago/media"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/stretchr/testify/require"
)

func TestIntegrationPlaybackURL(t *testing.T) {
	// Create transaction users, as many as needed.
	ua, _ := sipgo.NewUA(
		sipgo.WithUserAgent("inbound"),
	)
	defer ua.Close()
	tu := NewDiago(ua, WithTransport(
		Transport{
			Transport: "udp",
			BindHost:  "127.0.0.1",
			BindPort:  15060,
		},
	))

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	// media.RTPDebug = true

	urlStr := testStartAudioStreamServer(t)

	var errServer error
	wg := sync.WaitGroup{}
	wg.Add(1)
	err := tu.ServeBackground(ctx, func(in *DialogServerSession) {
		defer wg.Done()
		in.Progress()
		in.Ringing()
		in.Answer()
		t.Log("Playing url ", urlStr)
		pb, _ := in.PlaybackCreate()
		if _, err := pb.PlayURL(urlStr); err != nil {
			errServer = errors.Join(errServer, err)
		}

		t.Log("Done playing", urlStr)
		in.Hangup(in.Context())
	})
	require.NoError(t, err)

	{
		ua, _ := sipgo.NewUA()
		defer ua.Close()
		phone := NewDiago(ua, WithTransport(Transport{
			Transport: "udp",
			BindHost:  "127.0.0.100",
			BindPort:  15060,
		}))
		// Just to have handled BYE
		err := phone.ServeBackground(context.TODO(), func(d *DialogServerSession) {})
		require.NoError(t, err)

		dialog, err := phone.Invite(context.TODO(), sip.Uri{Host: "127.0.0.1", Port: 15060}, InviteOptions{})
		require.NoError(t, err)
		defer dialog.Close()

		rtpReader := dialog.RTPPacketReader

		go func() {
			defer dialog.Close()
			time.Sleep(10 * time.Second)
			dialog.Hangup(ctx)
		}()
		b := bytes.NewBuffer([]byte{})
		written, err := media.CopyWithBuf(rtpReader, b, make([]byte, media.RTPBufSize))
		// bnf, err := io.ReadAll(rtpReader)
		require.ErrorIs(t, err, io.EOF)
		require.Greater(t, written, int64(10000))
		require.Greater(t, b.Len(), 10000)
	}

	t.Log("Waiting server goroutine to exit")
	wg.Wait()
	require.NoError(t, errServer)
}

func testStartAudioStreamServer(t *testing.T) string {
	mux := http.NewServeMux()
	mux.HandleFunc("/ping", func(writer http.ResponseWriter, request *http.Request) {
		writer.WriteHeader(200)
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, req *http.Request) {
		fh, err := os.Open("testdata/files/demo-echodone.wav")
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
		Addr:    "127.0.0.1:18080",
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
