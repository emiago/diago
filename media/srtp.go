// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package media

import (
	"strings"

	"github.com/pion/srtp/v3"
)

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
