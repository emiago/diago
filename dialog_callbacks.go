// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"context"
	"errors"
	"sync"

	"github.com/emiago/diago/media"
	"github.com/emiago/sipgo/sip"
)

type dialogRemoteSDP func(ctx context.Context, remoteSDP []byte, offered bool) error
type dialogLocalSDP func(ctx context.Context, answered bool, mode string, mediaSession ...*media.MediaSession) ([]byte, error)

type dialogCallbacks struct {
	mu sync.Mutex

	remoteContactTarget *sip.ContactHeader
	onRemoteSDP         dialogRemoteSDP
	onLocalSDP          dialogLocalSDP
	onFinalize          func(ctx context.Context) error
	onReferDialog       OnReferDialogFunc
	onReferNotify       func(statusCode int)
	onClose             []func() error
}

func (d *dialogCallbacks) setRemoteContact(contact *sip.ContactHeader) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if contact == nil {
		d.remoteContactTarget = nil
		return
	}
	d.remoteContactTarget = contact.Clone()
}

func (d *dialogCallbacks) remoteContact(defaultContact *sip.ContactHeader) *sip.ContactHeader {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.remoteContactTarget != nil {
		return d.remoteContactTarget
	}
	return defaultContact
}

func (d *dialogCallbacks) closeCallbacks() error {
	d.mu.Lock()
	onClose := d.onClose
	d.onClose = nil
	d.mu.Unlock()

	var err error
	for _, f := range onClose {
		err = errors.Join(err, f())
	}
	return err
}
