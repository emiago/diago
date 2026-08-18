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

type dialogCallbacks struct {
	mu sync.Mutex

	remoteContactTarget *sip.ContactHeader

	mediaHanshaker mediaHanshaker
	onMediaFailure func()
	onReferDialog  OnReferDialogFunc
	onReferNotify  func(statusCode int)
	onClose        []func() error
}

type mediaHanshaker interface {
	onRemoteSDP(ctx context.Context, remoteSDP []byte, offered bool) error
	onLocalSDP(ctx context.Context, answered bool, mode string, mediaSession ...*media.MediaSession) ([]byte, error)
	onFinalize(ctx context.Context) error
}

func (d *dialogCallbacks) onFinalize(ctx context.Context) error {
	d.mu.Lock()
	mediaHanshaker := d.mediaHanshaker
	d.mu.Unlock()
	if mediaHanshaker == nil {
		return nil
	}
	return mediaHanshaker.onFinalize(ctx)
}

func (d *dialogCallbacks) abortMedia() {
	d.mu.Lock()
	onMediaFailure := d.onMediaFailure
	d.mu.Unlock()
	if onMediaFailure != nil {
		onMediaFailure()
	}
}

func (d *dialogCallbacks) mediaHanshakerLocked() mediaHanshaker {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.mediaHanshaker
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
