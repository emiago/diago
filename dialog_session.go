// SPDX-License-Identifier: BSD-2-Clause
// Copyright (C) 2024 Emir Aganovic

package diago

import (
	"context"

	"github.com/emiago/sipgo"
)

type DialogSession interface {
	Id() string
	Context() context.Context
	Hangup(ctx context.Context) error
	Media() *DialogMedia
	DialogSIP() *sipgo.Dialog
}
