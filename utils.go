// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"
	"sync"

	"github.com/emiago/diago/media"
	"github.com/pion/rtp"
)

var rtpBufPool = sync.Pool{
	New: func() any {
		return make([]byte, media.RTPBufSize)
	},
}

func copyWithBuf(reader io.Reader, writer io.Writer, payloadBuf []byte) (int64, error) {
	return media.CopyWithBuf(reader, writer, payloadBuf)
}

func closeAndLog(closer io.Closer, msg string) {
	if err := closer.Close(); err != nil {
		slog.Error(msg, "error", err)
	}
}

type rtpWriterBuffer struct {
	buf []*rtp.Packet
}

func newRTPWriterBuffer() *rtpWriterBuffer {
	return &rtpWriterBuffer{
		buf: make([]*rtp.Packet, 0, 1000),
	}
}

func (w *rtpWriterBuffer) WriteRTP(p *rtp.Packet) error {
	w.buf = append(w.buf, p)
	return nil
}

type loggingTransport struct{}

func (s *loggingTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	bytes, _ := httputil.DumpRequestOut(r, false)

	resp, err := http.DefaultTransport.RoundTrip(r)
	// err is returned after dumping the response

	respBytes, _ := httputil.DumpResponse(resp, false)
	bytes = append(bytes, respBytes...)

	slog.Debug(fmt.Sprintf("HTTP Debug:\n%s\n", bytes))

	return resp, err
}
