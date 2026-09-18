// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package media

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/pion/srtp/v3"
)

// ErrSRTPKeyLifetimeExceeded indicates that an incoming SRTP or SRTCP packet
// was rejected because the remote SDES master key reached its packet limit.
var ErrSRTPKeyLifetimeExceeded = errors.New("SRTP master key lifetime exceeded")

const (
	maxSRTPKeyLifetime  uint64 = 1 << 48
	maxSRTCPKeyLifetime uint64 = 1 << 31
)

type sdesKeyParam struct {
	masterKey  []byte
	masterSalt []byte
	lifetime   uint64
}

type srtpInboundPolicy struct {
	rtpLimit    uint64
	rtcpLimit   uint64
	rtpPackets  uint64
	rtcpPackets uint64
}

func newSRTPInboundPolicy(lifetime uint64) srtpInboundPolicy {
	policy := srtpInboundPolicy{
		rtpLimit:  maxSRTPKeyLifetime,
		rtcpLimit: maxSRTCPKeyLifetime,
	}
	if lifetime > 0 {
		policy.rtpLimit = lifetime
		policy.rtcpLimit = lifetime
	}
	return policy
}

func (p *srtpInboundPolicy) checkRTP() error {
	if p.rtpLimit > 0 && p.rtpPackets >= p.rtpLimit {
		return fmt.Errorf("%w for RTP after %d packets", ErrSRTPKeyLifetimeExceeded, p.rtpLimit)
	}
	return nil
}

func (p *srtpInboundPolicy) checkRTCP() error {
	if p.rtcpLimit > 0 && p.rtcpPackets >= p.rtcpLimit {
		return fmt.Errorf("%w for RTCP after %d packets", ErrSRTPKeyLifetimeExceeded, p.rtcpLimit)
	}
	return nil
}

func parseSDESKeyParam(value string, profile srtp.ProtectionProfile) (sdesKeyParam, error) {
	if strings.Contains(value, ";") {
		return sdesKeyParam{}, fmt.Errorf("multiple SDES keys are not supported")
	}

	method, keyInfo, ok := strings.Cut(value, ":")
	if !ok || !strings.EqualFold(method, "inline") {
		return sdesKeyParam{}, fmt.Errorf("unsupported SDES key method in %q", value)
	}

	keySaltEncoded := keyInfo
	lifetime := ""
	hasLifetime := false
	if i := strings.IndexByte(keyInfo, '|'); i >= 0 {
		keySaltEncoded = keyInfo[:i]
		lifetime = keyInfo[i+1:]
		hasLifetime = true

		if j := strings.IndexByte(lifetime, '|'); j >= 0 {
			if strings.IndexByte(lifetime[j+1:], '|') >= 0 {
				return sdesKeyParam{}, fmt.Errorf("invalid SDES inline key parameters")
			}
			return sdesKeyParam{}, fmt.Errorf("SDES MKI is not supported")
		}
		if strings.Contains(lifetime, ":") {
			return sdesKeyParam{}, fmt.Errorf("SDES MKI is not supported")
		}
	}

	keySalt, err := base64.StdEncoding.DecodeString(keySaltEncoded)
	if err != nil {
		return sdesKeyParam{}, fmt.Errorf("failed to decode SDES key: %w", err)
	}

	keyLen, err := profile.KeyLen()
	if err != nil {
		return sdesKeyParam{}, fmt.Errorf("failed to get SDES key length: %w", err)
	}
	saltLen, err := profile.SaltLen()
	if err != nil {
		return sdesKeyParam{}, fmt.Errorf("failed to get SDES salt length: %w", err)
	}
	if len(keySalt) != keyLen+saltLen {
		return sdesKeyParam{}, fmt.Errorf("expected %d-byte key and salt, got %d", keyLen+saltLen, len(keySalt))
	}

	param := sdesKeyParam{
		masterKey:  keySalt[:keyLen],
		masterSalt: keySalt[keyLen:],
	}
	if hasLifetime {
		param.lifetime, err = parseSDESLifetime(lifetime)
		if err != nil {
			return sdesKeyParam{}, err
		}
	}
	return param, nil
}

