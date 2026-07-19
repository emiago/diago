// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

// registerRefreshRatio is the fraction of the granted expiry at which the
// binding is refreshed. Refreshing at 3/4 of the grant leaves a quarter of the
// lifetime for a refresh to fail and be retried before the binding lapses.
const registerRefreshRatio = 0.75

// registerRetryMin is the shortest refresh interval, so an absent or nonsense
// grant cannot busy loop the REGISTER path.
const registerRetryMin = 30 * time.Second

const (
	// registerRetryBackoffInitial is the wait after the first failed refresh.
	// Short, because most refresh failures are a blip and the binding is still
	// good for the remaining quarter of its grant.
	registerRetryBackoffInitial = 1 * time.Second
	// registerRetryBackoffMax caps the doubling so a long registrar outage
	// settles into a steady probe rather than an ever widening one.
	registerRetryBackoffMax = 5 * time.Minute
)

type RegisterResponseError struct {
	RegisterReq *sip.Request
	RegisterRes *sip.Response

	Msg string
}

func (e *RegisterResponseError) StatusCode() int {
	return e.RegisterRes.StatusCode
}

func (e RegisterResponseError) Error() string {
	return e.Msg
}

type RegisterOptions struct {
	// Digest auth
	Username  string
	Password  string
	ProxyHost string

	// Expiry is for Expire header
	Expiry time.Duration
	// RetryInterval is the interval before the next Register is sent. It can only
	// bring the refresh forward: an interval longer than what the registrar
	// granted is clamped to the grant, otherwise the binding lapses before the
	// refresh fires.
	RetryInterval time.Duration
	AllowHeaders  []string

	OnRegistered func()

	// Useragent default will be used on what is provided as NewUA()
	// UserAgent         string
	// UserAgentHostname string
}

type RegisterTransaction struct {
	opts   RegisterOptions
	Origin *sip.Request

	client *sipgo.Client
	log    *slog.Logger

	// mu guards the registration state below. Diago.Register writes expiry from
	// its own goroutine while the refresh loop reads it.
	mu sync.RWMutex
	// expiry drives the refresh cadence, see calcRetry.
	expiry time.Duration
	// grantedExpires and lastRegistered are written only by a successful
	// REGISTER and describe the binding the registrar actually holds. They are
	// deliberately kept apart from expiry, which Diago.Register repurposes as a
	// Retry-After hint on a 503; mixing the two would make an otherwise live
	// binding look already lapsed.
	grantedExpires time.Duration
	lastRegistered time.Time
	// expiredEmitted latches the escalation so a lapsed binding is reported once
	// per outage rather than on every subsequent refresh.
	expiredEmitted bool
}

// recordGrant stores the expiry the registrar granted on a successful REGISTER
// and rearms the expired latch, since the binding is demonstrably alive.
func (t *RegisterTransaction) recordGrant(granted time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.expiry = granted
	t.grantedExpires = granted
	t.lastRegistered = time.Now()
	t.expiredEmitted = false
}

// setExpiry overrides only the refresh cadence, leaving the recorded grant intact.
func (t *RegisterTransaction) setExpiry(d time.Duration) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.expiry = d
}

// getExpiry reads the current refresh cadence input.
func (t *RegisterTransaction) getExpiry() time.Duration {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.expiry
}

func newRegisterTransaction(client *sipgo.Client, recipient sip.Uri, contact sip.ContactHeader, log *slog.Logger, opts RegisterOptions) *RegisterTransaction {
	expiry, allowHDRS := opts.Expiry, opts.AllowHeaders
	// log := p.getLoggerCtx(ctx, "Register")
	req := sip.NewRequest(sip.REGISTER, recipient)
	req.AppendHeader(&contact)

	if opts.ProxyHost != "" {
		req.SetDestination(opts.ProxyHost)
	}
	if expiry > 0 {
		expires := sip.ExpiresHeader(expiry.Seconds())
		req.AppendHeader(&expires)
	}
	if allowHDRS != nil {
		req.AppendHeader(sip.NewHeader("Allow", strings.Join(allowHDRS, ", ")))
	}

	// if opts.Username == "" {
	// 	opts.Username = opts.UserAgent
	// }

	if opts.Username == "" {
		opts.Username = client.Name()
	}

	t := &RegisterTransaction{
		Origin: req, // origin maybe updated after first register
		opts:   opts,
		client: client,
		log:    log.With("caller", "Register"),
	}

	return t
}

