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

type DialogCache[T DialogSession] interface {
	DialogStore(ctx context.Context, id string, v T) error
	DialogLoad(ctx context.Context, id string) (T, error)
	DialogDelete(ctx context.Context, id string) error
	DialogRange(ctx context.Context, f func(id string, d T) bool) error
}

// Non optimized for now
type dialogCacheMap[T DialogSession] struct{ sync.Map }

func (m *dialogCacheMap[T]) DialogStore(ctx context.Context, id string, v T) error {
	m.Store(id, v)
	return nil
}

func (m *dialogCacheMap[T]) DialogDelete(ctx context.Context, id string) error {
	m.Delete(id)
	return nil
}

func (m *dialogCacheMap[T]) DialogLoad(ctx context.Context, id string) (dialog T, err error) {
	d, ok := m.Load(id)
	if !ok {
		return dialog, sipgo.ErrDialogDoesNotExists
	}
	// interface to interface conversion is slow
	return d.(T), nil
}

func (m *dialogCacheMap[T]) DialogRange(ctx context.Context, f func(id string, d T) bool) error {
	m.Range(func(key, value any) bool {
		return f(key.(string), value.(T))
	})
	return nil
}

type DialogData struct {
	InviteRequest sip.Request
	State         sip.DialogState
}

type DialogCachePool struct {
	client DialogCache[*DialogClientSession]
	server DialogCache[*DialogServerSession]
}

func (p *DialogCachePool) MatchDialogClient(req *sip.Request) (*DialogClientSession, error) {
	id, err := sip.DialogIDFromRequestUAC(req)
	if err != nil {
		return nil, errors.Join(err, sipgo.ErrDialogOutsideDialog)
	}

	val, err := p.client.DialogLoad(context.Background(), id)
	if err != nil {
		return nil, err
	}

	return val, nil
}

func (p *DialogCachePool) MatchDialogServer(req *sip.Request) (*DialogServerSession, error) {
	id, err := sip.DialogIDFromRequestUAS(req)
	if err != nil {
		return nil, errors.Join(err, sipgo.ErrDialogOutsideDialog)
	}

	val, err := p.server.DialogLoad(context.Background(), id)
	if err != nil {
		return nil, err
	}

	return val, nil
}

func (p *DialogCachePool) MatchDialog(req *sip.Request) (*DialogServerSession, *DialogClientSession, error) {
	d, err := p.MatchDialogServer(req)
	if err != nil {
		if !errors.Is(err, sipgo.ErrDialogDoesNotExists) {
			return nil, nil, err
		}

		cd, err := p.MatchDialogClient(req)
		return nil, cd, err
	}
	return d, nil, nil
}
