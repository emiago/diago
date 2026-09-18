// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package media

import (
	"testing"

	"github.com/pion/srtp/v3"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSDESKeyParam(t *testing.T) {
	const key = "8Dlz/SyzlAKCZwH49w5DX8S4pDa7Lw0n3LTI4t6Z"
	profile := srtp.ProtectionProfileAes128CmHmacSha1_80

	tests := []struct {
		name     string
		value    string
		lifetime uint64
		err      string
	}{
		{name: "plain", value: "inline:" + key},
		{name: "power of two", value: "inline:" + key + "|2^31", lifetime: 1 << 31},
		{name: "decimal", value: "inline:" + key + "|2147483648", lifetime: 1 << 31},
		{name: "case insensitive method", value: "INLINE:" + key},
		{name: "empty lifetime", value: "inline:" + key + "|", err: "lifetime is empty"},
		{name: "zero", value: "inline:" + key + "|0", err: "must be positive"},
		{name: "leading zero", value: "inline:" + key + "|01", err: "leading zeroes"},
		{name: "power with leading zero", value: "inline:" + key + "|2^01", err: "leading zeroes"},
		{name: "power too large", value: "inline:" + key + "|2^32", err: "exceeds the maximum"},
		{name: "decimal too large", value: "inline:" + key + "|2147483649", err: "exceeds the maximum"},
		{name: "MKI without lifetime", value: "inline:" + key + "|1:4", err: "MKI is not supported"},
		{name: "MKI with lifetime", value: "inline:" + key + "|2^20|1:4", err: "MKI is not supported"},
		{name: "too many inline fields", value: "inline:" + key + "|2^20|1:4|2:4", err: "invalid SDES inline key parameters"},
		{name: "multiple keys", value: "inline:" + key + ";inline:" + key, err: "multiple SDES keys are not supported"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			param, err := parseSDESKeyParam(tt.value, profile)
			if tt.err != "" {
				require.ErrorContains(t, err, tt.err)
				return
			}
			require.NoError(t, err)
			assert.Len(t, param.masterKey, 16)
			assert.Len(t, param.masterSalt, 14)
			assert.Equal(t, tt.lifetime, param.lifetime)
		})
	}
}
