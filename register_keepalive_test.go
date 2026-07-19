// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/emiago/sipgo/sip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var errProbe = errors.New("options probe failed")

// testRegisterTransaction builds a RegisterTransaction backed by the fake client
// transaction requester, so probes hit onRequest instead of a socket.
func testRegisterTransaction(t *testing.T, onRequest func(req *sip.Request) *sip.Response) *RegisterTransaction {
	t.Helper()
	dg := testDiagoClient(t, onRequest)
	rtx, err := dg.RegisterTransaction(context.TODO(), sip.Uri{User: "alice", Host: "localhost"}, RegisterOptions{})
	require.NoError(t, err)
	return rtx
}

func TestOptionsKeepaliveLoopEscalatesAfterConsecutiveFailures(t *testing.T) {
	rtx := testRegisterTransaction(t, func(req *sip.Request) *sip.Response {
		return sip.NewResponseFromRequest(req, 200, "OK", nil)
	})

	calls := 0
	probe := func(context.Context) error {
		calls++
		return errProbe
	}

	err := rtx.optionsKeepaliveLoop(context.Background(), time.Millisecond, 3, probe)

	assert.ErrorIs(t, err, errProbe)
	assert.Equal(t, 3, calls, "must escalate on the 3rd consecutive failure, not earlier or later")
}

func TestOptionsKeepaliveLoopResetsCounterOnResponse(t *testing.T) {
	rtx := testRegisterTransaction(t, func(req *sip.Request) *sip.Response {
		return sip.NewResponseFromRequest(req, 200, "OK", nil)
	})

	// fail, fail, alive (reset), fail, fail, fail -> escalate on the 6th probe
	results := []error{errProbe, errProbe, nil, errProbe, errProbe, errProbe}
	calls := 0
	probe := func(context.Context) error {
		res := results[calls]
		calls++
		return res
	}

	err := rtx.optionsKeepaliveLoop(context.Background(), time.Millisecond, 3, probe)

	assert.ErrorIs(t, err, errProbe)
	assert.Equal(t, len(results), calls, "a live peer must reset the failure counter")
}

func TestOptionsKeepaliveLoopExitsOnContextCancel(t *testing.T) {
	rtx := testRegisterTransaction(t, func(req *sip.Request) *sip.Response {
		return sip.NewResponseFromRequest(req, 200, "OK", nil)
	})

	probe := func(context.Context) error { return nil } // peer always alive

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- rtx.optionsKeepaliveLoop(ctx, time.Millisecond, 3, probe) }()

	select {
	case err := <-done:
		assert.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(2 * time.Second):
		t.Fatal("loop did not exit within 2s of context cancel")
	}
}

func TestOptionsKeepaliveLoopDoesNotCountCancellationAsFailure(t *testing.T) {
	rtx := testRegisterTransaction(t, func(req *sip.Request) *sip.Response {
		return sip.NewResponseFromRequest(req, 200, "OK", nil)
	})

	ctx, cancel := context.WithCancel(context.Background())
	probe := func(context.Context) error {
		cancel()
		return context.Canceled
	}

	err := rtx.optionsKeepaliveLoop(ctx, time.Millisecond, 3, probe)

	assert.ErrorIs(t, err, context.Canceled)
}

func TestOptionsProbe(t *testing.T) {
	t.Run("NonOKResponseIsAlive", func(t *testing.T) {
		// 405 Method Not Allowed still proves the far end answered.
		rtx := testRegisterTransaction(t, func(req *sip.Request) *sip.Response {
			return sip.NewResponseFromRequest(req, 405, "Method Not Allowed", nil)
		})

		assert.NoError(t, rtx.optionsProbe(context.Background()))
	})

	t.Run("SendsOutOfDialogOptions", func(t *testing.T) {
		var mu sync.Mutex
		var got []*sip.Request
		rtx := testRegisterTransaction(t, func(req *sip.Request) *sip.Response {
			mu.Lock()
			got = append(got, req)
			mu.Unlock()
			return sip.NewResponseFromRequest(req, 200, "OK", nil)
		})

		require.NoError(t, rtx.optionsProbe(context.Background()))
		require.NoError(t, rtx.optionsProbe(context.Background()))

		mu.Lock()
		defer mu.Unlock()
		require.Len(t, got, 2)
		for _, req := range got {
			assert.Equal(t, sip.OPTIONS, req.Method)
			assert.Equal(t, "alice", req.Recipient.User, "probe must not reuse the registrar rewritten Origin")
			_, hasToTag := req.To().Params.Get("tag")
			assert.False(t, hasToTag, "probe must be out-of-dialog")
		}
		assert.NotEqual(t, got[0].CallID().Value(), got[1].CallID().Value(), "each probe needs a fresh Call-ID")
	})

	t.Run("TransportErrorIsFailure", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		rtx := testRegisterTransaction(t, func(req *sip.Request) *sip.Response {
			return sip.NewResponseFromRequest(req, 200, "OK", nil)
		})

		assert.Error(t, rtx.optionsProbe(ctx))
	})
}

// TestOptionsProbeConcurrentWithRegister runs a probe next to the register loop,
// which is what OptionsKeepaliveLoop does. Register rewrites Origin in place, so
// under -race this catches a probe that reads Origin instead of the recipient
// captured on construction.
func TestOptionsProbeConcurrentWithRegister(t *testing.T) {
	rtx := testRegisterTransaction(t, func(req *sip.Request) *sip.Response {
		return sip.NewResponseFromRequest(req, 200, "OK", nil)
	})

	ctx := context.Background()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			assert.NoError(t, rtx.Register(ctx))
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 20; i++ {
			assert.NoError(t, rtx.optionsProbe(ctx))
		}
	}()
	wg.Wait()
}
