package diago

import "context"

type DialogSession interface {
	Id() string
	Context() context.Context
	Hangup(ctx context.Context) error
	Media() *DialogMedia
}