func parseSDESLifetime(value string) (uint64, error) {
	if value == "" {
		return 0, fmt.Errorf("SDES key lifetime is empty")
	}

	powerOfTwo := strings.HasPrefix(value, "2^")
	digits := value
	if powerOfTwo {
		digits = strings.TrimPrefix(value, "2^")
	}
	if digits == "" {
		return 0, fmt.Errorf("invalid SDES key lifetime %q", value)
	}
	if len(digits) > 1 && digits[0] == '0' {
		return 0, fmt.Errorf("invalid SDES key lifetime %q: leading zeroes are not allowed", value)
	}
	for _, digit := range digits {
		if digit < '0' || digit > '9' {
			return 0, fmt.Errorf("invalid SDES key lifetime %q", value)
		}
	}

	n, err := strconv.ParseUint(digits, 10, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid SDES key lifetime %q: %w", value, err)
	}

	var lifetime uint64
	if powerOfTwo {
		if n > 31 {
			return 0, fmt.Errorf("SDES key lifetime %q exceeds the maximum of 2^31 packets", value)
		}
		lifetime = uint64(1) << n
	} else {
		lifetime = n
	}
	if lifetime == 0 {
		return 0, fmt.Errorf("SDES key lifetime must be positive")
	}
	// One lifetime applies independently to both SRTP and SRTCP. SRTCP has
	// the lower suite limit, so it bounds the value that can be signaled.
	if lifetime > maxSRTCPKeyLifetime {
		return 0, fmt.Errorf("SDES key lifetime %q exceeds the maximum of 2^31 packets", value)
	}
	return lifetime, nil
}

const (
	SRTPProfileAes128CmHmacSha1_80 uint16 = uint16(srtp.ProtectionProfileAes128CmHmacSha1_80)
	SRTPProfileAes256CmHmacSha1_80 uint16 = uint16(srtp.ProtectionProfileAes256CmHmacSha1_80)
	SRTPProfileAeadAes128Gcm       uint16 = uint16(srtp.ProtectionProfileAeadAes128Gcm)
	SRTPProfileAeadAes256Gcm       uint16 = uint16(srtp.ProtectionProfileAeadAes256Gcm)
	SRTPProfileNullHmacSha1_80     uint16 = uint16(srtp.ProtectionProfileNullHmacSha1_80)
)

func srtpProfileString(p srtp.ProtectionProfile) string {
	switch p {
	case srtp.ProtectionProfileAes128CmHmacSha1_80:
		return "AES_CM_128_HMAC_SHA1_80"
	case srtp.ProtectionProfileAes256CmHmacSha1_80:
		return "AES_CM_256_HMAC_SHA1_80"
	case srtp.ProtectionProfileAeadAes128Gcm:
		return "AEAD_AES_128_GCM"
	case srtp.ProtectionProfileAeadAes256Gcm:
		return "AEAD_AES_256_GCM"
	case srtp.ProtectionProfileNullHmacSha1_80:
		return "NULL_HMAC_SHA1_80"
	}
	// TODO: this is still wrong
	return strings.TrimPrefix("SRTP_", p.String())
}

func srtpProfileParse(alg string) srtp.ProtectionProfile {
	var profile srtp.ProtectionProfile
	switch alg {
	case "AES_CM_128_HMAC_SHA1_80":
		profile = srtp.ProtectionProfileAes128CmHmacSha1_80
	case "AES_CM_256_HMAC_SHA1_80":
		profile = srtp.ProtectionProfileAes256CmHmacSha1_80
	case "AEAD_AES_128_GCM":
		profile = srtp.ProtectionProfileAeadAes128Gcm
	case "AEAD_AES_256_GCM":
		profile = srtp.ProtectionProfileAeadAes256Gcm
	case "NULL_HMAC_SHA1_80":
		profile = srtp.ProtectionProfileNullHmacSha1_80
	}
	return profile
}
