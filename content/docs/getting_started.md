---
# title: Getting started
weight: 5
---

## Getting started

As showcase, code below is only needed to start serving Calls.
In this example Call will be answered and played some audio.

For testing you can use [gophone CLI sofpthone](https://github.com/emiago/gophone) built with same libraries or 
any other SIP softphone.

## Echo test app

Copy audio file from library `testdata/files/demo-echotest.wav`
or change to whatever you want

```go
ua, _ := sipgo.NewUA()
dg := diago.NewDiago(ua)

dg.Serve(ctx, func(inDialog *diago.DialogServerSession) {
	inDialog.Trying() // Trying
	inDialog.Answer(); // Answer

	// Make sure file below exists in work dir
	playfile, err := os.Open("demo-echotest.wav")
	if err != nil {
		fmt.Println("Failed to open file", err)
		return
	}
	defer playfile.Close()

	// Create playback and play file.
	pb, _ := inDialog.PlaybackCreate()
	if err := pb.Play(playfile, "audio/wav"); err != nil {
		fmt.Println("Playing failed", err)
	}
}
```


Dial in with softphone on `127.0.0.1:5060` and you should here audio playing.

With gophone:
```
gophone dial -media=speaker sip:111@127.0.0.1
```