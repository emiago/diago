---
# title: Unit testing
# cascade:
#   type: docs
---


### How to UNIT test server 


First make sure you have imported siptest
```go
import "github.com/emiago/sipgo/siptest"
```

Example with testing REGISTER request
```go 

func handleRegister(req *sip.Request, tx sip.ServerTransaction) {
  res := sip.NewResponseFromRequest(req, sip.StatusBadRequest, "Bad Request", nil)
  tx.Respond(res)
}


func TestServerHandlers(t *testing.T) {
	// Setup server
	uas, _ := sipgo.NewUA()
	srv, _ := sipgo.NewServer(uas)
	srv.OnRegister(handleRegister)
	
  // Create request
	req := sip.NewRequest(sip.REGISTER, sip.Uri{User: "alice", Host: "localhost"})

	// Use dummy client to build request headers
	uac, _ := sipgo.NewUA()
	client, _ := sipgo.NewClient(uac)
	sipgo.ClientRequestBuild(client, req)

	// Create transaction Recorder
	txRecord := siptest.NewServerTxRecorder(req)

	// Run handler and read response
	handleRegister(req, txRecord)
	responses := txRecord.Result()
	require.Len(t, responses, 1)
	assert.EqualValues(t, 400, responses[0].StatusCode)
} 
```