// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/emiago/sipgo/sip"
)

// A registrar may grant a shorter lifetime than requested, and the common way is
// a per-Contact expires param rather than a top level Expires header. Reading
// only the top level header meant a 120s grant was recorded as the requested 1h,
// so the binding lapsed long before the refresh fired.
func TestGrantedExpiry(t *testing.T) {
	const requested = time.Hour

	for _, tc := range []struct {
		name    string
		headers []sip.Header
		want    time.Duration
	}{
		{
			name:    "contact expires param",
			headers: []sip.Header{sip.NewHeader("Contact", "<sip:alice@10.0.0.5:5060>;expires=120")},
			want:    120 * time.Second,
		},
		{
			name:    "top level Expires header",
			headers: []sip.Header{sip.NewHeader("Expires", "300")},
			want:    300 * time.Second,
		},
		{
			// The per-Contact param is the binding's own lifetime, so it wins.
			name: "contact param beats top level Expires",
			headers: []sip.Header{
				sip.NewHeader("Contact", "<sip:alice@10.0.0.5:5060>;expires=120"),
				sip.NewHeader("Expires", "3600"),
			},
			want: 120 * time.Second,
		},
		{
			// A registrar that grants silently: the requested value stands.
			name: "no header falls back to requested",
			want: requested,
		},
		{
			// expires=0 is a de-registration echo, not a grant. A zero lifetime
			// would otherwise pin calcRetry to its floor forever.
			name:    "non positive grant ignored",
			headers: []sip.Header{sip.NewHeader("Contact", "<sip:alice@10.0.0.5:5060>;expires=0")},
			want:    requested,
		},
		{
			// Used to be a hard error that replaced the response.
			name:    "unparseable grant falls back to requested",
			headers: []sip.Header{sip.NewHeader("Expires", "soon")},
			want:    requested,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := sip.NewResponse(sip.StatusOK, "OK")
			for _, h := range tc.headers {
				res.AppendHeader(h)
			}
			if got := grantedExpiry(res, requested); got != tc.want {
				t.Errorf("grantedExpiry() = %v, want %v", got, tc.want)
			}
		})
	}
}

// The refresh cadence must come from what the registrar granted. calcRetry used
// to return RetryInterval verbatim whenever it was set, so the grant was never
// consulted and a 120s grant still refreshed on the caller's 30m clock.
func TestCalcRetry(t *testing.T) {
	for _, tc := range []struct {
		name          string
		retryInterval time.Duration
		expiry        time.Duration
		want          time.Duration
	}{
		{name: "ratio of the grant", expiry: 120 * time.Second, want: 90 * time.Second},
		{name: "ratio of a long grant", expiry: time.Hour, want: 45 * time.Minute},
		{
			// The whole point: 30m against a 120s grant used to win outright and
			// the binding lapsed 28 minutes before the refresh.
			name:          "RetryInterval past the grant is clamped",
			retryInterval: 30 * time.Minute,
			expiry:        120 * time.Second,
			want:          90 * time.Second,
		},
		{
			name:          "RetryInterval inside the grant still honoured",
			retryInterval: 10 * time.Minute,
			expiry:        time.Hour,
			want:          10 * time.Minute,
		},
		{
			// A zero interval would busy loop the REGISTER path.
			name:   "absent expiry floors",
			expiry: 0,
			want:   registerRetryMin,
		},
		{
			name:          "absent expiry with a caller interval",
			retryInterval: 5 * time.Second,
			expiry:        0,
			want:          5 * time.Second,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tr := &RegisterTransaction{opts: RegisterOptions{RetryInterval: tc.retryInterval}}
			if got := tr.calcRetry(tc.expiry); got != tc.want {
				t.Errorf("calcRetry(%v) = %v, want %v", tc.expiry, got, tc.want)
			}
		})
	}
}

