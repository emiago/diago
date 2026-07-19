// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/emiago/sipgo/sip"
)

// errRefresh is a stand-in session-refresh failure returned by the fake refresh.
var errRefresh = errors.New("session refresh failed")

// u32 returns a pointer to the given uint32, for building offered header values.
func u32(v uint32) *uint32 { return &v }

// --- sessionRefreshLoop ------------------------------------------------------

// TestSessionRefreshLoop_EscalatesOnFirstRefreshError verifies the loop returns
// the refresh error immediately on the first failure. There is no failure
// counter: a failed re-INVITE means the dialog is at risk, unlike a tolerated
// liveness blip.
func TestSessionRefreshLoop_EscalatesOnFirstRefreshError(t *testing.T) {
	calls := 0
	refresh := func(context.Context) error {
		calls++
		return errRefresh
	}

	err := sessionRefreshLoop(context.Background(), time.Millisecond, refresh)

	if !errors.Is(err, errRefresh) {
		t.Fatalf("err = %v, want errRefresh", err)
	}
	if calls != 1 {
		t.Fatalf("refresh called %d times, want exactly 1 (escalate on first error)", calls)
	}
}

// TestSessionRefreshLoop_ExitsOnContextCancel verifies the loop returns the
// context error rather than a refresh escalation when ctx is cancelled, such as
// on dialog Close or Bye, and that the goroutine exits.
func TestSessionRefreshLoop_ExitsOnContextCancel(t *testing.T) {
	refresh := func(context.Context) error { return nil }

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() { done <- sessionRefreshLoop(ctx, time.Millisecond, refresh) }()

	select {
	case err := <-done:
		if !errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("err = %v, want context.DeadlineExceeded", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("loop did not exit within 2s of context cancel")
	}
}

// TestSessionRefreshLoop_ContextCancelDuringRefreshTakesPrecedence verifies that
// a refresh failing because its ctx was cancelled mid-call is surfaced as the
// context error, never the refresh error. Clean shutdown takes precedence.
func TestSessionRefreshLoop_ContextCancelDuringRefreshTakesPrecedence(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	refresh := func(context.Context) error {
		cancel()
		return errRefresh
	}

	err := sessionRefreshLoop(ctx, time.Millisecond, refresh)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want context.Canceled (ctx error precedence)", err)
	}
}

// --- armPeerRefreshWatchdog ---------------------------------------------------

// TestPeerRefreshWatchdog_FiresByeOnExpiry verifies a watchdog with no reset
// hangs up exactly once within the expiry window and then its goroutine exits.
func TestPeerRefreshWatchdog_FiresByeOnExpiry(t *testing.T) {
	byeCh := make(chan struct{}, 2)
	sendBye := func(context.Context) error {
		byeCh <- struct{}{}
		return nil
	}

	w := armPeerRefreshWatchdog(context.Background(), 20*time.Millisecond, 20*time.Millisecond, sendBye)
	defer w.Stop()

	select {
	case <-byeCh:
	case <-time.After(2 * time.Second):
		t.Fatal("watchdog did not BYE within 2s of expiry")
	}

	// Single shot: it must not fire a second BYE.
	select {
	case <-byeCh:
		t.Fatal("watchdog fired BYE more than once")
	case <-time.After(100 * time.Millisecond):
	}

	select {
	case <-w.done:
	case <-time.After(2 * time.Second):
		t.Fatal("watchdog goroutine did not exit after firing")
	}
}

// TestPeerRefreshWatchdog_ResetPreventsBye verifies that periodic Reset before
// expiry keeps the dialog alive, so sendBye is never called during the window.
func TestPeerRefreshWatchdog_ResetPreventsBye(t *testing.T) {
	var byes atomic.Int32
	sendBye := func(context.Context) error {
		byes.Add(1)
		return nil
	}

	// expiry 100ms, resetting every 25ms must keep it armed.
	w := armPeerRefreshWatchdog(context.Background(), 100*time.Millisecond, 0, sendBye)

	deadline := time.Now().Add(250 * time.Millisecond)
	for time.Now().Before(deadline) {
		w.Reset()
		time.Sleep(25 * time.Millisecond)
	}

	if got := byes.Load(); got != 0 {
		t.Fatalf("watchdog fired %d BYEs during the reset window, want 0", got)
	}

	w.Stop()
	select {
	case <-w.done:
	case <-time.After(2 * time.Second):
		t.Fatal("watchdog goroutine did not exit after Stop")
	}
}

