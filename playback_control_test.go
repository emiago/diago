// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"bytes"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAudioControl(t *testing.T) {
	receiver := bytes.NewBuffer([]byte{})
	p := audioControl{
		Writer: receiver,
	}

	payload := []byte{1, 2, 3}

	p.Write(payload)
	res := payload
	require.Equal(t, payload, receiver.Bytes())

	p.Write(payload)
	res = append(res, payload...)
	require.Equal(t, res, receiver.Bytes())

	p.Mute(true)
	p.Write(payload)
	res = append(res, []byte{0, 0, 0}...)
	require.Equal(t, res, receiver.Bytes())

	p.Mute(false)
	p.Write(payload)
	res = append(res, payload...)
	require.Equal(t, res, receiver.Bytes())
}
