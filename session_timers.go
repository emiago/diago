// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/emiago/sipgo/sip"
)

// RFC 4028 session-timer policy defaults.
const (
	// defaultSessionExpires is the session interval we advertise as a fallback
	// only when a timer-supporting peer offers no Session-Expires of its own.
	// SessionTimerPolicy.DefaultSE overrides it. It is never a value imposed on
	// a peer that offered one.
	defaultSessionExpires = 1800 * time.Second

	// defaultMinSE is the minimum acceptable session interval, the RFC 4028 §4
	// default of 90 seconds. An offered Session-Expires below this is reported
	// as below-floor so the answer path can reject with 422 and a Min-SE header.
	defaultMinSE = 90 * time.Second

	// defaultWatchdogGrace is the tolerance added to the negotiated interval
	// before the peer-refresh watchdog hangs up on a missed refresh. Used when
	// SessionTimerPolicy.WatchdogGrace is zero.
	defaultWatchdogGrace = 5 * time.Second

	// minRefreshInterval clamps the refresh cadence so a tiny offered
	// Session-Expires can never drive a sub-floor re-INVITE storm.
	minRefreshInterval = defaultMinSE / 2
)

// Refresher tokens as carried in the RFC 4028 refresher parameter.
const (
	refresherUAS = "uas"
	refresherUAC = "uac"
)

// SessionTimerPolicy is the RFC 4028 session-timer policy. It is stamped onto
// each dialog at construction via WithSessionTimers and drives negotiation, the
// refresh loop and the peer-refresh watchdog. The zero value is disabled and is
// safe to use.
type SessionTimerPolicy struct {
	// Enabled turns session-timer advertisement and negotiation on.
	Enabled bool

	// DefaultSE is the advertise-fallback session interval, used only when we
	// advertise to a timer-supporting peer that offered no Session-Expires. Zero
	// falls back to defaultSessionExpires. It is never imposed on a peer's own
	// offer.
	DefaultSE time.Duration

	// MinSE is our floor for an acceptable session interval. Zero falls back to
	// defaultMinSE. The effective floor is max(MinSE, the peer's offered Min-SE).
	MinSE time.Duration

	// PreferUASRefresher is a tiebreaker applied only when the peer omits a
	// refresher: true elects "uas", false stays neutral. When the peer offers a
	// refresher it is always honored and this never overrides it.
	PreferUASRefresher bool

	// WatchdogGrace is the tolerance added to the negotiated interval before the
	// peer-refresh watchdog hangs up on a missed refresh. Zero falls back to
	// defaultWatchdogGrace.
	WatchdogGrace time.Duration
}

// sessionTimerDecision is the outcome of negotiate.
type sessionTimerDecision struct {
	// Negotiated is the agreed session interval. It is also the Min-SE value to
	// echo when rejecting a below-floor offer.
	Negotiated time.Duration

	// Interval is the refresh cadence: half the negotiated interval, clamped to
	// minRefreshInterval.
	Interval time.Duration

	// Refresher is the elected refresher token: "uas", "uac", or "" for neutral.
	Refresher string

	// BelowFloor is true when an offered Session-Expires was strictly below the
	// floor, signalling the answer path to reply 422.
	BelowFloor bool

	// WeRefresh is true when the elected refresher is our role: the caller runs
	// the refresh loop, otherwise it arms the peer-refresh watchdog.
	WeRefresh bool
}

// watchdogGrace resolves the peer-refresh watchdog grace from policy, falling
// back to defaultWatchdogGrace when the field is zero. It is the single source
// for the grace so call sites never pass a bare literal.
func watchdogGrace(policy SessionTimerPolicy) time.Duration {
	if policy.WatchdogGrace > 0 {
		return policy.WatchdogGrace
	}
	return defaultWatchdogGrace
}

