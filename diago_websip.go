// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2026, Emir Aganovic

package diago

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sync"

	"github.com/emiago/diago/media"
	"github.com/emiago/sipgo/sip"
	websip "github.com/emiago/websip"
)

// ServeDialogWebsipFunc owns an incoming Websip dialog for the duration of the
// callback. Returning rejects an unanswered call or hangs up an answered call.
type ServeDialogWebsipFunc func(*DialogWebsipServerSession)

// DiagoWebsip is the Websip signaling counterpart of Diago. It intentionally
// exposes only the direct DialogWebrtc media stack.
type DiagoWebsip struct {
	ua     *websip.UA
	client *websip.Client
	server *websip.Server

	mediaConf MediaConfig

	serveMu      sync.RWMutex
	serveHandler ServeDialogWebsipFunc

	clientDialogs sync.Map // Call-ID -> *DialogWebsipClientSession
	serverDialogs sync.Map // Call-ID -> *DialogWebsipServerSession
}

type DiagoWebsipOption func(*DiagoWebsip)

// WithWebsipClient provides the client role handle. It must use the same UA
// passed to NewDiagoWebsip.
func WithWebsipClient(client *websip.Client) DiagoWebsipOption {
	return func(d *DiagoWebsip) { d.client = client }
}

// WithWebsipServer provides the server role handle. It must use the same UA
// passed to NewDiagoWebsip.
func WithWebsipServer(server *websip.Server) DiagoWebsipOption {
	return func(d *DiagoWebsip) { d.server = server }
}

// WithWebsipMediaConfig configures media for new Websip dialogs. Websip only
// uses the codec portion of MediaConfig because its media is always WebRTC.
func WithWebsipMediaConfig(conf MediaConfig) DiagoWebsipOption {
	return func(d *DiagoWebsip) { d.mediaConf = cloneWebsipMediaConfig(conf) }
}

func NewDiagoWebsip(ua *websip.UA, options ...DiagoWebsipOption) (*DiagoWebsip, error) {
	if ua == nil {
		return nil, fmt.Errorf("websip UA is nil")
	}
	d := &DiagoWebsip{
		ua: ua,
		mediaConf: MediaConfig{
			Codecs: []media.Codec{
				media.CodecAudioUlaw,
				media.CodecAudioAlaw,
				media.CodecTelephoneEvent8000,
			},
		},
		serveHandler: func(*DialogWebsipServerSession) {},
	}
	for _, option := range options {
		if option != nil {
			option(d)
		}
	}
	var err error
	if d.client == nil {
		d.client, err = websip.NewClient(ua)
		if err != nil {
			return nil, fmt.Errorf("create Websip client: %w", err)
		}
	} else if d.client.UA != ua {
		return nil, fmt.Errorf("Websip client uses a different UA")
	}
	if d.server == nil {
		d.server, err = websip.NewServer(ua)
		if err != nil {
			return nil, fmt.Errorf("create Websip server: %w", err)
		}
	} else if d.server.UA != ua {
		return nil, fmt.Errorf("Websip server uses a different UA")
	}
	d.server.OnDialog(d.handleDialog)
	return d, nil
}

func (d *DiagoWebsip) WebsipClient() *websip.Client { return d.client }
func (d *DiagoWebsip) WebsipServer() *websip.Server { return d.server }

func (d *DiagoWebsip) OnDialog(handler ServeDialogWebsipFunc) {
	if handler == nil {
		handler = func(*DialogWebsipServerSession) {}
	}
	d.serveMu.Lock()
	d.serveHandler = handler
	d.serveMu.Unlock()
}

// ServeHTTP upgrades and serves one Websip WebSocket connection. HTTP server,
// TLS, Origin validation, timeouts and graceful shutdown remain application
// responsibilities.
func (d *DiagoWebsip) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	d.server.ServeHTTP(w, r)
}

func (d *DiagoWebsip) NewDialog(recipient sip.Uri) (*DialogWebsipClientSession, error) {
	dialog, err := d.client.NewDialog(recipient)
	if err != nil {
		return nil, err
	}
	wrapped := newDialogWebsipClientSession(dialog, cloneWebsipMediaConfig(d.mediaConf))
	d.clientDialogs.Store(dialog.ID(), wrapped)
	dialog.OnState(func(state websip.DialogState) {
		if state != websip.DialogTerminated {
			return
		}
		_ = wrapped.closeMedia()
		d.clientDialogs.Delete(dialog.ID())
	})
	dialog.OnRequest(wrapped.handleRequest)
	return wrapped, nil
}

// Invite creates, sends and establishes a Websip WebRTC dialog.
func (d *DiagoWebsip) Invite(ctx context.Context, recipient sip.Uri, options InviteWebsipOptions) (*DialogWebsipClientSession, *DialogWebrtc, error) {
	dialog, err := d.NewDialog(recipient)
	if err != nil {
		return nil, nil, err
	}
	med, err := dialog.Invite(ctx, options)
	if err != nil {
		return nil, nil, errors.Join(err, dialog.Close())
	}
	return dialog, med, nil
}

func (d *DiagoWebsip) handleDialog(dialog *websip.DialogServer) {
	wrapped := newDialogWebsipServerSession(dialog, cloneWebsipMediaConfig(d.mediaConf))
	d.serverDialogs.Store(dialog.ID(), wrapped)
	dialog.OnState(func(state websip.DialogState) {
		if state != websip.DialogTerminated {
			return
		}
		_ = wrapped.closeMedia()
		d.serverDialogs.Delete(dialog.ID())
	})
	dialog.OnRequest(wrapped.handleRequest)

	d.serveMu.RLock()
	handler := d.serveHandler
	d.serveMu.RUnlock()
	handler(wrapped)
}

func cloneWebsipMediaConfig(conf MediaConfig) MediaConfig {
	conf.Codecs = slices.Clone(conf.Codecs)
	return conf
}

// Close releases all local dialogs and closes the shared Websip UA.
func (d *DiagoWebsip) Close() error {
	var closeErr error
	d.clientDialogs.Range(func(_, value any) bool {
		closeErr = errors.Join(closeErr, value.(*DialogWebsipClientSession).Close())
		return true
	})
	d.serverDialogs.Range(func(_, value any) bool {
		closeErr = errors.Join(closeErr, value.(*DialogWebsipServerSession).Close())
		return true
	})
	return errors.Join(closeErr, d.ua.Close())
}
