// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"testing"

	"github.com/emiago/diago/media"
	"github.com/pion/srtp/v3"
)

// countingAllocator records whether it was consulted at all.
type countingAllocator struct {
	allocated int
	released  int
}

func (a *countingAllocator) AllocateRTPPort() (int, error) {
	a.allocated++
	return 0, nil // 0 lets the OS pick, so the session still binds
}

func (a *countingAllocator) ReleaseRTPPort(port int) {
	a.released++
}

// TestMediaConfigPropagation pins the fields a dialog inherits from the Diago
// wide media config. initMediaSessionFromConf reads them from the per dialog
// config, so a field the per dialog config drops is silently inert however the
// caller sets it.
func TestMediaConfigPropagation(t *testing.T) {
	t.Run("RTPPortAllocator reaches the dialog", func(t *testing.T) {
		alloc := &countingAllocator{}
		dg := &Diago{mediaConf: MediaConfig{RTPPortAllocator: alloc}}

		conf := dg.mediaConfForTransport(&Transport{})

		if conf.RTPPortAllocator == nil {
			t.Fatal("per dialog config dropped RTPPortAllocator: the allocator can never be consulted")
		}
		if conf.RTPPortAllocator != RTPPortAllocator(alloc) {
			t.Fatal("per dialog config carries a different allocator")
		}
	})

	t.Run("SecureRTPAlg reaches the dialog", func(t *testing.T) {
		const alg = uint16(srtp.ProtectionProfileAes256CmHmacSha1_80)
		dg := &Diago{mediaConf: MediaConfig{SecureRTPAlg: alg}}

		conf := dg.mediaConfForTransport(&Transport{})

		if conf.SecureRTPAlg != alg {
			t.Fatalf("per dialog config dropped SecureRTPAlg: got %d, want %d", conf.SecureRTPAlg, alg)
		}
	})

	t.Run("Codecs reach the dialog", func(t *testing.T) {
		dg := &Diago{mediaConf: MediaConfig{Codecs: []media.Codec{media.CodecAudioAlaw}}}

		conf := dg.mediaConfForTransport(&Transport{})

		if len(conf.Codecs) != 1 || conf.Codecs[0].Name != media.CodecAudioAlaw.Name {
			t.Fatalf("per dialog config dropped Codecs: got %v", conf.Codecs)
		}
	})
}