// TestPeerRefreshWatchdog_ExitsOnContextCancel verifies the watchdog exits on ctx
// cancel without firing a BYE, covering a dialog torn down before the
// peer-refresh window elapses.
func TestPeerRefreshWatchdog_ExitsOnContextCancel(t *testing.T) {
	var byes atomic.Int32
	sendBye := func(context.Context) error {
		byes.Add(1)
		return nil
	}

	ctx, cancel := context.WithCancel(context.Background())
	// Long expiry so only ctx cancel can end the watchdog.
	w := armPeerRefreshWatchdog(ctx, 10*time.Second, 0, sendBye)
	cancel()

	select {
	case <-w.done:
	case <-time.After(2 * time.Second):
		t.Fatal("watchdog did not exit within 2s of context cancel")
	}

	if got := byes.Load(); got != 0 {
		t.Fatalf("watchdog fired %d BYEs on ctx-cancel, want 0", got)
	}
}

// TestPeerRefreshWatchdog_GraceResolvesViaPolicy verifies the grace source: the
// policy field wins when set, otherwise the constant fallback applies.
func TestPeerRefreshWatchdog_GraceResolvesViaPolicy(t *testing.T) {
	if got := watchdogGrace(SessionTimerPolicy{}); got != defaultWatchdogGrace {
		t.Fatalf("watchdogGrace(zero) = %v, want %v", got, defaultWatchdogGrace)
	}
	if got := watchdogGrace(SessionTimerPolicy{WatchdogGrace: 3 * time.Second}); got != 3*time.Second {
		t.Fatalf("watchdogGrace(3s) = %v, want 3s", got)
	}
}

// --- negotiate ----------------------------------------------------------------

