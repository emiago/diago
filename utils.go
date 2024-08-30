package diago

import (
	"sync"

	"github.com/emiago/diago/media"
)

var rtpBufPool = sync.Pool{
	New: func() any {
		return make([]byte, media.RTPBufSize)
	},
}
