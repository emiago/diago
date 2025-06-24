---
title: API Docs
weight: 6
---


Library API tries to be well documented through comments.
For more reference visit [GO DOC](https://pkg.go.dev/github.com/emiago/diago)

Here we explain some of main built in concepts and usage.

## Dialog Sessions

diago can act as **UAS**(User Agent Server) and **UAC**(User Agent Client), and adds **bridging** capability to build B2BUA services.

It intentionally **distincts** dialog received (**Acting as server**) and dialog created (**Acting as client**):
- `DialogServerSession` when receving incoming dialog (SIP INVITE) and setups session (media)
- `DialogClientSession` when it creates outgoing dialog (SIP INVITE) and setups session (media) 

For best understanding here some docs with short code reference.

## Setup

Diago needs instance to be created `NewDiago` or in other words instance represents single UserAgent. 

With this instance you can serve multiple incoming dialogs or dial destinations in parallel. 

```go
ua, _ := sipgo.NewUA()
dg := diago.NewDiago(ua)
```

### Customize SIP Transport 
Diago allows you to customize transport listeners with `WithTransport`
Below example makes diago only listen for `TCP` SIP. 

```go
transportTCP := diago.Transport{
	Transport: "tcp",
	BindHost:  "127.0.0.1",
	BindPort:  5060,
}
dg := diago.NewDiago(ua,
	diago.WithTransport(transportTCP),
)
```

Transport support: UDP, TCP, TLS, WS, WSS

For more configuration checkout [github.com/emiago/diago#Transport](https://pkg.go.dev/github.com/emiago/diago#Transport)

## Incoming call

Calling `Serve` allows to serve every new call. Here you can build
you routing by accessing dialog. Some of helpers are added
- `ToUser` - destination callerID
- `FromUser` - from callerID
- `Transport` - transport of SIP message

See example below.



```go

dg.Serve(ctx, func(inDialog *diago.DialogServerSession) {
	// - Do your call routing.
	switch inDialog.ToUser() {
		case "play"
			// PlayFile(inDialog)
		case "answer":
			// Answer(inDialog)
		case "123456"
	}

	// inDialog is scope limited, exiting this routine will Close dialog.
    // Use Context to hold dialog in routine
	<-inDialog.Context().Done()
})
```

---

**NOTE:**
Dialog created is scoped (Like HTTP request serving). Once dialog exists, it is cleaned up, so no further action is needed.  

---

## Outgoing call

`Invite` sends SIP INVITE and **waits for answer**. After succesfull answer you can apply many other actions for dialog.
```go
dialog, err := dg.Invite(ctx, recipient sip.Uri, opts diago.InviteOptions)
if err != nil {
	// Handle err. Special ErrDialogResponse is returned if non 200 is received
}
defer dialog.Close() // Closing outgoing dialog is needed

// Do something

// Hangup 
dialog.Hangup()
```

For more controled dialog handling, checkout out `NewDialog` which allows you to create dialog and media stack 
before doing INVITE. 

## Answering call


```go
func Answer(inDialog *diago.DialogServerSession) {
	inDialog.Trying() // Trying -> 100 Trying
	inDialog.Ringing()  // Ringing -> 180 Response

	if err := inDialog.Answer(); err != nil {
		fmt.Println("Failed to answer", err)
		return
	}

	ctx := inDialog.Context()
	select {
	case <-ctx.Done():
		// Callee hangup
	case <-time.After(1 * time.Minute):
		// Caller hangup
		inDialog.Hangup(ctx)
	}
}
```

## Media handling

Every session comes with 2 streams (Audio for now). In diago case it is referenced as reader/writer. 
- `AudioReader` reads incoming stream
- `AudioWriter` writes outgoing stream 

Normally you mostly deal with writing audio so **Playback** is created for easier dealing with audio streams. 

**NOTE**: Diago does not automatically reads audio stream in background. This can happen with explicit call or *bridging*. 

## Playback

Playing audio file is done with audio playback. Library provides prebuilt playback functionality 

Playback can:
- Play File (wav/PCM)
- Play any stream (no encoders)


Either outgoing or incoming after leg is answered you can 
create playback

```go
func PlayFile(dialog *diago.DialogServerSession) {
	// NOTE: It is expected that dialog.Answer() is called

	playFile, err := os.Open("myaudiofile.wav")
	if err != nil {
		return err
	}
	defer playFile.Close()

	pb, err := dialog.PlaybackCreate()
	if err != nil {
		fmt.Println("Failed to create playback", err)
		return
	}

	if err := pb.Play(playfile, "audio/wav"); err != nil {
		fmt.Println("Playing failed", err)
	}
}
```

### Playback with control

If you need to control your playback like `Mute` `Unmute` or just to `Stop` current playback, then you can use `AudioPlaybackControl`

```go
func PlayFileControled(dialog *diago.DialogClientSession) {
	// NOTE: It is expected that dialog.Answer() is called

 	pb, err := dialog.PlaybackControlCreate()
	if err != nil {
		fmt.Println("Failed to create playback", err)
		return
	}

	go func() {
		playFile, err := os.Open("myaudiofile.wav")
		if err != nil {
			return err
		}
		defer playFile.Close()
		
		pb.Play(playfile, "audio/wav") // Note needs error handling
	}

	pb.Mute(true) // Mute/Unmute audio
	pb.Stop() // Stop playing audio. This will make Play exit
}
```

## Recording 

Diago exposes audio recording object which can be used in audio pipeline. Recording is only there once you 
**start reading or writing** to the stream.

```go 
// Create wav file to store recording
func Record(dialog *diago.DialogServerSession) {
	// NOTE: It is expected that dialog.Answer() is called

	wawFile, err := os.OpenFile("myrecording.wav", os.O_RDWR|os.O_CREATE, 0755)
	if err != nil {
		return err
	}
	defer wawFile.Close()

	// Create recording audio pipeline
	rec, err := inDialog.AudioStereoRecordingCreate(wawFile)
	if err != nil {
		return err
	}
	// Must be closed for correct flushing
	defer func() {
		if err := rec.Close(); err != nil {
			slog.Error("Failed to close recording", "error", err)
		}
	}()
	
	// Do echo until call is hanguped
	media.Copy(rec.AudioReader(), rec.AudioWriter())
}
```

## Early Media

Many systems use early media to announce some activity, but not actually signaling call being answered.

```go
func PlayFile(dialog *diago.DialogServerSession) {
	if err := dialog.ProgressMedia(); err != nil {
		fmt.Println("Failed to start early", err)
		return
	}
	// Now media is already open and we can start playing file
	
	pb, err := dialog.PlaybackCreate()
	if err != nil {
		fmt.Println("Failed to create playback", err)
		return
	}

	if err := pb.PlayUrl("https://mycoolaudio.wav"); err != nil {
		fmt.Println("Playing failed", err)
	}

	// Continue with answering call
	dialog.Answer()
}
```

## Bridge

Bridge allows bridging your **Dialog Sessions**. If dialogs 
are answered and have media sessions you can bridge them.

```go
bridge := diago.NewBridge()
bridge.AddDialogSession(d1)
bridge.AddDialogSession(d2)
```

For now bridge is only doing proxy of RTP and it does not allow Transcoding, although lib supports transcoding.

> Transcoding is generally something you do not want in running system so bridge will return error in case codecs are missmatch.
> If needed it will be exposed more as special thing.

