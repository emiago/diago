---
# title: Getting started
weight: 20
---


Diago for now is only built for audio processing to target VOIP needs, but it may support video as well.

### SDP negotiation

You can control codecs support with media conf and there order.
Checkout for `diago.MediaConfig` which can be passed on creating diago.

Supported codecs:
- PCMU (ulaw)
- PCMA (alaw)
- opus

### Opus

Library uses opus C binding and it is not enabled by default
You need to have installed opus development files before compiling.

Example for linux:

Ubuntu:
```bash
sudo apt-get install pkg-config libopus-dev libopusfile-dev
```
Fedora:
```bash
sudo dnf install opus-devel opusfile-devel
```

#### Opus compile 

To enable opus compile you need to place build tags. 
 
```bash 
go build -tags with_opus_c .
```

Make sure you have enabled opus with `diago.MediaConfig` and passing `media.CodecAudioOpus`. Example:
```go
diago.MediaConfig{
    Codecs: []string(media.CodecAudioOpus, media.CodecAudioAlaw),
}
```

For more on how to compile checkout this package https://github.com/hraban/opus