package diago

import (
	"context"
	"time"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

type RegisterOptions struct {
	Username string
	Password string

	Expiry        time.Duration
	RetryInterval time.Duration
	AllowHeaders  []string
	UnregisterAll bool
}

func (dg *Diago) Register(ctx context.Context, recipient sip.Uri, opts RegisterOptions) error {
	// Make our client reuse address
	transport := recipient.Headers["transport"]
	if transport == "" {
		transport = "udp"
	}

	contactHDR := dg.getContactHDR(transport)

	client, err := sipgo.NewClient(dg.ua,
		// sipgo.WithClientHostname(contactHDR.Address.Host),
		// sipgo.WithClientPort(lport),
		sipgo.WithClientNAT(), // add rport support
	)
	if err != nil {
		return err
	}

	defer client.Close()

	// TODO use context logging
	log := dg.log.With().Str("caller", "Register").Logger()
	t := NewRegisterTransaction(log, client, recipient, contactHDR, opts)

	if opts.UnregisterAll {
		if err := t.Unregister(ctx); err != nil {
			return ErrRegisterFail
		}
	}

	if err := t.Register(ctx); err != nil {
		return err
	}

	// Unregister
	defer func() {
		ctx, _ := context.WithTimeout(context.Background(), 5*time.Second)
		err := t.Unregister(ctx)
		if err != nil {
			log.Error().Err(err).Msg("Fail to unregister")
		}
	}()

	return t.QualifyLoop(ctx)
}
