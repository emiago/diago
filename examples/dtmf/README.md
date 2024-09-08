
Run playback as server
```bash
go run ./examples/dtmf
```

Dial in and send DTMF. On upper terminal you should see DTMF detected and printed
```sh 
gophone dial -dtmf="1234ABCD" sip:112@127.0.0.1
```