// A registrar is free to refuse an account with a challenge-less 401. Running
// digest against an absent challenge returns a bare "No WWW-Authenticate header
// present" that replaces the real response, hiding the status code from the
// caller's classifier.
func TestIsDigestChallenge(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		header string
		want   bool
	}{
		{name: "401 with challenge", status: sip.StatusUnauthorized, header: "WWW-Authenticate", want: true},
		{name: "401 without challenge", status: sip.StatusUnauthorized, want: false},
		{name: "407 with challenge", status: sip.StatusProxyAuthRequired, header: "Proxy-Authenticate", want: true},
		{name: "407 without challenge", status: sip.StatusProxyAuthRequired, want: false},
		{name: "401 carrying only the proxy challenge", status: sip.StatusUnauthorized, header: "Proxy-Authenticate", want: false},
		{name: "200", status: sip.StatusOK, want: false},
		{name: "403", status: sip.StatusForbidden, header: "WWW-Authenticate", want: false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			res := sip.NewResponse(tc.status, "")
			if tc.header != "" {
				res.AppendHeader(sip.NewHeader(tc.header, `Digest realm="test", nonce="abc"`))
			}
			if got := isDigestChallenge(res); got != tc.want {
				t.Errorf("isDigestChallenge(%d) = %v, want %v", tc.status, got, tc.want)
			}
		})
	}
}

var errRegisterRefresh = errors.New("register refresh failed")

// The production constants are seconds and minutes, so the loop takes its bounds
// as parameters and the tests drive it at millisecond scale.
const (
	testBackoffInitial = 10 * time.Millisecond
	testBackoffMax     = 80 * time.Millisecond
)

// fakeRefresher drives registerRefreshLoop without a registrar.
type fakeRefresher struct {
	results    []error // one per refresh call, in order
	calls      int
	at         []time.Time // wall time of each refresh call, for gap assertions
	isExpired  bool
	expiredAt  int // call index (1 based) at/after which expired() flips
	cadenceDur time.Duration
	stop       context.CancelFunc
	stopAfter  int // cancel ctx once this many refreshes have run (0 = never)
}

func (f *fakeRefresher) refresh(context.Context) error {
	var err error
	if f.calls < len(f.results) {
		err = f.results[f.calls]
	}
	f.calls++
	f.at = append(f.at, time.Now())
	if f.expiredAt > 0 && f.calls >= f.expiredAt {
		f.isExpired = true
	}
	if f.stopAfter > 0 && f.calls >= f.stopAfter && f.stop != nil {
		f.stop()
	}
	return err
}

func (f *fakeRefresher) cadence() time.Duration { return f.cadenceDur }
func (f *fakeRefresher) expired() bool          { return f.isExpired }

// gap reports the wait the loop took before refresh call n (1 based).
func (f *fakeRefresher) gap(n int) time.Duration { return f.at[n-1].Sub(f.at[n-2]) }

// The loop used to return the first refresh error, tearing the registration down
// on one 503 or one network blip while the registrar still held a perfectly
// valid binding. A failure whose binding has not lapsed must be retried.
func TestRegisterRefreshLoopBacksOffInsteadOfTearingDown(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	f := &fakeRefresher{
		// Four consecutive failures, none of which lapse the binding.
		results:    []error{errRegisterRefresh, errRegisterRefresh, errRegisterRefresh, errRegisterRefresh},
		cadenceDur: time.Millisecond,
		stop:       cancel,
		stopAfter:  4,
	}

	err := registerRefreshLoop(ctx, time.Millisecond, testBackoffInitial, testBackoffMax, f)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("loop = %v, want context.Canceled: a failure that has not lapsed the binding must be retried, never escalated", err)
	}
	if f.calls != 4 {
		t.Fatalf("refresh called %d times, want 4, it must keep retrying through failures", f.calls)
	}

	// The retries must actually back off: 10ms, 20ms, 40ms. Asserted as a lower
	// bound only, since a loaded CI box can stretch a timer but never shrink it.
	for n, min := range map[int]time.Duration{2: testBackoffInitial, 3: 2 * testBackoffInitial, 4: 4 * testBackoffInitial} {
		if got := f.gap(n); got < min {
			t.Errorf("wait before refresh #%d = %v, want >= %v, backoff must double", n, got, min)
		}
	}
}

// The other half: a binding whose granted lifetime has run out is genuinely
// gone, retrying is pointless, and the error must reach the caller.
func TestRegisterRefreshLoopEscalatesOnlyWhenProvablyDead(t *testing.T) {
	f := &fakeRefresher{
		results:    []error{errRegisterRefresh, errRegisterRefresh, errRegisterRefresh},
		cadenceDur: time.Millisecond,
		expiredAt:  3, // binding lapses only by the 3rd failure
	}

	err := registerRefreshLoop(context.Background(), time.Millisecond, testBackoffInitial, testBackoffMax, f)

	if !errors.Is(err, errRegisterRefresh) {
		t.Fatalf("loop = %v, want errRegisterRefresh once the grant has lapsed", err)
	}
	if f.calls != 3 {
		t.Fatalf("refresh called %d times, want exactly 3, it must escalate on the first failure after the grant lapsed, not before", f.calls)
	}
}

