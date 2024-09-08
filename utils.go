// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"io"
	"sync"

	"github.com/emiago/diago/media"
)

var rtpBufPool = sync.Pool{
	New: func() any {
		return make([]byte, media.RTPBufSize)
	},
}

func copyWithBuf(reader io.Reader, writer io.Writer, payloadBuf []byte) (int64, error) {
	return media.CopyWithBuf(reader, writer, payloadBuf)
}
