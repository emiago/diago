// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2026, Emir Aganovic

package diago

import (
	"errors"
	"fmt"
	"mime"
	"strings"
	"sync"

	"github.com/emiago/sipgo/sip"
	websip "github.com/emiago/websip"
)

type dialogWebsipMedia struct {
	mu     sync.Mutex
	media  *DialogWebrtc
	closed bool
}

func (d *dialogWebsipMedia) attach(media *DialogWebrtc) error {
	if media == nil {
		return fmt.Errorf("dialog WebRTC media is nil")
	}
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return errors.Join(websip.ErrClosed, media.Close())
	}
	if d.media != nil {
		d.mu.Unlock()
		return errors.Join(fmt.Errorf("dialog WebRTC media is already initialized"), media.Close())
	}
	d.media = media
	d.mu.Unlock()
	return nil
}

func (d *dialogWebsipMedia) closeMedia() error {
	d.mu.Lock()
	if d.closed {
		d.mu.Unlock()
		return nil
	}
	d.closed = true
	media := d.media
	d.media = nil
	d.mu.Unlock()
	if media == nil {
		return nil
	}
	return media.Close()
}

func isSDPMessage(contentType *sip.ContentTypeHeader, body []byte) bool {
	if contentType == nil || len(body) == 0 {
		return false
	}
	mediaType, _, err := mime.ParseMediaType(contentType.Value())
	return err == nil && strings.EqualFold(mediaType, "application/sdp")
}
