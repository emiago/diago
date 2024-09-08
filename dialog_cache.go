// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"context"
	"errors"
	"fmt"
	"sync"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

type DialogCache interface {
	DialogStore(ctx context.Context, id string, v DialogSession) error
	DialogLoad(ctx context.Context, id string) (DialogSession, error)
	DialogDelete(ctx context.Context, id string) error
}

// Non optimized for now
type dialogCacheMap struct{ sync.Map }

func (m *dialogCacheMap) DialogStore(ctx context.Context, id string, v DialogSession) error {
	m.Store(id, v)
	return nil
}

func (m *dialogCacheMap) DialogDelete(ctx context.Context, id string) error {
	m.Delete(id)
	return nil
}

func (m *dialogCacheMap) DialogLoad(ctx context.Context, id string) (DialogSession, error) {
	d, ok := m.Load(id)
	if !ok {
		return nil, fmt.Errorf("not exists")
	}
	// interface to interface conversion is slow
	return d.(DialogSession), nil
}

// TODO consider modern iterator
func (m *dialogCacheMap) DialogRange(ctx context.Context, f func(id string, v DialogSession) bool) error {
	m.Range(func(key, value any) bool {
		id := key.(string)
		v := value.(DialogSession)
		return f(id, v)
	})
	return nil
}

var (
	// TODO, replace with typed versions
	dialogsClientCache = dialogCacheMap{sync.Map{}}
	dialogsServerCache = dialogCacheMap{sync.Map{}}
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

	val, ok := dialogsClientCache.Load(id)
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

	val, ok := dialogsServerCache.Load(id)
	if !ok || val == nil {
		return nil, sipgo.ErrDialogDoesNotExists
	}

	d := val.(*DialogServerSession)
	return d, nil
}
