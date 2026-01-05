Run bridge app that always bridges with bob on port `5090`
```bash
go run ./examples/bridge sip:bob@127.0.0.1:5090
```

Run receiver:
```bash 
gophone answer -ua bob -l 127.0.0.1:5090
```

Dial server on `5060` be bridged with bob on `5090`
```sh 
gophone dial -ua alice sip:bob@127.0.0.1:5060
```