package diago

import (
	"context"
	"testing"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/stretchr/testify/require"
)

func TestDiagoRegister(t *testing.T) {
	// Create client transaction request
	cTxReq := &clientTxRequester{
		onRequest: func(req *sip.Request) *sip.Response {
			return sip.NewResponseFromRequest(req, 200, "OK", nil)
		},
	}

	ua, _ := sipgo.NewUA()
	client, _ := sipgo.NewClient(ua)
	client.TxRequester = cTxReq

	dg := NewDiago(ua, WithClient(client))

	ctx := context.TODO()
	rtx, err := dg.RegisterTransaction(ctx, sip.Uri{User: "alice", Host: "localhost"}, RegisterOptions{})
	require.NoError(t, err)

	err = rtx.Register(ctx)
	require.NoError(t, err)
}
