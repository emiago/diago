# Direct VoIP WebRTC client and server

These two processes establish a SIP call whose media uses diago's direct ICE,
DTLS-SRTP and RTP stack. Both endpoints play the bundled demonstration audio
and create a stereo recording with a fixed name under `/tmp`.

Start the server:

```sh
go run ./examples/voip_webrtc/server
```

Then start the client:

```sh
go run ./examples/voip_webrtc/client
```

The recordings are written to:

- `/tmp/diago-voip-webrtc-server.wav`
- `/tmp/diago-voip-webrtc-client.wav`
