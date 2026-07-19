// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSipfragStatus(t *testing.T) {
	tests := []struct {
		name       string
		frag       string
		wantStatus int
		wantReason string
		wantOK     bool
	}{
		{name: "trying", frag: "SIP/2.0 100 Trying", wantStatus: 100, wantReason: "Trying", wantOK: true},
		{name: "ok", frag: "SIP/2.0 200 OK", wantStatus: 200, wantReason: "OK", wantOK: true},
		{name: "busy", frag: "SIP/2.0 486 Busy Here", wantStatus: 486, wantReason: "Busy Here", wantOK: true},
		{name: "only status line is read", frag: "SIP/2.0 486 Busy Here\r\nVia: SIP/2.0/UDP 10.0.0.1", wantStatus: 486, wantReason: "Busy Here", wantOK: true},
		{name: "no reason phrase", frag: "SIP/2.0 480", wantStatus: 480, wantOK: true},
		{name: "not a status line", frag: "GARBAGE 486 Busy", wantOK: false},
		{name: "non numeric code", frag: "SIP/2.0 4x6 Busy", wantOK: false},
		{name: "code out of range", frag: "SIP/2.0 999 Nope", wantOK: false},
		{name: "code below range", frag: "SIP/2.0 42 Nope", wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, reason, ok := parseSipfragStatus(tt.frag)
			assert.Equal(t, tt.wantOK, ok)
			assert.Equal(t, tt.wantStatus, status)
			assert.Equal(t, tt.wantReason, reason)
		})
	}
}

func TestParseReferNotifyEventID(t *testing.T) {
	newReq := func(event string) *sip.Request {
		req := sip.NewRequest(sip.NOTIFY, sip.Uri{Host: "127.0.0.1"})
		if event != "" {
			req.AppendHeader(sip.NewHeader("Event", event))
		}
		return req
	}

	tests := []struct {
		name      string
		event     string
		wantID    uint32
		wantHasID bool
	}{
		{name: "id present", event: "refer;id=42", wantID: 42, wantHasID: true},
		{name: "id with spacing", event: "refer; id=7", wantID: 7, wantHasID: true},
		{name: "no id param", event: "refer", wantHasID: false},
		{name: "no event header", event: "", wantHasID: false},
		{name: "non numeric id", event: "refer;id=abc", wantHasID: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			id, hasID := parseReferNotifyEventID(newReq(tt.event))
			assert.Equal(t, tt.wantHasID, hasID)
			assert.Equal(t, tt.wantID, id)
		})
	}
}

func TestReferFailureErrorMessageHidesSIPInternals(t *testing.T) {
	err := &ReferFailureError{Status: 486, Reason: "Busy Here"}
	assert.Equal(t, "refer transfer failed: 486 Busy Here", err.Error())

	timeout := &ReferFailureError{Status: 0, Reason: "timeout"}
	assert.Equal(t, "refer transfer failed: timeout", timeout.Error())

	// The error reaches callers that surface it; it must never carry routing state.
	for _, banned := range []string{"Via", "Contact", "@", "SIP/2.0"} {
		assert.NotContains(t, err.Error(), banned)
	}
}

// TestReferWaiterCorrelation covers the delivery rules of the in-flight REFER
// waiter without needing a full dialog: an Event id must match the attempt's
// CSeq, a NOTIFY with no id falls back to the single in-flight attempt, and a
// finished attempt must not absorb a late NOTIFY.
func TestReferWaiterCorrelation(t *testing.T) {
	term := referTerminal{status: 486, reason: "Busy Here"}

	t.Run("no id delivers to single in flight attempt", func(t *testing.T) {
		d := &DialogMedia{}
		w := d.beginReferAttempt()
		d.setReferAttemptCSeq(w, 5)

		d.deliverReferResult(0, false, term)
		assert.Equal(t, term, <-w.ch)
	})

	t.Run("matching id delivers", func(t *testing.T) {
		d := &DialogMedia{}
		w := d.beginReferAttempt()
		d.setReferAttemptCSeq(w, 5)

		d.deliverReferResult(5, true, term)
		assert.Equal(t, term, <-w.ch)
	})

	t.Run("stale id is dropped", func(t *testing.T) {
		d := &DialogMedia{}
		w := d.beginReferAttempt()
		d.setReferAttemptCSeq(w, 5)

		// A NOTIFY for an earlier REFER on the same dialog must not resolve this one.
		d.deliverReferResult(4, true, term)
		assert.Empty(t, w.ch)
	})

	t.Run("id before cseq known is delivered", func(t *testing.T) {
		d := &DialogMedia{}
		w := d.beginReferAttempt()

		// NOTIFY racing ahead of the CSeq being recorded cannot be judged stale.
		d.deliverReferResult(9, true, term)
		assert.Equal(t, term, <-w.ch)
	})

	t.Run("finished attempt does not absorb late notify", func(t *testing.T) {
		d := &DialogMedia{}
		w := d.beginReferAttempt()
		d.endReferAttempt(w)

		d.deliverReferResult(0, false, term)
		assert.Empty(t, w.ch)
	})

	t.Run("ended attempt does not clear a newer one", func(t *testing.T) {
		d := &DialogMedia{}
		old := d.beginReferAttempt()
		fresh := d.beginReferAttempt()
		d.endReferAttempt(old)

		d.deliverReferResult(0, false, term)
		assert.Equal(t, term, <-fresh.ch)
	})

	t.Run("delivery never blocks", func(t *testing.T) {
		d := &DialogMedia{}
		w := d.beginReferAttempt()
		d.setReferAttemptCSeq(w, 1)

		// Second delivery must not block once the mailbox is full.
		d.deliverReferResult(0, false, term)
		done := make(chan struct{})
		go func() {
			d.deliverReferResult(0, false, term)
			close(done)
		}()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("deliverReferResult blocked on a full waiter mailbox")
		}
	})
}

