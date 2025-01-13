<img src="icons/diago-text.png" width="300" alt="DIAGO">

[![Go Report Card](https://goreportcard.com/badge/github.com/emiago/diago)](https://goreportcard.com/report/github.com/emiago/diago)
![Coverage](https://img.shields.io/badge/coverage-61.1%25-blue)
![GitHub go.mod Go version](https://img.shields.io/github/go-mod/go-version/emiago/diago)

Short of **dialog + GO**.  
**Library for building VOIP solutions in GO!**

Built on top of optimized [SIPgo library]((https://emiago.github.io/diago))!  
In short it allows developing fast and easy testable VOIP apps to handle calls, registrations and more... 

*Diago is mainly project driven lib, so lot of API design will/should be challenged with real working apps needs*

**For more information and documentation visit [the website](https://emiago.github.io/diago/docs)**

Quick links:
- [Getting started](https://emiago.github.io/diago/docs/getting_started/)
- [Demo Examples](https://emiago.github.io/diago/docs/examples/)
- [API Docs](https://emiago.github.io/diago/docs/api_docs/)
- [GO Docs](https://pkg.go.dev/github.com/emiago/diago)

*If you find this project useful and you want to support/sponzor or need help with your projects, you can contact me more on*
[mail](mailto:emirfreelance91@gmail.com).

Follow me on [X/Twitter](https://twitter.com/emiago123) for regular updates

## Contributions
Please open first issues instead PRs. Library is under development and could not have latest code pushed.


## Usage 

Checkout more on [Getting started](https://emiago.github.io/diago/docs/getting_started/), but for quick view here is echotest (hello world) example.
```go 
ua, _ := sipgo.NewUA()
dg := diago.NewDiago(ua)

dg.Serve(ctx, func(inDialog *diago.DialogServerSession) {
	inDialog.Progress() // Progress -> 100 Trying
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

See more [examples in this repo](/examples)
### Tracing SIP, RTP

While openning issue, consider having some traces enabled.

```go
sip.SIPDebug = true // Enables SIP tracing
media.RTCPDebug = true // Enables RTCP tracing
media.RTPDebug = true // Enables RTP tracing. NOTE: It will dump every RTP Packet
```