// negotiate is a pure, role-agnostic RFC 4028 session-timer negotiation. Given a
// peer's offered Session-Expires, Min-SE and refresher plus our policy it floors
// the interval by Min-SE, falls back to the advertise-fallback when nothing is
// offered, halves for the refresh cadence, honors the peer's refresher (using
// PreferUASRefresher only as a tiebreaker when the peer omits one) and flags a
// below-floor offer for a 422 reply. ourRole only decides WeRefresh, so the same
// function serves the UAS answer path and a future UAC client path unchanged.
//
// All inputs are bounded uint32 deltas, so the duration arithmetic cannot
// overflow.
func negotiate(offeredSE *uint32, offeredMinSE *uint32, offeredRefresher string, policy SessionTimerPolicy, ourRole string) sessionTimerDecision {
	// Effective floor: our Min-SE, raised by the peer's when it offered a higher
	// one. Per RFC 4028 the floor is the max of both parties' minima, so the peer
	// can raise the floor but never lower ours.
	floor := policy.MinSE
	if floor <= 0 {
		floor = defaultMinSE
	}
	if offeredMinSE != nil {
		if peerMin := time.Duration(*offeredMinSE) * time.Second; peerMin > floor {
			floor = peerMin
		}
	}

	// Candidate interval: the peer's offer, or our advertise-fallback when the
	// peer is timer-supporting but offered no Session-Expires.
	var (
		candidate  time.Duration
		belowFloor bool
	)
	if offeredSE != nil {
		candidate = time.Duration(*offeredSE) * time.Second
		// A strictly below-floor offer is flagged for a 422. The negotiated value
		// still reports the floor, which is the Min-SE the answer path echoes.
		belowFloor = candidate < floor
	} else {
		candidate = policy.DefaultSE
		if candidate <= 0 {
			candidate = defaultSessionExpires
		}
	}

	// Clamp up to the floor so a below-floor offer never drives a sub-floor
	// cadence.
	negotiated := candidate
	if negotiated < floor {
		negotiated = floor
	}

	// Refresh at half the negotiated interval, clamped so no offer can storm the
	// signaling path.
	interval := negotiated / 2
	if interval < minRefreshInterval {
		interval = minRefreshInterval
	}

	// Honor the peer's offered refresher. Only tiebreak with PreferUASRefresher
	// when the peer omitted one; false stays neutral and never forces "uas".
	refresher := offeredRefresher
	if refresher == "" && policy.PreferUASRefresher {
		refresher = refresherUAS
	}

	return sessionTimerDecision{
		Negotiated: negotiated,
		Interval:   interval,
		Refresher:  refresher,
		BelowFloor: belowFloor,
		WeRefresh:  refresher != "" && refresher == ourRole,
	}
}

// sessionTimerOffer is a peer's parsed RFC 4028 offer.
type sessionTimerOffer struct {
	SE        *uint32
	MinSE     *uint32
	Refresher string
}

// parseSessionTimerOffer reads Session-Expires and Min-SE off a request. sipgo
// carries no typed accessors for these headers, so they are read generically and
// parsed here. A malformed or out-of-range value is treated as absent rather
// than as an error: RFC 4028 §7.4 has the receiver ignore what it cannot parse,
// and a timer header is never worth failing an otherwise valid INVITE over.
// ok is false when the peer sent no Session-Expires at all (timer-unaware).
func parseSessionTimerOffer(req *sip.Request) (offer sessionTimerOffer, ok bool) {
	h := req.GetHeader("Session-Expires")
	if h == nil {
		// Compact form of Session-Expires per RFC 4028 §7.1.
		h = req.GetHeader("x")
	}
	if h == nil {
		return sessionTimerOffer{}, false
	}

	value, params, _ := strings.Cut(h.Value(), ";")
	se, err := parseDeltaSeconds(value)
	if err != nil {
		return sessionTimerOffer{}, false
	}
	offer.SE = &se

	for _, p := range strings.Split(params, ";") {
		k, v, found := strings.Cut(p, "=")
		if !found {
			continue
		}
		if strings.EqualFold(strings.TrimSpace(k), "refresher") {
			offer.Refresher = strings.ToLower(strings.TrimSpace(v))
		}
	}

	if m := req.GetHeader("Min-SE"); m != nil {
		// Min-SE may itself carry generic params; only the delta matters here.
		mv, _, _ := strings.Cut(m.Value(), ";")
		if minSE, err := parseDeltaSeconds(mv); err == nil {
			offer.MinSE = &minSE
		}
	}

	return offer, true
}

