[![Go Report Card](https://goreportcard.com/badge/github.com/emiago/diago)](https://goreportcard.com/report/github.com/emiago/diago)
![Coverage](https://img.shields.io/badge/coverage-38.1%25-blue)
[![License](https://img.shields.io/badge/License-BSD_2--Clause-orange.svg)](https://github.com/emiago/sipgo/LICENCE) 
![GitHub go.mod Go version](https://img.shields.io/github/go-mod/go-version/emiago/media)


# Diago

Short of **dialog + GO**.  
Framework for building comunication (VOIP) solutions in GO. 

It is providing more developer friendly solution on top of [SIPgo](https://github.com/emiago/sipgo) with **media** stack integrations. 



**Library is here shared only partially with some core features. If you want to get extended version with more features, support or consulting checkout**  [Support section](#support)**

Stack: 
- Signaling: SIP
- Media: RTP/UDP, Webrtc

**NOTE: This API design is WIP and no PRs are accepted, but issues or feature requests are welcome**

# Getting started

diago acts as UAS(User Agent Server) and UAC(User Agent Client). For now it keeps abstractions only where it needs.


#### Setup diago:
```go
ua, _ := sipgo.NewUA()
transportUDP := diago.Transport{
	Transport: "udp",
	BindHost:  "127.0.0.1",
	BindPort:  5060,
}

transportTCP := diago.Transport{
	Transport: "tcp",
	BindHost:  "127.0.0.1",
	BindPort:  5060,
}

d := diago.NewDiago(ua,
	diago.WithTransport(transportUDP),
	diago.WithTransport(transportTCP),
)
```


#### Incoming call
```go
d.Serve(ctx, func(inDialog *diago.DialogServerSession) {
	// - Do your call routing.
	switch inDialog.ToUser() {
		case "answer":
		case "123456"
	}
})
```

#### Outgoing

```go
dialog, err := d.Dial(ctx, recipient sip.Uri, bridge *Bridge, opts sipgo.AnswerOptions)
dialog.Hangup()
```

## Answering call

```go
func Answer(inDialog *diago.DialogServerSession) {
	inDialog.Progress() // Progress -> 100 Trying
	inDialog.Ringing()  // Ringing -> 180 Response

	if err := inDialog.Answer(); err != nil {
		fmt.Println("Failed to answer", err)
		return
	}

	ctx := inDialog.Context()
	select {
	case <-ctx.Done():
	case <-time.After(1 * time.Minute):
	}
}
```

## Playing file with Playback

Playing file is done by playback. 

Supported formats:
- wav (PCM)

```go
func Playback(inDialog *diago.DialogServerSession) {
	inDialog.Ringing()

	playfile, err := os.Open("demo-instruct.wav")
	if err != nil {
		fmt.Println("Failed to open file", err)
		return
	}

	pb, err := inDialog.PlaybackCreate()
	if err != nil {
		fmt.Println("Failed to create playback", err)
		return
	}

	if err := inDialog.Answer(); err != nil {
		fmt.Println("Failed to answer", err)
		return
	}

	if err := pb.Play(playfile, "audio/wav"); err != nil {
		fmt.Println("Playing failed", err)
	}
}
```

### Playback with control 

For more controling over audio playback
```go 
pb, err := inDialog.PlaybackControlCreate()

pb.Mute(true) // Mute call
pb.Mute(false) // Unmute call 
```

## Proxy media 

Accessing media (audio payload) for custom processing or proxying. 
This is something you will use for **Speech to Text**.

```go 
func ProxyMedia(inDialog *diago.DialogServerSession) {
	inDialog.Progress() // Progress -> 100 Trying
	inDialog.Ringing()  // Ringing -> 180 Response
	inDialog.Answer()   // Answqer -> 200 Response

	lastPrint := time.Now()
	pktsCount := 0
	buf := make([]byte, media.RTPBufSize)
	for {
		n, err := inDialog.Media().Read(buf)
		if err != nil {
			return
		}
        // Access to RTP Header If needed
        rtpHeader := inDialog.Media().RTPPacketReader.PacketHeader

        // Get Audio payload. Decode it, send to THIRD PARTY
        audioEncoded := buf[:n]

        pktsCount++
	}
}
```
# Bridging calls (B2BUA)

**Bridge** is entity that needs to be created before media can be bridged.

Normally you need to Dial + Bridge, but there is helper for this called `DialBridge()`

Example:
```go
var recipient = sip.Uri{User: "test", Host: "127.0.0.1", Port: 5090}

func BridgeCall(inDialog *diago.DialogServerSession) {
	inDialog.Progress() // Progress -> 100 Trying
	inDialog.Ringing()  // Ringing -> 180 Response
	inDialog.Answer()

	bridge := diago.NewBridge()
	// Add our leg into bridge
	if err := bridge.AddDialogSession(inDialog); err != nil {
		fmt.Println("Failed to add inDialog session", err)
		return
	}

	outDialog, err := d.DialBridge(ctx, , &bridge, sipgo.AnswerOptions{})
	if err != nil {
		fmt.Println("Failed to dial", err)
		return
	}
	defer outDialog.Close() // Always close, not have media hanging

	outCtx := outDialog.Context()
	defer func() {
		if err := outDialog.Hangup(outCtx); err != nil {
			fmt.Println("Failed to hangup", err)
		}
	}()

	// Wait for hangup of one of dialogs
	select {
	case <-inCtx.Done():
	case <-outCtx.Done():
	}
}

```


# Advanced

## Access SIP headers

```go
req := inDialog.InviteRequest
xheader := req.GetHeader("X-My-Header").Value()
```


## Access Media Session (SDP)

```go
sess := inDialog.Media().MediaSession

for _, s := range sess.Formats {
	fmt.Println(s) // ulaw, alaw
}
```

## License

This project is licensed under the BSD 2-Clause License. Each source file includes an SPDX license identifier to reference the license. For more details, see the LICENSE file.


## Support

If you find this project interesting for bigger support or consulting, you can contact me on
[mail](emirfreelance91@gmail.com)

There are many already valid solutions done with this lib.

For bugs features pls create [issue](https://github.com/emiago/sipgo/issues).