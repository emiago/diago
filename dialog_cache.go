// SPDX-License-Identifier: MPL-2.0
// Copyright (C) 2024 Emir Aganovic

package diago

import (
	"errors"
	"sync"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

var (
	DialogsClientCache = sync.Map{}
	DialogsServerCache = sync.Map{}
)

type DialogData struct {
	InviteRequest sip.Request
	State         sip.DialogState
}

func MatchDialogClient(req *sip.Request) (*DialogClientSession, error) {
	id, err := sip.UACReadRequestDialogID(req)
	if err != nil {
		return nil, errors.Join(err, sipgo.ErrDialogOutsideDialog)
	}

	val, ok := DialogsClientCache.Load(id)
	if !ok || val == nil {
		return nil, sipgo.ErrDialogDoesNotExists
	}

	d := val.(*DialogClientSession)
	return d, nil
}

func MatchDialogServer(req *sip.Request) (*DialogServerSession, error) {
	id, err := sip.UASReadRequestDialogID(req)
	if err != nil {
		return nil, errors.Join(err, sipgo.ErrDialogOutsideDialog)
	}

	val, ok := DialogsServerCache.Load(id)
	if !ok || val == nil {
		return nil, sipgo.ErrDialogDoesNotExists
	}

	d := val.(*DialogServerSession)
	return d, nil
}
