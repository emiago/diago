// SPDX-License-Identifier: BSD-2-Clause
// Copyright (C) 2024 Emir Aganovic
package diago

import (
	"context"
	"io"
	"net"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/stretchr/testify/require"
)

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
		pb, _ := in.PlaybackCreate()
		if err := pb.PlayURL(ctx, urlStr); err != nil {
			t.Error(err)
		}

		t.Log("Done playing", urlStr)
		in.Hangup(in.Context())
	})
	require.NoError(t, err)

	{
		ua, _ := sipgo.NewUA()
		// phone := sipgox.NewPhone(ua, sipgox.WithPhoneListenAddr(
		// 	sipgox.ListenAddr{
		// 		Network: "udp",
		// 		Addr:    "127.0.0.100:5090",
		// 	},
		// ))

		phone := NewDiago(ua, WithTransport(Transport{
			Transport: "udp",
			BindHost:  "127.0.0.100",
			BindPort:  5090,
		}))

		dialog, err := phone.Invite(context.TODO(), sip.Uri{Host: "127.0.0.1", Port: 5090}, InviteOptions{})
		require.NoError(t, err)
		defer dialog.Close()

		rtpReader := dialog.RTPPacketReader

		go func() {
			defer dialog.Close()
			time.Sleep(10 * time.Second)
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