// TestSessionTimerNegotiate exercises the pure, role-agnostic RFC 4028
// negotiation: the Min-SE floor, the advertise-fallback when nothing is offered,
// the half refresh interval with anti-storm clamp, honoring the peer's
// refresher, the PreferUASRefresher tiebreaker only when the peer omits one, and
// the below-floor 422 signal.
func TestSessionTimerNegotiate(t *testing.T) {
	tests := []struct {
		name             string
		offeredSE        *uint32
		offeredMinSE     *uint32
		offeredRefresher string
		policy           SessionTimerPolicy
		ourRole          string
		want             sessionTimerDecision
	}{
		{
			name:    "none offered uses advertise-fallback, uas tiebreaker, we refresh",
			policy:  SessionTimerPolicy{PreferUASRefresher: true},
			ourRole: refresherUAS,
			want: sessionTimerDecision{
				Negotiated: defaultSessionExpires,
				Interval:   defaultSessionExpires / 2,
				Refresher:  refresherUAS,
				WeRefresh:  true,
			},
		},
		{
			name:             "peer refresher honored over our preference",
			offeredSE:        u32(1200),
			offeredRefresher: refresherUAC,
			policy:           SessionTimerPolicy{PreferUASRefresher: true},
			ourRole:          refresherUAS,
			want: sessionTimerDecision{
				Negotiated: 1200 * time.Second,
				Interval:   600 * time.Second,
				Refresher:  refresherUAC,
				WeRefresh:  false,
			},
		},
		{
			name:      "no peer refresher, PreferUAS true -> uas tiebreaker",
			offeredSE: u32(1800),
			policy:    SessionTimerPolicy{PreferUASRefresher: true},
			ourRole:   refresherUAS,
			want: sessionTimerDecision{
				Negotiated: 1800 * time.Second,
				Interval:   900 * time.Second,
				Refresher:  refresherUAS,
				WeRefresh:  true,
			},
		},
		{
			name:      "no peer refresher, PreferUAS false -> neutral, not forced uas",
			offeredSE: u32(1800),
			policy:    SessionTimerPolicy{PreferUASRefresher: false},
			ourRole:   refresherUAS,
			want: sessionTimerDecision{
				Negotiated: 1800 * time.Second,
				Interval:   900 * time.Second,
				Refresher:  "",
				WeRefresh:  false,
			},
		},
		{
			name:      "offer below floor -> below-floor 422 signal, negotiated reports the floor",
			offeredSE: u32(30),
			policy:    SessionTimerPolicy{PreferUASRefresher: true},
			ourRole:   refresherUAS,
			want: sessionTimerDecision{
				Negotiated: defaultMinSE,
				Interval:   defaultMinSE / 2,
				Refresher:  refresherUAS,
				BelowFloor: true,
				WeRefresh:  true,
			},
		},
		{
			name:         "peer Min-SE raises the floor above ours",
			offeredSE:    u32(1800),
			offeredMinSE: u32(120),
			policy:       SessionTimerPolicy{PreferUASRefresher: true},
			ourRole:      refresherUAS,
			want: sessionTimerDecision{
				Negotiated: 1800 * time.Second,
				Interval:   900 * time.Second,
				Refresher:  refresherUAS,
				WeRefresh:  true,
			},
		},
		{
			name:         "peer Min-SE above the offer raises the negotiated interval",
			offeredSE:    u32(100),
			offeredMinSE: u32(600),
			policy:       SessionTimerPolicy{PreferUASRefresher: true},
			ourRole:      refresherUAS,
			want: sessionTimerDecision{
				Negotiated: 600 * time.Second,
				Interval:   300 * time.Second,
				Refresher:  refresherUAS,
				BelowFloor: true,
				WeRefresh:  true,
			},
		},
		{
			name:    "configurable DefaultSE advertise-fallback is used, not the constant",
			policy:  SessionTimerPolicy{DefaultSE: 600 * time.Second, PreferUASRefresher: true},
			ourRole: refresherUAS,
			want: sessionTimerDecision{
				Negotiated: 600 * time.Second,
				Interval:   300 * time.Second,
				Refresher:  refresherUAS,
				WeRefresh:  true,
			},
		},
		{
			name:             "we are uac and peer elected uac -> we refresh",
			offeredSE:        u32(1800),
			offeredRefresher: refresherUAC,
			policy:           SessionTimerPolicy{},
			ourRole:          refresherUAC,
			want: sessionTimerDecision{
				Negotiated: 1800 * time.Second,
				Interval:   900 * time.Second,
				Refresher:  refresherUAC,
				WeRefresh:  true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := negotiate(tt.offeredSE, tt.offeredMinSE, tt.offeredRefresher, tt.policy, tt.ourRole)
			if got != tt.want {
				t.Fatalf("negotiate() = %+v, want %+v", got, tt.want)
			}
			// Anti-storm: the refresh cadence is never sub-floor, even for a tiny
			// offer.
			if got.Interval < minRefreshInterval {
				t.Fatalf("Interval = %v, want >= minRefreshInterval %v", got.Interval, minRefreshInterval)
			}
		})
	}
}

// --- parseSessionTimerOffer ---------------------------------------------------

// TestParseSessionTimerOffer pins the header shim that stands in for the typed
// accessors sipgo does not provide: the delta and refresher param are read off
// Session-Expires, Min-SE is optional, the compact form "x" is accepted, and a
// malformed value is treated as absent rather than as an error.
func TestParseSessionTimerOffer(t *testing.T) {
	tests := []struct {
		name          string
		headers       [][2]string
		wantOK        bool
		wantSE        *uint32
		wantMinSE     *uint32
		wantRefresher string
	}{
		{
			name:          "delta with refresher param",
			headers:       [][2]string{{"Session-Expires", "1800;refresher=uac"}},
			wantOK:        true,
			wantSE:        u32(1800),
			wantRefresher: refresherUAC,
		},
		{
			name:    "bare delta, no params",
			headers: [][2]string{{"Session-Expires", "1800"}},
			wantOK:  true,
			wantSE:  u32(1800),
		},
		{
			name:          "refresher param is case insensitive",
			headers:       [][2]string{{"Session-Expires", "1800;REFRESHER=UAS"}},
			wantOK:        true,
			wantSE:        u32(1800),
			wantRefresher: refresherUAS,
		},
		{
			name:      "Min-SE is parsed alongside",
			headers:   [][2]string{{"Session-Expires", "1800"}, {"Min-SE", "120"}},
			wantOK:    true,
			wantSE:    u32(1800),
			wantMinSE: u32(120),
		},
		{
			name:          "compact form x is accepted",
			headers:       [][2]string{{"x", "1800;refresher=uas"}},
			wantOK:        true,
			wantSE:        u32(1800),
			wantRefresher: refresherUAS,
		},
		{
			name:    "no Session-Expires means timer-unaware peer",
			headers: nil,
			wantOK:  false,
		},
		{
			name:    "malformed delta is treated as absent, never an error",
			headers: [][2]string{{"Session-Expires", "not-a-number"}},
			wantOK:  false,
		},
		{
			name:    "out-of-range delta is treated as absent",
			headers: [][2]string{{"Session-Expires", "4294967296"}},
			wantOK:  false,
		},
		{
			name:      "malformed Min-SE is ignored, Session-Expires still stands",
			headers:   [][2]string{{"Session-Expires", "1800"}, {"Min-SE", "bogus"}},
			wantOK:    true,
			wantSE:    u32(1800),
			wantMinSE: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := sip.NewRequest(sip.INVITE, sip.Uri{User: "alice", Host: "example.com"})
			for _, h := range tt.headers {
				req.AppendHeader(sip.NewHeader(h[0], h[1]))
			}

			offer, ok := parseSessionTimerOffer(req)
			if ok != tt.wantOK {
				t.Fatalf("ok = %v, want %v", ok, tt.wantOK)
			}
			if !tt.wantOK {
				return
			}
			if got, want := derefU32(offer.SE), derefU32(tt.wantSE); got != want {
				t.Fatalf("SE = %v, want %v", got, want)
			}
			if got, want := derefU32(offer.MinSE), derefU32(tt.wantMinSE); got != want {
				t.Fatalf("MinSE = %v, want %v", got, want)
			}
			if offer.Refresher != tt.wantRefresher {
				t.Fatalf("Refresher = %q, want %q", offer.Refresher, tt.wantRefresher)
			}
		})
	}
}

