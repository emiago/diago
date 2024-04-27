package diago

import (
	"context"

	"github.com/emiago/sipgox"
)

type DialogSession interface {
	// Dialog() sipgo.Dialog
	Hangup(ctx context.Context) error
	MediaSession() *sipgox.MediaSession
}

type DialogMedia struct {
	// DO NOT use IT or mix with reader and writer, unless it is specific case
	mediaSession *sipgox.MediaSession

	*sipgox.RTPWriter
	*sipgox.RTPReader
}

func (d *DialogMedia) MediaSession() *sipgox.MediaSession {
	return d.mediaSession
}
