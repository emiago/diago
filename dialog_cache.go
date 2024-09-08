// SPDX-License-Identifier: MPL-2.0
// SPDX-FileCopyrightText: Copyright (c) 2024, Emir Aganovic

package diago

import (
	"context"
	"errors"
	"sync"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

var (
	// TODO, replace with typed versions
	dialogsClientCache DialogCache = &dialogCacheMap{sync.Map{}}
	dialogsServerCache DialogCache = &dialogCacheMap{sync.Map{}}
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
		return nil, sipgo.ErrDialogDoesNotExists
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

type DialogData struct {
	InviteRequest sip.Request
	State         sip.DialogState
}

func MatchDialogClient(req *sip.Request) (*DialogClientSession, error) {
	id, err := sip.UACReadRequestDialogID(req)
	if err != nil {
		return nil, errors.Join(err, sipgo.ErrDialogOutsideDialog)
	}

	val, err := dialogsClientCache.DialogLoad(context.Background(), id)
	if err != nil {
		return nil, err
	}

	d := val.(*DialogClientSession)
	return d, nil
}

func MatchDialogServer(req *sip.Request) (*DialogServerSession, error) {
	id, err := sip.UASReadRequestDialogID(req)
	if err != nil {
		return nil, errors.Join(err, sipgo.ErrDialogOutsideDialog)
	}

	val, err := dialogsServerCache.DialogLoad(context.Background(), id)
	if err != nil {
		return nil, err
	}

	d := val.(*DialogServerSession)
	return d, nil
}

func MatchDialog(req *sip.Request) (*DialogServerSession, *DialogClientSession, error) {
	d, err := MatchDialogServer(req)
	if err != nil {
		if !errors.Is(err, sipgo.ErrDialogDoesNotExists) {
			return nil, nil, err
		}

		cd, err := MatchDialogClient(req)
		return nil, cd, err
	}
	return d, nil, nil
}