func (t *RegisterTransaction) Register(ctx context.Context) error {
	if err := t.register(ctx); err != nil {
		return err
	}

	if t.opts.OnRegistered != nil {
		t.opts.OnRegistered()
	}
	return nil
}
func (t *RegisterTransaction) register(ctx context.Context) error {
	username, password := t.opts.Username, t.opts.Password
	client := t.client
	req := t.Origin
	contact := *req.Contact().Clone()

	res, err := client.Do(ctx, req, sipgo.ClientRequestRegisterBuild)
	if err != nil {
		return fmt.Errorf("fail to create transaction req=%q: %w", req.StartLine(), err)
	}

	via := res.Via()
	if via == nil {
		return fmt.Errorf("no Via header in response")
	}

	// https://datatracker.ietf.org/doc/html/rfc3581#section-9
	if rport, _ := via.Params.Get("rport"); rport != "" {
		if p, err := strconv.Atoi(rport); err == nil {
			contact.Address.Port = p
		}

		if received, _ := via.Params.Get("received"); received != "" {
			// TODO: consider parsing IP
			contact.Address.Host = received
		}

		// Update contact address of NAT
		req.ReplaceHeader(&contact)
	}

	if isDigestChallenge(res) {
		res, err = client.DoDigestAuth(ctx, req, res, sipgo.DigestAuth{
			Username: username,
			Password: password,
		})
		if err != nil {
			return fmt.Errorf("fail to get response req=%q : %w", req.StartLine(), err)
		}
	}

	if res.StatusCode != 200 {
		return &RegisterResponseError{
			RegisterReq: req,
			RegisterRes: res,
			Msg:         res.StartLine(),
		}
	}

	// Record what the registrar granted, which may be far shorter than what was
	// requested. That grant, not the request, bounds the binding's lifetime.
	t.recordGrant(grantedExpiry(res, t.opts.Expiry))

	return nil
}

// isDigestChallenge reports whether a 401/407 is an actual challenge, meaning it
// carries the matching WWW-Authenticate / Proxy-Authenticate header, rather than
// a plain rejection.
//
// A registrar is free to refuse an account with a challenge-less 401. Running
// digest against an absent challenge makes sipgo return a bare "No
// WWW-Authenticate header present" that replaces the final response, so the
// caller sees neither the status code nor the disposition. Falling through
// instead yields a RegisterResponseError carrying the real response, which is
// what a status code classifier needs.
func isDigestChallenge(res *sip.Response) bool {
	switch res.StatusCode {
	case sip.StatusUnauthorized:
		return res.GetHeader("WWW-Authenticate") != nil
	case sip.StatusProxyAuthRequired:
		return res.GetHeader("Proxy-Authenticate") != nil
	default:
		return false
	}
}

// grantedExpiry resolves a 200 OK's granted lifetime, defaulting to requested.
//
// RFC 3261 section 10.3 lets the registrar answer with either a per-Contact
// expires param or a top level Expires header. The per-Contact param is the
// binding's own lifetime, so it wins. Only a positive grant counts: expires=0 is
// a de-registration echo, not a lifetime.
func grantedExpiry(res *sip.Response, requested time.Duration) time.Duration {
	if c := res.Contact(); c != nil {
		if v, ok := c.Params.Get("expires"); ok {
			if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil && n > 0 {
				return time.Duration(n) * time.Second
			}
		}
	}

	if h := res.GetHeader("Expires"); h != nil {
		if n, err := strconv.Atoi(strings.TrimSpace(h.Value())); err == nil && n > 0 {
			return time.Duration(n) * time.Second
		}
	}

	return requested
}

func (t *RegisterTransaction) QualifyLoop(ctx context.Context) error {
	return t.reregisterLoop(ctx, t.calcRetry(t.getExpiry()))
}

// registerRefresher is the half of the refresh loop that talks to the network,
// split out so the tolerance contract below is testable without a registrar.
// RegisterTransaction is the only production implementation.
type registerRefresher interface {
	// refresh sends one REGISTER refresh.
	refresh(ctx context.Context) error
	// cadence reports the wait after a successful refresh, derived from the
	// expiry the registrar granted.
	cadence() time.Duration
	// expired reports whether the binding is provably gone, meaning the last
	// confirmed registration's granted lifetime has run out. It latches, so a
	// lapsed binding escalates once per outage.
	expired() bool
}

func (t *RegisterTransaction) reregisterLoop(ctx context.Context, retry time.Duration) error {
	return registerRefreshLoop(ctx, retry, registerRetryBackoffInitial, registerRetryBackoffMax, t)
}

// registerRefreshLoop keeps a binding alive, tolerating transient refresh
// failures instead of surrendering the whole registration to the first one.
//
// It used to return on the first failed Qualify, so one 503 or one network blip
// tore the registration down even though the binding the registrar holds is
// still valid for the rest of its granted lifetime and the refresh would have
// succeeded seconds later. The failure arm now backs off exponentially and
// retries. Escalation is reserved for the case where retrying is provably
// pointless: expired() reports the granted lifetime has run out, so the
// registrar has certainly dropped us. Cancellation is a clean shutdown and is
// surfaced as the context error, never as a failure.
//
// The backoff bounds are parameters rather than the constants directly so the
// tolerance contract is testable at millisecond scale.
func registerRefreshLoop(ctx context.Context, retry, initialBackoff, maxBackoff time.Duration, r registerRefresher) error {
	timer := time.NewTimer(retry)
	defer timer.Stop()

	backoff := initialBackoff
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-timer.C:
		}

		err := r.refresh(ctx)
		if ctx.Err() != nil {
			return ctx.Err()
		}

		if err != nil {
			if r.expired() {
				// The binding outlived its grant: no amount of further retrying
				// brings it back, so hand the error up.
				return err
			}
			timer.Reset(backoff)
			backoff *= 2
			if backoff > maxBackoff {
				backoff = maxBackoff
			}
			continue
		}

		backoff = initialBackoff
		timer.Reset(r.cadence())
	}
}

