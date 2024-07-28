package diago

import (
	"errors"
	"sync"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

var (
	dialogsClient = sync.Map{}
	dialogsServer = sync.Map{}
)

func MatchDialogClient(req *sip.Request) (*DialogClientSession, error) {
	id, err := sip.UACReadRequestDialogID(req)
	if err != nil {
		return nil, errors.Join(err, sipgo.ErrDialogOutsideDialog)
	}

	val, ok := dialogsClient.Load(id)
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

	val, ok := dialogsServer.Load(id)
	if !ok || val == nil {
		return nil, sipgo.ErrDialogDoesNotExists
	}

	d := val.(*DialogServerSession)
	return d, nil
}
