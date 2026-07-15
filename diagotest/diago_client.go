package diagotest

import (
	"github.com/emiago/diago"
	"github.com/emiago/sipgo"
	"github.com/emiago/sipgo/sip"
)

func NewDiagoClientTest(ua *sipgo.UserAgent, onRequest func(req *sip.Request) *sip.Response) *diago.Diago {
	// Create client transaction request
	cTxReq := &clientTxRequester{
		onRequest: onRequest,
	}

	client, _ := sipgo.NewClient(ua)
	client.TxRequester = cTxReq
	return diago.NewDiago(ua, diago.WithClient(client))
}
