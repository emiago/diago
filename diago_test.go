package diago

import (
	"context"
	"testing"

	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func testDiagoClient(t *testing.T, onRequest func(req *sip.Request) *sip.Response) *Diago {
	// Create client transaction request
	cTxReq := &clientTxRequester{
		onRequest: onRequest,
	}

	ua, _ := sipgo.NewUA()
	client, _ := sipgo.NewClient(ua)
	client.TxRequester = cTxReq
	t.Cleanup(func() {
		ua.Close()
	})
	return NewDiago(ua, WithClient(client))
}

func TestDiagoRegister(t *testing.T) {
	dg := testDiagoClient(t, func(req *sip.Request) *sip.Response {
		return sip.NewResponseFromRequest(req, 200, "OK", nil)
	})

	ctx := context.TODO()
	rtx, err := dg.RegisterTransaction(ctx, sip.Uri{User: "alice", Host: "localhost"}, RegisterOptions{})
	require.NoError(t, err)

	err = rtx.Register(ctx)
	require.NoError(t, err)
}

func TestDiagoInviteCallerID(t *testing.T) {

	t.Run("NoSDPInResponse", func(t *testing.T) {
		dg := testDiagoClient(t, func(req *sip.Request) *sip.Response {
			return sip.NewResponseFromRequest(req, 200, "OK", nil)
		})

		_, err := dg.Invite(context.Background(), sip.Uri{User: "alice", Host: "localhost"}, InviteOptions{})
		if assert.Error(t, err) {
			assert.Equal(t, "no SDP in response", err.Error())
		}
	})

	reqCh := make(chan *sip.Request)
	dg := testDiagoClient(t, func(req *sip.Request) *sip.Response {
		reqCh <- req
		return sip.NewResponseFromRequest(req, 500, "", nil)
	})

	t.Run("DefaultCallerID", func(t *testing.T) {
		go dg.Invite(context.Background(), sip.Uri{User: "alice", Host: "localhost"}, InviteOptions{})
		req := <-reqCh

		assert.Equal(t, dg.ua.Name(), req.From().Address.User)
		assert.Equal(t, dg.client.GetHostname(), req.From().Address.Host)
		assert.NotEmpty(t, req.From().Params["tag"])
	})

	t.Run("SetCallerid", func(t *testing.T) {
		opts := InviteOptions{}
		opts.SetCaller("Test", "123456")
		go dg.Invite(context.Background(), sip.Uri{User: "alice", Host: "localhost"}, opts)
		req := <-reqCh
		assert.Equal(t, "Test", req.From().DisplayName)
		assert.Equal(t, "123456", req.From().Address.User)
		assert.NotEmpty(t, req.From().Params["tag"])
	})

	t.Run("SetCalleridWithBridgeOriginator", func(t *testing.T) {
		t.Skip("NOT IMPLEMENTED")
	})
}
