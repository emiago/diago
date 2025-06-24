---
# title: My Docs
cascade:
  type: docs
---

Welcome to diago documentation!.



## What is Diago?

If you are familiar with terms
*Calling, Bridging, Conferencing, IVR, Recording, Transcribing, Voicemail ...* that is all about.

Developing this kind of services can be challenging when it needs more behavior: monitoring, media control, integrations, databases etc...

Diago with GO offers faster way of **developing** and **testing** communication services, but keeping care on providing **low latency**. 

More on [Why Diago](why_diago)


## Core (Roadmap):

- [x] Full dialog control and High Level API
- [x] alaw,ulaw codecs (opus as third is planned as well)
- [x] Audio package for streaming: WAV reader/writer, PCM transcoding to alaw/ulaw
- [x] Playbacks as buffers,files(wav),url
- [x] Playback URL streaming
- [x] Playback with control mute/unmute
- [x] Audio Reader/Writer stream exposed for manual processing like sending to third party
- [x] DTMF with RTP
- [x] Handling Reinvites with media updates
- [x] Bridging as proxy media for 2 parties B2BUA
- [x] Opus codec support
- [x] Handling blind transfers (Refers)
- [ ] Handling attended transfers
- [ ] Handle Anonymous Trust Domain PAI handling (rfc3325) **Partially Done**
- [ ] Conferencing audio
- [ ] DTMF with SIP INFO (Needed more in case webrtc)
- [ ] Writing Unit Test on Server with SIP and Media Recorder
- [ ] RTP symetric
- [ ] SRTP for more critical services
- [x] Simple Wav Stereo recording
- [ ] Webrtc as media stack (integration with pion) **Experimental**
- [ ] Full IPV6 support (sipgo work) **Supported but has corner cases**
- [ ] And plenty more ...

If you want support/sponzor current development roadmap or you want to prioritize different contact me on [mail](mailto:emirfreelance91@gmail.com)

## Diago extra modules

Some of modules are not yet considered to be part of lib and they are developed/consulted for private projects. To mention few: 
- Recording
- Webrtc(pion) stack over diago's media stack
- Complex modules etc...

**NEXT**: [-> Guides](guides)

