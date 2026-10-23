// SPDX-License-Identifier: MPL-2.0

package media

import (
	"bytes"
	"sync"
	"time"

	"github.com/pion/rtcp"
	"github.com/pion/rtp"
	"github.com/pion/srtp/v3"
)

const srtpRekeyGracePeriod = 5 * time.Second

// srtpReceiver shares receive-key updates with readers on forked sessions.
type srtpReceiver struct {
	mu            sync.Mutex
	current       *srtp.Context
	previous      *srtp.Context
	previousUntil time.Time
	used          bool
}

func newSRTPReceiver(ctx *srtp.Context) *srtpReceiver {
	return &srtpReceiver{current: ctx}
}

func (r *srtpReceiver) rekey(ctx *srtp.Context) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.used || r.previous == nil {
		r.previous = r.current
	}
	r.current = ctx
	r.used = false
}

func (r *srtpReceiver) DecryptRTP(dst, encrypted []byte, header *rtp.Header) ([]byte, error) {
	return r.decrypt(encrypted, func(ctx *srtp.Context, data []byte) ([]byte, error) {
		return ctx.DecryptRTP(dst, data, header)
	})
}

func (r *srtpReceiver) DecryptRTCP(dst, encrypted []byte, header *rtcp.Header) ([]byte, error) {
	return r.decrypt(encrypted, func(ctx *srtp.Context, data []byte) ([]byte, error) {
		return ctx.DecryptRTCP(dst, data, header)
	})
}

func (r *srtpReceiver) decrypt(encrypted []byte, decrypt func(*srtp.Context, []byte) ([]byte, error)) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.previous != nil && r.used && time.Now().After(r.previousUntil) {
		r.previous = nil
	}
	// Failed in-place decryption may modify the input before a retry.
	var saved []byte
	if r.previous != nil {
		saved = bytes.Clone(encrypted)
	}
	data, err := decrypt(r.current, encrypted)
	if err != nil {
		if r.previous != nil {
			return decrypt(r.previous, saved)
		}
		return nil, err
	}
	if !r.used {
		r.used = true
		r.previousUntil = time.Now().Add(srtpRekeyGracePeriod)
	}
	return data, nil
}