// refresh sends one REGISTER refresh, logging a failure that the loop will
// absorb so a transient outage is still visible.
func (t *RegisterTransaction) refresh(ctx context.Context) error {
	expiry := t.getExpiry()
	if err := t.Qualify(ctx); err != nil {
		t.log.Warn("Register refresh failed, will retry", "error", err, "expiry", expiry)
		return err
	}

	if next := t.getExpiry(); next != expiry {
		t.log.Info("Register expiry changed", "expiry_old", expiry, "expiry_new", next, "retry", t.calcRetry(next))
	}
	return nil
}

// cadence reports the post success wait, recomputed each time so a registrar
// that shortens the grant mid registration takes effect on the next refresh.
func (t *RegisterTransaction) cadence() time.Duration {
	return t.calcRetry(t.getExpiry())
}

// expired reports whether the last confirmed registration's granted lifetime has
// run out, latching so one outage escalates once. A transaction that never
// registered successfully has no binding to lose and never reports expired: the
// initial Register's own error already covers that case.
func (t *RegisterTransaction) expired() bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	if t.expiredEmitted || t.lastRegistered.IsZero() || t.grantedExpires <= 0 {
		return false
	}
	if !time.Now().After(t.lastRegistered.Add(t.grantedExpires)) {
		return false
	}
	t.expiredEmitted = true
	return true
}

// calcRetry derives the refresh cadence from the expiry the registrar granted,
// so a short grant is refreshed early rather than on a caller chosen wall clock.
//
// RetryInterval used to short circuit this outright: whenever it was set it was
// returned verbatim and the granted expiry was never consulted, so a caller
// asking for 30m against a registrar granting 120s saw the binding lapse long
// before the refresh fired. It now only brings the refresh forward.
func (t *RegisterTransaction) calcRetry(expiry time.Duration) time.Duration {
	calc := expiry.Seconds() * registerRefreshRatio
	retry := time.Duration(calc) * time.Second

	// Allow caller to use own interval, but never past the grant.
	if ri := t.opts.RetryInterval; ri > 0 && (retry <= 0 || ri < retry) {
		retry = ri
	}

	// A zero or absent expiry must not yield a zero interval, a busy loop.
	if retry <= 0 {
		retry = registerRetryMin
	}

	return retry
}

func (t *RegisterTransaction) Unregister(ctx context.Context) error {
	req := t.Origin

	req.RemoveHeader("Expires")
	req.RemoveHeader("Contact")
	req.AppendHeader(sip.NewHeader("Contact", "*"))
	expires := sip.ExpiresHeader(0)
	req.AppendHeader(&expires)
	// Deliberately records no grant: the 200 to a de-registration echoes
	// expires=0, which is the binding going away, not a new lifetime.
	_, err := t.doRequest(ctx, req)
	return err
}

// Qualify refreshes the binding and records the newly granted expiry.
func (t *RegisterTransaction) Qualify(ctx context.Context) error {
	res, err := t.doRequest(ctx, t.Origin)
	if err != nil {
		return err
	}
	// A refresh regrants the binding, so reread the granted expiry every time. A
	// registrar is free to shorten it mid registration.
	t.recordGrant(grantedExpiry(res, t.opts.Expiry))
	return nil
}

func (t *RegisterTransaction) doRequest(ctx context.Context, req *sip.Request) (*sip.Response, error) {
	// log := p.getLoggerCtx(ctx, "Register")
	client := t.client
	username, password := t.opts.Username, t.opts.Password
	// Send request and parse response
	// req.SetDestination(*dst)
	req.RemoveHeader("Via")
	res, err := client.Do(ctx, req, sipgo.ClientRequestRegisterBuild)
	if err != nil {
		return nil, fmt.Errorf("fail to get response req=%q : %w", req.StartLine(), err)
	}

	if isDigestChallenge(res) {
		res, err = client.DoDigestAuth(ctx, req, res, sipgo.DigestAuth{
			Username: username,
			Password: password,
		})
		if err != nil {
			return nil, fmt.Errorf("fail to get response req=%q : %w", req.StartLine(), err)
		}
	}

	if res.StatusCode != 200 {
		return nil, &RegisterResponseError{
			RegisterReq: req,
			RegisterRes: res,
			Msg:         res.StartLine(),
		}
	}

	return res, nil
}

func getResponse(ctx context.Context, tx sip.ClientTransaction) (*sip.Response, error) {
	select {
	case <-tx.Done():
		return nil, fmt.Errorf("transaction died")
	case res := <-tx.Responses():
		return res, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
