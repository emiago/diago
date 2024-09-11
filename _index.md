---
# title: Diago
---
<img src="diago-text-no-icon.png" width="300" alt="DIAGO">

---
**Diago** is short of **dialog + GO**.  
Library for building comunication (VOIP) solutions in *GO*.


It is providing more developer friendly solution on top of [SIPgo](https://github.com/emiago/sipgo) to develop any kind of media streaming (VOIP) services.
It comes with own media stack to provide optimized handling of media(RTP framework) in Go language.

**Stack:**
* Signaling: SIP
* Media: RTP/AVP

Visit [Documentation](docs/) to find out more!

You can follow on [X/Twitter](https://twitter.com/emiago123) for more updates.


**NOTE: This API design is WIP and may have changes**

## What is really about?

If you are familiar with terms
*Calling, Bridging, Conferencing, IVR, Recording, Transcribing, Voicemail ...* that is all about.

Using GO it offers faster way of **developing** and **testing** communication services, with main focus on building voice services over IP. 

## Core (Roadmap):

***WIP*** = Work in progress (Expect soon to be part of lib)

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
- [ ] Handling transfers (Refers) (WIP)
- [ ] Conferencing audio
- [ ] DTMF with SIP INFO (Needed more in case webrtc)
- [ ] Writing Unit Test on Server with SIP and Media Recorder (***WIP***)
- [ ] RTP symetric
- [ ] SRTP for more critical services
- [ ] And plenty more ...


## diagox 

**Diago extra modules**

Some of modules are not yet considered to be part of lib and they are developed for private projects. To mention few: 
- Recording
- Webrtc(pion) stack over diago's media stack
- Complex modules etc...

If you have interest or need consulting/sponsoring please contact me on [mail](mailto:emirfreelance91@gmail.com)


