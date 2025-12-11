// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"context"
	"log/slog"
	"net"
	"sync/atomic"

	"github.com/emiago/sipgo/sip"
)

type connRecorder struct {
	msgs []sip.Message

	ref atomic.Int32
}

func NewConnRecorder() *connRecorder {
	return &connRecorder{}
}

func (c *connRecorder) LocalAddr() net.Addr {
	return nil
}

func (c *connRecorder) WriteMsg(msg sip.Message) error {
	c.msgs = append(c.msgs, msg)
	return nil
}
func (c *connRecorder) Ref(i int) int {
	return int(c.ref.Add(int32(i)))
}
func (c *connRecorder) TryClose() (int, error) {
	new := c.ref.Add(int32(-1))
	return int(new), nil
}
func (c *connRecorder) Close() error { return nil }

type clientTxRequester struct {
	// rec *siptest.ClientTxRecorder
	onRequest func(req *sip.Request) *sip.Response
}

func (r *clientTxRequester) Request(ctx context.Context, req *sip.Request) (sip.ClientTransaction, error) {
	key, _ := sip.ClientTxKeyMake(req)
	rec := NewConnRecorder()
	tx := sip.NewClientTx(key, req, rec, slog.Default())
	if err := tx.Init(); err != nil {
		return nil, err
	}

	resp := r.onRequest(req)
	go tx.Receive(resp)

	return tx, nil
}
