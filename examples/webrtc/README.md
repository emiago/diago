
# Setup


## 1. Backend 

Run webrtc example
```bash 
go run ./examples/webrtc
```
You should listen on `:5443` port

## 2. Frontend (Softphone)

Go to simpl5 project and use some http simple server to start serving it

```bash 
git clone https://github.com/DoubangoTelecom/sipml5
cd sipml5

gohttp -l "127.0.0.1:7000" # Start serving files on port 7000
```


### Setup phone

#### Expert mode
open in browser: http://127.0.0.1:7000/expert.htm

Set this options 
- Disable video: `checked`
- Websocket URL: `ws://127.0.0.1:5443`
- ICE Server: `[]` **USE EMPTY array to avoid SLOW STUN Resolution**
- Disable 3GPP Early IMS: `checked`
- Disable Call button options: `checked`


#### Open phone

open in browser: http://127.0.0.1:7000/call.htm

Fill needed fields:
- Display Name: `test`
- Private Identity: `test`
- Public Identity: `sip:test@127.0.0.1`

### Login and make a call

Phone needs login (REGISTER) to allow making calls 

1. Login and dial any number. Allow Microphone usage
2. After setup you should hear sound.



# TLS For WSS

For full security you need WSS

Use `root-ca.pem` and load into your browser certificates. This are already loaded by go example

**Chrome**: settings -> manage certificates -> authority -> load -> select `root-ca.pem`  
`localhost` should appear as CN

This also needs to be loaded into your server.
```go
transportWS := diago.Transport{
		Transport:    "wss", // <- HERE
		BindHost:     host,
		BindPort:     port,
		ExternalHost: host,
		ExternalPort: port,
		TLSConf:      testdata.ServerTLSConfig(), // <- HERE
	}
```