// A recovered refresh must drop out of backoff and return to the granted expiry
// cadence. Without the reset, a binding that had a rough hour keeps refreshing
// at the 5m cap even after the registrar returns, and a registrar granting less
// than 5m would then lapse on every cycle.
func TestRegisterRefreshLoopSuccessResetsBackoff(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	f := &fakeRefresher{
		// fail, fail, fail (backoff now at 80ms), OK, fail
		results:    []error{errRegisterRefresh, errRegisterRefresh, errRegisterRefresh, nil, errRegisterRefresh},
		cadenceDur: time.Millisecond,
		stop:       cancel,
		stopAfter:  5,
	}

	err := registerRefreshLoop(ctx, time.Millisecond, testBackoffInitial, testBackoffMax, f)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("loop = %v, want context.Canceled", err)
	}
	if f.calls != 5 {
		t.Fatalf("refresh called %d times, want 5", f.calls)
	}

	// #5 follows the OK and so must wait only the 1ms cadence, not the 80ms
	// backoff step it would otherwise have taken.
	if got := f.gap(5); got >= 4*testBackoffInitial {
		t.Errorf("wait before the post recovery refresh = %v, want well under %v: a successful refresh must reset the backoff to the granted expiry cadence", got, 4*testBackoffInitial)
	}
}

// A clean shutdown is surfaced as the context error, never mistaken for a dead
// binding.
func TestRegisterRefreshLoopExitsOnContextCancel(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	f := &fakeRefresher{cadenceDur: time.Millisecond}

	err := registerRefreshLoop(ctx, time.Hour, testBackoffInitial, testBackoffMax, f)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("loop = %v, want context.Canceled", err)
	}
	if f.calls != 0 {
		t.Fatalf("refresh called %d times, want 0 on an already cancelled context", f.calls)
	}
}

// expired() is the contract the loop's escalation arm rests on.
func TestExpired(t *testing.T) {
	t.Run("never registered", func(t *testing.T) {
		tr := &RegisterTransaction{}
		if tr.expired() {
			t.Error("expired() = true with no successful REGISTER; there is no binding to lose, and the initial Register's own error already covers that case")
		}
	})

	t.Run("still inside the grant", func(t *testing.T) {
		tr := &RegisterTransaction{}
		tr.recordGrant(time.Hour)
		if tr.expired() {
			t.Error("expired() = true 0s into a 1h grant; a refresh failure here must back off, not escalate")
		}
	})

	t.Run("past the grant, latching", func(t *testing.T) {
		tr := &RegisterTransaction{}
		tr.recordGrant(time.Minute)
		tr.mu.Lock()
		tr.lastRegistered = time.Now().Add(-2 * time.Minute)
		tr.mu.Unlock()

		if !tr.expired() {
			t.Fatal("expired() = false past the grant, want true")
		}
		if tr.expired() {
			t.Error("expired() = true twice; it must latch so one outage escalates once")
		}
	})

	t.Run("a successful refresh rearms the latch", func(t *testing.T) {
		tr := &RegisterTransaction{}
		tr.recordGrant(time.Minute)
		tr.mu.Lock()
		tr.lastRegistered = time.Now().Add(-2 * time.Minute)
		tr.mu.Unlock()

		if !tr.expired() {
			t.Fatal("expired() = false past the grant, want true")
		}

		tr.recordGrant(time.Minute) // registrar came back
		tr.mu.Lock()
		tr.lastRegistered = time.Now().Add(-2 * time.Minute)
		tr.mu.Unlock()

		if !tr.expired() {
			t.Error("expired() = false after a successful refresh rearmed the latch; a second, later outage must be able to escalate again")
		}
	})
}

// Diago.Register writes a Retry-After into the refresh cadence on a 503. That
// must not disturb the recorded grant, or an otherwise live binding looks
// already lapsed and trips the escalation arm.
func TestSetExpiryLeavesGrantIntact(t *testing.T) {
	tr := &RegisterTransaction{}
	tr.recordGrant(time.Hour)

	tr.setExpiry(5 * time.Second)

	if got := tr.getExpiry(); got != 5*time.Second {
		t.Errorf("getExpiry() = %v, want the 5s Retry-After", got)
	}
	if tr.expired() {
		t.Error("expired() = true after a Retry-After shortened only the cadence; the 1h grant is still live")
	}
}
