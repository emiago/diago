---
title: Audio Pipelines
weight: 7
---


Everything is io.Reader and io.Writer

Diago follows GO std lib and providing interface for Reader/Writer when it comes reading and writing media.
This made easier usage of RTP framework and optimizations, by providing end user standard library io.Reader io.Writer to pass his media.

In other words creating reader or writer **pipelines** allows to build interceptors, encoders, decoders without introducing overhead of contention and buffer reuse. 

`media` and `audio` package also provide all this helpers for making audio pipelines straightforward. 
In **realtime media**  streaming, audio is sampled at constant rate.  
Some of io operations you encounter have helpers `media` package like `media.Copy` , `media.ReadAll`, which are better suited instead std `io`

### Creating Echo
Everything mostly begins by getting actual `AudioReader` and `AudioWriter` from Dialog Session. 

```go
ar, _ := dialog.AudioReader()
aw, _ := dialog.AudioWriter()
```

Now you can use this helpers
```go
n, err := media.Copy(ar, aw) // It creates buffer of media.RTPBufSize 
```
For less memory alloc, better option is `media.CopyWithBuf` where controlling buffer reuse can be made.


### More audio processing

For more **audio** processing `audio` (ex **transcoding**, **wav streaming**) package provides you with prebuilt helpers for pipelining 
More you can find here https://pkg.go.dev/github.com/emiago/diago/audio 
