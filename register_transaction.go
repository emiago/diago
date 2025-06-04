// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
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

type RegisterTransaction struct {
	opts   RegisterOptions
	Origin *sip.Request

	client *sipgo.Client
	log    *slog.Logger

	expiry time.Duration
}

func newRegisterTransaction(client *sipgo.Client, recipient sip.Uri, contact sip.ContactHeader, opts RegisterOptions) *RegisterTransaction {
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
		log:    slog.Default().With("caller", "Register"),
	}

	return t
}

func (t *RegisterTransaction) Register(ctx context.Context) error {
	username, password, expiry := t.opts.Username, t.opts.Password, t.opts.Expiry
	client := t.client
	log := t.log
	req := t.Origin
	contact := *req.Contact().Clone()

	// Send request and parse response
	// req.SetDestination(*dst)
	log.Info("REGISTER", "uri", req.Recipient.String(), "expiry", int(expiry))
	tx, err := client.TransactionRequest(ctx, req, sipgo.ClientRequestRegisterBuild)
	if err != nil {
		return fmt.Errorf("fail to create transaction req=%q: %w", req.StartLine(), err)
	}
	defer tx.Terminate()

	res, err := getResponse(ctx, tx)
	if err != nil {
		return fmt.Errorf("fail to get response req=%q : %w", req.StartLine(), err)
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

	log.Info("Received status", "status", int(res.StatusCode))
	if res.StatusCode == sip.StatusUnauthorized || res.StatusCode == sip.StatusProxyAuthRequired {
		tx.Terminate() //Terminate previous

		log.Info("Unathorized. Doing digest auth")
		res, err = client.DoDigestAuth(ctx, req, res, sipgo.DigestAuth{
			Username: username,
			Password: password,
		})
		if err != nil {
			return fmt.Errorf("fail to get response req=%q : %w", req.StartLine(), err)
		}
		log.Info("Received status", "status", int(res.StatusCode))
	}

	if res.StatusCode != 200 {
		return &RegisterResponseError{
			RegisterReq: req,
			RegisterRes: res,
			Msg:         res.StartLine(),
		}
	}

	// Now update server expiry
	t.expiry = t.opts.Expiry
	if h := res.GetHeader("Expires"); h != nil {
		val, err := strconv.Atoi(h.Value())
		if err != nil {
			return fmt.Errorf("Failed to parse server Expires value: %w", err)
		}
		t.expiry = time.Duration(val) * time.Second
	}

	return nil
}

func (t *RegisterTransaction) QualifyLoop(ctx context.Context) error {
	// TODO: based on server response Expires header this must be adjusted
	// Allows caller to adjust

	calcRetry := func(expiry time.Duration) time.Duration {
		// Allow caller to use own interval
		if t.opts.RetryInterval != 0 {
			return t.opts.RetryInterval
		}

		calc := expiry.Seconds() * 0.75
		retry := time.Duration(calc) * time.Second

		// Set to 30 in case retry is not set
		if retry == 0 {
			retry = 30 * time.Second
		}

		return retry
	}

	expiry := t.expiry
	retry := calcRetry(expiry)

	ticker := time.NewTicker(retry)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C: // TODO make configurable
		}
		err := t.Qualify(ctx)
		if err != nil {
			return err
		}

		if t.expiry != expiry {
			// expiry got updated
			expiry = t.expiry
			retry = calcRetry(expiry)

			t.log.Info("Register expiry changed", "expiry_old", expiry, "expiry_new", t.expiry, "retry", retry)
			ticker.Reset(retry)
		}

	}
}

func (t *RegisterTransaction) Unregister(ctx context.Context) error {
	req := t.Origin

	req.RemoveHeader("Expires")
	req.RemoveHeader("Contact")
	req.AppendHeader(sip.NewHeader("Contact", "*"))
	expires := sip.ExpiresHeader(0)
	req.AppendHeader(&expires)
	return t.doRequest(ctx, req)
}

func (t *RegisterTransaction) Qualify(ctx context.Context) error {
	return t.doRequest(ctx, t.Origin)
}

func (t *RegisterTransaction) doRequest(ctx context.Context, req *sip.Request) error {
	// log := p.getLoggerCtx(ctx, "Register")
	log := t.log
	client := t.client
	username, password := t.opts.Username, t.opts.Password
	// Send request and parse response
	// req.SetDestination(*dst)
	req.RemoveHeader("Via")
	res, err := client.Do(ctx, req, sipgo.ClientRequestRegisterBuild)
	if err != nil {
		return fmt.Errorf("fail to get response req=%q : %w", req.StartLine(), err)
	}

	log.Info("Received status", "uri", req.Recipient.String())
	if res.StatusCode == sip.StatusUnauthorized || res.StatusCode == sip.StatusProxyAuthRequired {
		log.Info("Unathorized. Doing digest auth")
		res, err = client.DoDigestAuth(ctx, req, res, sipgo.DigestAuth{
			Username: username,
			Password: password,
		})
		if err != nil {
			return fmt.Errorf("fail to get response req=%q : %w", req.StartLine(), err)
		}
		log.Info("Received status", "uri", req.Recipient.String())
	}

	if res.StatusCode != 200 {
		return &RegisterResponseError{
			RegisterReq: req,
			RegisterRes: res,
			Msg:         res.StartLine(),
		}
	}

	// Check is expirese changed
	if h := res.GetHeader("Expires"); h != nil {
		val, err := strconv.Atoi(h.Value())
		if err != nil {
			return fmt.Errorf("Failed to parse server Expires value: %w", err)
		}
		t.expiry = time.Duration(val) * time.Second
	}

	return nil
}

func getResponse(ctx context.Context, tx sip.ClientTransaction) (*sip.Response, error) {
	for {
		select {
		case <-tx.Done():
			return nil, fmt.Errorf("transaction died before final reply")
		case res := <-tx.Responses():
			// If this is a provisional (1xx), just loop again
			if code := res.StatusCode; code >= 100 && code < 200 {
				// quietly ignore the 1xx
				continue
			}
			// Otherwise this is the first non-1xx (200, 401, 5xx, etc.)
			return res, nil
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
}
