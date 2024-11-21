# media

Implemented:
- [DTMF EVENT RFC2833](https://datatracker.ietf.org/doc/html/rfc2833)



Everything is `io.Reader` and `io.Writer`

We follow GO std lib and providing interface for Reader/Writer when it comes reading and writing media.   
This optimized and made easier usage of RTP framework, by providing end user standard library `io.Reader` `io.Writer`
to pass his media.

In other words chaining reader or writer allows to build interceptors, encoders, decoders without introducing 
overhead of contention or many memory allocations

Features:
- [x] Simple SDP build with formats alaw,ulaw,dtmf
- [x] RTP/RTCP receiving and logging
- [x] Extendable MediaSession handling for RTP/RTCP handling (ex microphone,speaker)
- [x] DTMF encoder, decoder via RFC4733
- [x] Minimal SDP package for audio
- [x] Media Session, RTP Session handling
- [x] RTCP monitoring
- [ ] SDP codec fields manipulating
- [ ] ... who knows

## Concepts

- **Media Session** represents mapping between SDP media description and creates session based on local/remote addr
- **RTP Session** is creating RTP/RTCP session. It is using media session underneath to add networking layer.
- **RTP Packet Reader** is depackatizing RTP packets and providing payload as `io.Reader`. Normally it should be chained to RTP Session 
- **RTP Packet Writer** is packatizing payload to RTP packets as `io.Writer`. Normally it should be chained to RTP Session

## IO flow

Reader:
`AudioDecoder<->RTPPacketReader<->RTPSession<->MediaSession`

Writer:
`AudioEncoder<->RTPPackerWriter<->RTPSession<->MediaSession`


### more docs...