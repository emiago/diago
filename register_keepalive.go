// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"context"
	"fmt"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

const (
	// optionsKeepaliveInterval is how often an out-of-dialog OPTIONS liveness
	// probe is sent to the registrar. It is independent of the REGISTER refresh
	// and bounds peer death detection well below the REGISTER retry interval.
	// 15s matches the common softphone keepalive cadence.
	optionsKeepaliveInterval = 15 * time.Second

	// optionsMaxFailures is the number of consecutive failed probes (transport
	// error or transaction timeout) tolerated before the loop gives up. Any
	// response, even a non 2xx such as 405 or 481, proves the peer is alive and
	// resets the counter, so this only trips on a genuinely unreachable peer,
	// never on a single transient blip.
	optionsMaxFailures = 3
)

// OptionsKeepaliveLoop sends an out-of-dialog OPTIONS to the registrar every 15
// seconds as a liveness probe. It runs next to QualifyLoop, not instead of it:
// QualifyLoop refreshes the registration on the expiry interval, this only
// checks that the peer still answers.
//
// Unlike a transport keepalive, OPTIONS is answered by the SIP stack on the far
// end, so it also catches a peer that is wedged above a socket that is still
// open. On transports with no keepalive of their own it is the only fast idle
// peer death detector.
//
// It returns ctx.Err() when the context is cancelled, or the last probe error
// after optionsMaxFailures consecutive failures.
func (t *RegisterTransaction) OptionsKeepaliveLoop(ctx context.Context) error {
	return t.optionsKeepaliveLoop(ctx, optionsKeepaliveInterval, optionsMaxFailures, t.optionsProbe)
}

// optionsKeepaliveLoop is the control loop, split from the probe so the failure
// counting can be tested without a transport.
func (t *RegisterTransaction) optionsKeepaliveLoop(ctx context.Context, interval time.Duration, maxFailures int, probe func(context.Context) error) error {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	consecutive := 0
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}

		err := probe(ctx)
		if ctx.Err() != nil {
			// Cancelled mid probe. That is a shutdown, not a dead peer.
			return ctx.Err()
		}
		if err != nil {
			consecutive++
			t.log.Warn("OPTIONS keepalive probe failed", "consecutive", consecutive, "max", maxFailures, "error", err)
			if consecutive >= maxFailures {
				return err
			}
			continue
		}
		consecutive = 0
	}
}

// optionsProbe sends one out-of-dialog OPTIONS and reports whether the peer is
// alive. Any final response means alive and returns nil, including a 401 or a
// 405: the point is that the far end answered, so no digest auth is attempted.
// Only a transport error or a transaction timeout returns an error.
//
// The request is built from the recipient captured on construction rather than
// from Origin, which the register loop rewrites in place. ClientRequestBuild
// gives every probe a fresh Call-ID, From tag and CSeq, keeping probes
// independent of each other and of the registration.
func (t *RegisterTransaction) optionsProbe(ctx context.Context) error {
	req := sip.NewRequest(sip.OPTIONS, t.recipient)
	if t.opts.ProxyHost != "" {
		req.SetDestination(t.opts.ProxyHost)
	}

	if _, err := t.client.Do(ctx, req, sipgo.ClientRequestBuild); err != nil {
		return fmt.Errorf("fail to get response req=%q : %w", req.StartLine(), err)
	}
	return nil
}