// parseDeltaSeconds parses an RFC 3261 delta-seconds value, the on-the-wire form
// of Session-Expires and Min-SE. It rejects anything that does not fit the
// unsigned 32-bit range the negotiation arithmetic assumes.
func parseDeltaSeconds(s string) (uint32, error) {
	v, err := strconv.ParseUint(strings.TrimSpace(s), 10, 32)
	if err != nil {
		return 0, err
	}
	return uint32(v), nil
}

// secondsOf renders a session-timer duration as integer delta-seconds, the
// on-the-wire form for Session-Expires and Min-SE header values.
func secondsOf(d time.Duration) string {
	return strconv.FormatInt(int64(d/time.Second), 10)
}

// sessionRefreshLoop drives the RFC 4028 in-dialog refresh when we are the
// elected refresher. It ticks every interval and runs refresh. Unlike a liveness
// probe, which tolerates blips and counts to a threshold, a failed session
// refresh means the dialog is at risk, so it escalates on the first refresh
// error. Cancellation of ctx (dialog Close or Bye) is surfaced as the context
// error and takes precedence over a refresh error observed during shutdown, so
// the goroutine always exits cleanly with the dialog.
func sessionRefreshLoop(ctx context.Context, interval time.Duration, refresh func(context.Context) error) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}

		if err := refresh(ctx); err != nil {
			if ctx.Err() != nil {
				// Cancellation during the refresh is a clean teardown, never a
				// peer-failure signal, so surface the context error.
				return ctx.Err()
			}
			return err
		}
	}
}

// peerRefreshWatchdog bounds the case where the peer is the elected refresher
// and stops refreshing. It fires sendBye exactly once if no inbound refresh
// resets it within negotiatedSE plus grace, and is cancellable via ctx (dialog
// Close or Bye) or Stop. The caller supplies the hangup action and resets it on
// an inbound refresh re-INVITE.
type peerRefreshWatchdog struct {
	resetCh chan struct{}
	stopCh  chan struct{}
	done    chan struct{}
	stopOne sync.Once
	fired   atomic.Bool
}

// armPeerRefreshWatchdog starts a peer-refresh watchdog that hangs up the dialog
// via sendBye if no Reset arrives within negotiatedSE plus grace. Resolve grace
// from policy via watchdogGrace rather than passing a literal. The watchdog exits
// on ctx cancel, on Stop, or after it fires.
func armPeerRefreshWatchdog(ctx context.Context, negotiatedSE, grace time.Duration, sendBye func(context.Context) error) *peerRefreshWatchdog {
	w := &peerRefreshWatchdog{
		resetCh: make(chan struct{}, 1),
		stopCh:  make(chan struct{}),
		done:    make(chan struct{}),
	}
	go w.run(ctx, negotiatedSE+grace, sendBye)
	return w
}

// run is the watchdog control loop. It rearms its expiry timer on every Reset and
// fires sendBye at most once when the timer elapses with no reset.
func (w *peerRefreshWatchdog) run(ctx context.Context, expiry time.Duration, sendBye func(context.Context) error) {
	defer close(w.done)

	timer := time.NewTimer(expiry)
	defer timer.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-w.stopCh:
			return
		case <-w.resetCh:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			timer.Reset(expiry)
		case <-timer.C:
			if w.fired.CompareAndSwap(false, true) {
				// Best effort: the dialog is being torn down regardless, and a BYE
				// error on a dying dialog is not actionable here.
				_ = sendBye(ctx)
			}
			return
		}
	}
}

// Reset rearms the watchdog to its full negotiatedSE plus grace window. It is
// called on an inbound refresh re-INVITE and is safe to call from any goroutine.
// A reset that races a pending one is coalesced, since the window is rearmed
// either way.
func (w *peerRefreshWatchdog) Reset() {
	select {
	case w.resetCh <- struct{}{}:
	default:
	}
}

// Stop tears the watchdog down without firing a BYE. It is idempotent and safe
// to call alongside ctx cancellation.
func (w *peerRefreshWatchdog) Stop() {
	w.stopOne.Do(func() { close(w.stopCh) })
}
