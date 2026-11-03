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

const (
	srtpRekeyGracePeriod   = 5 * time.Second
	srtpRekeyPendingPeriod = 32 * time.Second
)

// srtpReceiver shares receive-key updates with readers on forked sessions.
type srtpReceiver struct {
	mu             sync.Mutex
	current        *srtp.Context
	currentKey     string
	pending        *srtp.Context
	pendingKey     string
	pendingUntil   time.Time
	previous       *srtp.Context
	previousUntil  time.Time
	currentPolicy  srtpInboundPolicy
	pendingPolicy  srtpInboundPolicy
	previousPolicy srtpInboundPolicy
}

func newSRTPReceiver(ctx *srtp.Context) *srtpReceiver {
	return &srtpReceiver{current: ctx, currentPolicy: newSRTPInboundPolicy(0)}
}

func (r *srtpReceiver) rekey(ctx *srtp.Context, key string, lifetime uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pending, r.pendingKey = ctx, key
	r.pendingPolicy = newSRTPInboundPolicy(lifetime)
	r.pendingUntil = time.Now().Add(srtpRekeyPendingPeriod)
}

func (r *srtpReceiver) limitLifetime(key string, lifetime uint64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if key == r.currentKey {
		r.currentPolicy.limitLifetime(lifetime)
	} else if r.pending != nil && key == r.pendingKey {
		r.pendingPolicy.limitLifetime(lifetime)
	}
}

// Called only by decrypt while holding mu; policy travels with its context.
func (r *srtpReceiver) policy(ctx *srtp.Context) *srtpInboundPolicy {
	if ctx == r.current {
		return &r.currentPolicy
	}
	if ctx == r.pending {
		return &r.pendingPolicy
	}
	return &r.previousPolicy
}

func (r *srtpReceiver) expire(now time.Time) {
	if r.pending != nil && !now.Before(r.pendingUntil) {
		r.pending, r.pendingKey = nil, ""
	}
	if r.previous != nil && !now.Before(r.previousUntil) {
		r.previous = nil
	}
}

func (r *srtpReceiver) hasKey(key string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expire(time.Now())
	return r.currentKey == key || (r.pending != nil && r.pendingKey == key)
}

func (r *srtpReceiver) DecryptRTP(dst, encrypted []byte, header *rtp.Header) ([]byte, error) {
	return r.decrypt(encrypted, func(ctx *srtp.Context, data []byte) ([]byte, error) {
		policy := r.policy(ctx)
		if err := policy.checkRTP(); err != nil {
			return nil, err
		}
		plain, err := ctx.DecryptRTP(dst, data, header)
		if err == nil {
			policy.rtpPackets++
		}
		return plain, err
	})
}

func (r *srtpReceiver) DecryptRTCP(dst, encrypted []byte, header *rtcp.Header) ([]byte, error) {
	return r.decrypt(encrypted, func(ctx *srtp.Context, data []byte) ([]byte, error) {
		policy := r.policy(ctx)
		if err := policy.checkRTCP(); err != nil {
			return nil, err
		}
		plain, err := ctx.DecryptRTCP(dst, data, header)
		if err == nil {
			policy.rtcpPackets++
		}
		return plain, err
	})
}

func (r *srtpReceiver) decrypt(encrypted []byte, decrypt func(*srtp.Context, []byte) ([]byte, error)) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.expire(time.Now())
	// Failed in-place decryption may modify the input before a retry.
	var saved []byte
	if r.pending != nil || r.previous != nil {
		saved = bytes.Clone(encrypted)
	}
	data, err := decrypt(r.current, encrypted)
	if err == nil {
		return data, nil
	}
	if r.pending != nil {
		candidateInput := bytes.Clone(saved)
		candidate, candidateErr := decrypt(r.pending, candidateInput)
		if candidateErr == nil {
			r.previous, r.previousUntil = r.current, time.Now().Add(srtpRekeyGracePeriod)
			r.previousPolicy = r.currentPolicy
			r.current, r.currentKey = r.pending, r.pendingKey
			r.currentPolicy = r.pendingPolicy
			r.pending, r.pendingKey = nil, ""
			return candidate, nil
		}
	}
	if r.previous != nil {
		return decrypt(r.previous, saved)
	}
	return nil, err
}
