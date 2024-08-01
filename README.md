# Diago

Short of **dialog + GO**.  
New framework for building comunication (VOIP) solutions in GO. 

Stack: 
- Signaling: SIP
- Media: RTP/UDP, Webrtc

**NOTE: This API design is not FINAL**

# Getting started

diago acts as UAS(User Agent Server) and UAC(User Agent Client). For now it keeps abstractions only where it needs.
Therefore it distincts your dialog control by:
- It receives `DialogServerSession` for serving incoming sessions(Call leg)
- It creates `DialogClientSession` when it Dials outgoing session(Call leg)

Further it is explicit which media stack it uses
- `Answer` -> RTP/UDP
- `AnswerWebrtc` -> Webrtc stack SRTP/DTLS


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


#### Handle incoming
```go
d.Serve(ctx, func(inDialog *diago.DialogServerSession) {
	// - Do your call routing.
	switch inDialog.ToUser() {
		case "answer":
		case "123456"
	}
})
```

#### Do outgoing

```go
d.Dial(ctx, recipient sip.Uri, bridge *Bridge, opts sipgo.AnswerOptions) 
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

## Direct media 

Accessing media (audio payload) for custom processing or proxying. 
Consider this when using Speech to Text is required.

```go 
func DirectMedia(inDialog *diago.DialogServerSession) {
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