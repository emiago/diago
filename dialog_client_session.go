package diago

import (
	"context"

	"github.com/emiago/sipgo"
)

// DialogClientSession represents outbound channel
type DialogClientSession struct {
	*sipgo.DialogClientSession

	// MediaSession *media.MediaSession // For normal media
	DialogMedia
}

func (d *DialogClientSession) Close() {
	if d.MediaSession != nil {
		d.MediaSession.Close()
	}

	d.DialogClientSession.Close()
}

func (d *DialogClientSession) Id() string {
	return d.ID
}

func (d *DialogClientSession) Hangup(ctx context.Context) error {
	return d.Bye(ctx)
}

func (d *DialogClientSession) FromUser() string {
	return d.InviteRequest.From().Address.User
}

func (d *DialogClientSession) ToUser() string {
	return d.InviteRequest.To().Address.User
}
