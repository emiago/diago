// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

type RegisterResponseError struct {
	RegisterReq *sip.Request
	RegisterRes *sip.Response

	Msg string
}

func (e *RegisterResponseError) StatusCode() sip.StatusCode {
	return e.RegisterRes.StatusCode
}

func (e RegisterResponseError) Error() string {
	return e.Msg
}

type RegisterTransaction struct {
	opts   RegisterOptions
	Origin *sip.Request

	client *sipgo.Client
	log    zerolog.Logger

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
		log:    log.With().Str("caller", "Register").Logger(),
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
	log.Info().Str("uri", req.Recipient.String()).Int("expiry", int(expiry)).Msg("REGISTER")
	tx, err := client.TransactionRequest(ctx, req)
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

	log.Info().Int("status", int(res.StatusCode)).Msg("Received status")
	if res.StatusCode == sip.StatusUnauthorized || res.StatusCode == sip.StatusProxyAuthRequired {
		tx.Terminate() //Terminate previous

		log.Info().Msg("Unathorized. Doing digest auth")
		tx, err = client.DoDigestAuth(ctx, req, res, sipgo.DigestAuth{
			Username: username,
			Password: password,
		})
		if err != nil {
			return err
		}
		defer tx.Terminate()

		res, err = getResponse(ctx, tx)
		if err != nil {
			return fmt.Errorf("fail to get response req=%q : %w", req.StartLine(), err)
		}
		log.Info().Int("status", int(res.StatusCode)).Msg("Received status")
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
		err := t.qualify(ctx)
		if err != nil {
			return err
		}

		if t.expiry != expiry {
			// expiry got updated
			expiry = t.expiry
			retry = calcRetry(expiry)

			t.log.Info().Dur("expiry_old", expiry).Dur("expiry_new", t.expiry).Dur("retry", retry).Msg("Register expiry changed")
			ticker.Reset(retry)
		}

	}
}

func (t *RegisterTransaction) Unregister(ctx context.Context) error {
	log := t.log
	req := t.Origin

	req.RemoveHeader("Expires")
	req.RemoveHeader("Contact")
	req.AppendHeader(sip.NewHeader("Contact", "*"))
	expires := sip.ExpiresHeader(0)
	req.AppendHeader(&expires)

	log.Info().Str("uri", req.Recipient.String()).Msg("UNREGISTER")
	return t.doRequest(ctx, req)
}

func (t *RegisterTransaction) qualify(ctx context.Context) error {
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
	tx, err := client.TransactionRequest(ctx, req)
	if err != nil {
		return fmt.Errorf("fail to create transaction req=%q: %w", req.StartLine(), err)
	}
	defer tx.Terminate()

	res, err := getResponse(ctx, tx)
	if err != nil {
		return fmt.Errorf("fail to get response req=%q : %w", req.StartLine(), err)
	}

	log.Info().Int("status", int(res.StatusCode)).Msg("Received status")
	if res.StatusCode == sip.StatusUnauthorized || res.StatusCode == sip.StatusProxyAuthRequired {
		tx.Terminate() //Terminate previous
		log.Info().Msg("Unathorized. Doing digest auth")
		tx, err = client.DoDigestAuth(ctx, req, res, sipgo.DigestAuth{
			Username: username,
			Password: password,
		})
		if err != nil {
			return err
		}
		defer tx.Terminate()

		res, err = getResponse(ctx, tx)
		if err != nil {
			return fmt.Errorf("fail to get response req=%q : %w", req.StartLine(), err)
		}
		log.Info().Int("status", int(res.StatusCode)).Msg("Received status")
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
	select {
	case <-tx.Done():
		return nil, fmt.Errorf("transaction died")
	case res := <-tx.Responses():
		return res, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}