// derefU32 renders an optional uint32 as a comparable value, using -1 for nil so
// "absent" and zero stay distinguishable in test failures.
func derefU32(p *uint32) int64 {
	if p == nil {
		return -1
	}
	return int64(*p)
}

// --- buildTimerAnswer ---------------------------------------------------------

// inviteWithSE builds a minimal INVITE carrying the given Session-Expires value.
// An empty se models a timer-unaware peer that sent no Session-Expires.
func inviteWithSE(se string) *sip.Request {
	req := sip.NewRequest(sip.INVITE, sip.Uri{User: "alice", Host: "example.com"})
	if se != "" {
		req.AppendHeader(sip.NewHeader("Session-Expires", se))
	}
	return req
}

// answerHeader returns the value of the first answer header matching name.
func answerHeader(hdrs []sip.Header, name string) (string, bool) {
	for _, h := range hdrs {
		if strings.EqualFold(h.Name(), name) {
			return h.Value(), true
		}
	}
	return "", false
}

// TestTimerAnswer pins the answer-path decision over the pure buildTimerAnswer
// helper: honor the peer's offered refresher, advertise Supported: timer and
// never Require: timer, reject below-floor with 422 plus Min-SE, no-op
// gracefully for a timer-unaware peer, and elect loop versus watchdog.
func TestTimerAnswer(t *testing.T) {
	tests := []struct {
		name            string
		se              string
		policy          SessionTimerPolicy
		wantStatus      int
		wantSE          string // expected Session-Expires value, "" means none expected
		wantSupported   bool
		wantMinSE       string // expected Min-SE value on a 422, "" means none
		wantAnswer      bool
		wantWeRefresh   bool
		wantArmWatchdog bool
	}{
		{
			// Peer offered refresher=uac: the answer honors uac rather than forcing
			// uas, we are not the refresher, so the decision is to arm the watchdog.
			name:            "honor peer refresher=uac -> arm watchdog",
			se:              "1800;refresher=uac",
			policy:          SessionTimerPolicy{Enabled: true, PreferUASRefresher: true},
			wantStatus:      200,
			wantSE:          "1800;refresher=uac",
			wantSupported:   true,
			wantAnswer:      true,
			wantWeRefresh:   false,
			wantArmWatchdog: true,
		},
		{
			// Peer omitted the refresher: the PreferUASRefresher tiebreaker elects
			// uas, we are the refresher, so the decision is to start the loop.
			name:            "omitted refresher, PreferUAS -> refresher=uas, start loop",
			se:              "1800",
			policy:          SessionTimerPolicy{Enabled: true, PreferUASRefresher: true},
			wantStatus:      200,
			wantSE:          "1800;refresher=uas",
			wantSupported:   true,
			wantAnswer:      true,
			wantWeRefresh:   true,
			wantArmWatchdog: false,
		},
		{
			// Below the 90s floor: 422 Session Interval Too Small plus Min-SE 90,
			// no loop and no watchdog.
			name:       "offer below floor -> 422 Min-SE, no loop/watchdog",
			se:         "30",
			policy:     SessionTimerPolicy{Enabled: true, PreferUASRefresher: true},
			wantStatus: 422,
			wantMinSE:  "90",
			wantAnswer: false,
		},
		{
			// Timer-unaware peer: graceful no-op with no timer headers, no loop and
			// no watchdog, answered normally and never rejected.
			name:          "no timer support -> graceful no-op",
			se:            "",
			policy:        SessionTimerPolicy{Enabled: true, PreferUASRefresher: true},
			wantStatus:    200,
			wantSE:        "",
			wantSupported: false,
			wantAnswer:    false,
		},
		{
			name:          "timers disabled -> no headers",
			se:            "1800",
			policy:        SessionTimerPolicy{Enabled: false, PreferUASRefresher: true},
			wantStatus:    200,
			wantSE:        "",
			wantSupported: false,
			wantAnswer:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ta, hdrs, status := buildTimerAnswer(inviteWithSE(tt.se), tt.policy)

			if status != tt.wantStatus {
				t.Fatalf("status = %d, want %d", status, tt.wantStatus)
			}
			if (ta != nil) != tt.wantAnswer {
				t.Fatalf("answer present = %v, want %v", ta != nil, tt.wantAnswer)
			}
			if ta != nil {
				if ta.weRefresh != tt.wantWeRefresh {
					t.Fatalf("weRefresh = %v, want %v", ta.weRefresh, tt.wantWeRefresh)
				}
				if ta.armWatchdog != tt.wantArmWatchdog {
					t.Fatalf("armWatchdog = %v, want %v", ta.armWatchdog, tt.wantArmWatchdog)
				}
			}

			// We advertise Supported: timer and never Require: timer, so a
			// timer-unaware peer is never rejected for lacking timer support.
			if _, ok := answerHeader(hdrs, "Require"); ok {
				t.Fatalf("answer must never carry Require: timer; headers=%v", hdrs)
			}

			if tt.wantSE != "" {
				gotSE, ok := answerHeader(hdrs, "Session-Expires")
				if !ok {
					t.Fatalf("missing Session-Expires header; headers=%v", hdrs)
				}
				if gotSE != tt.wantSE {
					t.Fatalf("Session-Expires = %q, want %q", gotSE, tt.wantSE)
				}
			} else if gotSE, ok := answerHeader(hdrs, "Session-Expires"); ok {
				t.Fatalf("unexpected Session-Expires = %q, want none", gotSE)
			}

			_, gotSupported := answerHeader(hdrs, "Supported")
			if gotSupported != tt.wantSupported {
				t.Fatalf("Supported present = %v, want %v; headers=%v", gotSupported, tt.wantSupported, hdrs)
			}
			if tt.wantSupported {
				if v, _ := answerHeader(hdrs, "Supported"); v != "timer" {
					t.Fatalf("Supported = %q, want \"timer\"", v)
				}
			}

			if tt.wantMinSE != "" {
				gotMinSE, ok := answerHeader(hdrs, "Min-SE")
				if !ok {
					t.Fatalf("missing Min-SE header on 422; headers=%v", hdrs)
				}
				if gotMinSE != tt.wantMinSE {
					t.Fatalf("Min-SE = %q, want %q", gotMinSE, tt.wantMinSE)
				}
			}
		})
	}
}

// TestWithSessionTimers verifies the option stamps the policy onto Diago, which
// is what the dialog construction path reads. Without the option timers stay
// disabled, preserving the existing behavior for callers that never opt in.
func TestWithSessionTimers(t *testing.T) {
	policy := SessionTimerPolicy{Enabled: true, DefaultSE: 600 * time.Second, PreferUASRefresher: true}

	dg := &Diago{}
	WithSessionTimers(policy)(dg)

	if dg.sessionTimers != policy {
		t.Fatalf("sessionTimers = %+v, want %+v", dg.sessionTimers, policy)
	}

	if (&Diago{}).sessionTimers.Enabled {
		t.Fatal("session timers must be disabled without the option")
	}
}