// TestIntegrationDialogReferWaitsForOutcome asserts the whole loop: a plain
// Refer (no OnNotify) reports the real transfer outcome rather than the 202, and
// a failed transfer surfaces as a typed *ReferFailureError carrying the status.
func TestIntegrationDialogReferWaitsForOutcome(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// UAS that receives our REFER and drives the referred INVITE.
	{
		ua, _ := sipgo.NewUA()
		defer ua.Close()

		dg := NewDiago(ua, WithTransport(
			Transport{
				Transport: "udp",
				BindHost:  "127.0.0.1",
				BindPort:  15081,
				ID:        "udp",
			},
		))

		err := dg.ServeBackground(ctx, func(d *DialogServerSession) {
			d.AnswerOptions(AnswerOptions{
				OnRefer: func(referDialog *DialogClientSession) error {
					if err := referDialog.Invite(referDialog.Context(), InviteClientOptions{}); err != nil {
						return err
					}
					if err := referDialog.Ack(ctx); err != nil {
						return err
					}
					return referDialog.Hangup(referDialog.Context())
				},
			})
			<-d.Context().Done()
		})
		require.NoError(t, err)
	}

	// Refer target.
	{
		ua, _ := sipgo.NewUA()
		defer ua.Close()

		dg := NewDiago(ua, WithTransport(
			Transport{
				Transport: "udp",
				BindHost:  "127.0.0.1",
				BindPort:  15082,
			},
		))

		err := dg.ServeBackground(ctx, func(d *DialogServerSession) {
			switch d.ToUser() {
			case "busy":
				d.Respond(sip.StatusBusyHere, "Busy Here", nil)
				return
			default:
				d.Answer()
			}
			<-d.Context().Done()
		})
		require.NoError(t, err)
	}

	// UAS that accepts the REFER (so 202 + 100 Trying are sent) but never lets the
	// referred dialog reach a terminal state, so no terminal sipfrag is ever sent.
	// Released at test end so the handler goroutine does not linger.
	stuckRefer := make(chan struct{})
	defer close(stuckRefer)
	{
		ua, _ := sipgo.NewUA()
		defer ua.Close()

		dg := NewDiago(ua, WithTransport(
			Transport{
				Transport: "udp",
				BindHost:  "127.0.0.1",
				BindPort:  15083,
				ID:        "udp",
			},
		))

		err := dg.ServeBackground(ctx, func(d *DialogServerSession) {
			d.AnswerOptions(AnswerOptions{
				OnRefer: func(referDialog *DialogClientSession) error {
					<-stuckRefer
					return nil
				},
			})
			<-d.Context().Done()
		})
		require.NoError(t, err)
	}

	ua, _ := sipgo.NewUA()
	defer ua.Close()

	dg := NewDiago(ua, WithTransport(
		Transport{
			Transport: "udp",
			BindHost:  "127.0.0.1",
			BindPort:  15080,
		},
	))
	require.NoError(t, dg.ServeBackground(ctx, nil))

	t.Run("Successful", func(t *testing.T) {
		d, err := dg.Invite(ctx, sip.Uri{Host: "127.0.0.1", Port: 15081}, InviteOptions{})
		require.NoError(t, err)
		defer d.Close()
		defer d.Hangup(d.Context())

		// Returns only once the terminal sipfrag says the transfer completed.
		require.NoError(t, d.Refer(d.Context(), sip.Uri{Host: "127.0.0.1", Port: 15082}))
	})

	t.Run("BusyReturnsTypedFailure", func(t *testing.T) {
		d, err := dg.Invite(ctx, sip.Uri{Host: "127.0.0.1", Port: 15081}, InviteOptions{})
		require.NoError(t, err)
		defer d.Close()
		defer d.Hangup(d.Context())

		err = d.Refer(d.Context(), sip.Uri{User: "busy", Host: "127.0.0.1", Port: 15082})
		require.Error(t, err, "refer to a busy target must not report success")

		var referErr *ReferFailureError
		require.True(t, errors.As(err, &referErr), "want *ReferFailureError, got %T: %v", err, err)
		assert.Equal(t, sip.StatusBusyHere, referErr.Status)
		assert.NotContains(t, referErr.Error(), "127.0.0.1")
	})

	t.Run("NoTerminalNotifyTimesOut", func(t *testing.T) {
		// Shrink the answer-supervision window so the deadline path is testable.
		orig := referAnswerDeadline
		referAnswerDeadline = 200 * time.Millisecond
		defer func() { referAnswerDeadline = orig }()

		// Talk to the peer that accepts the REFER but never completes it.
		d, err := dg.Invite(ctx, sip.Uri{Host: "127.0.0.1", Port: 15083}, InviteOptions{})
		require.NoError(t, err)
		defer d.Close()
		defer d.Hangup(d.Context())

		// The REFER is accepted (202) and 100 Trying arrives, but no terminal
		// sipfrag ever does, so the deadline must decide the outcome.
		err = d.Refer(d.Context(), sip.Uri{Host: "127.0.0.1", Port: 15082})
		require.Error(t, err, "a refer with no terminal sipfrag must not report success")

		var referErr *ReferFailureError
		require.True(t, errors.As(err, &referErr), "want *ReferFailureError, got %T: %v", err, err)
		assert.Equal(t, 0, referErr.Status, "a timeout carries no SIP status")
		assert.True(t, strings.Contains(referErr.Reason, "timeout") || strings.Contains(referErr.Reason, "cancelled"))
	})
}
