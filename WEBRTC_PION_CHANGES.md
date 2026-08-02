# Media API Changes and Dialog Decoupling

This document explains the current media API direction in Diago. The main change
is that SIP dialog sessions and media stacks are now separate objects.

`DialogClientSession` and `DialogServerSession` represent SIP dialog signaling.
`DialogMedia` and `DialogWebrtc` represent media handling. New APIs return media
objects explicitly, and application code should keep and use those returned media
objects instead of asking the dialog session for media later.

## What Changed

Before this refactor, code could treat a dialog session as if it also owned the
media stack. That made media updates, REFER handling, re-INVITE, WebRTC, and
forked calls harder to reason about because SIP session objects carried direct
media references.

The new model is:

- SIP sessions handle SIP signaling, dialog state, requests, responses, BYE,
  REFER, re-INVITE, hold, and remote target changes.
- Media stacks handle SDP/media negotiation, audio reader/writer APIs, playback,
  recording, RTP/WebRTC state, and media updates.
- SIP sessions do not expose media through `d.Media()` as the main API.
- APIs that create media return it directly.
- Callers are responsible for closing the returned dialog and media objects they
  own.

## Public API Differences

### Outgoing calls

`DialogClientSession.Invite` returns media and still expects the caller to ACK
when using the lower-level dialog API.

```go
dialog, err := dg.NewDialog(recipient, diago.NewDialogOptions{})
if err != nil {
	return err
}
defer dialog.Close()

med, err := dialog.Invite(ctx, diago.InviteClientOptions{})
if err != nil {
	return err
}
defer med.Close()

if err := dialog.Ack(ctx); err != nil {
	return err
}
```

If early media detection is enabled, keep the returned media and pass it into
`WaitAnswer`.

```go
med, err := dialog.Invite(ctx, diago.InviteClientOptions{
	EarlyMediaDetect: true,
})
if !errors.Is(err, diago.ErrClientEarlyMedia) {
	return err
}
defer med.Close()

if err := dialog.WaitAnswer(ctx, med, sipgo.AnswerOptions{}); err != nil {
	return err
}
if err := dialog.Ack(ctx); err != nil {
	return err
}
```

### Incoming calls

`DialogServerSession.Answer` creates and returns a new `DialogMedia`.

```go
func handleCall(inDialog *diago.DialogServerSession) {
	if err := inDialog.Trying(); err != nil {
		return
	}

	med, err := inDialog.Answer(diago.AnswerOptions{})
	if err != nil {
		return
	}
	defer med.Close()

	r, err := med.AudioReader()
	if err != nil {
		return
	}
	_ = r
}
```

`Answer` no longer checks hidden existing media. It always creates the answer
media stack for that answer call.

### Early media

Early media is explicit. `ProgressMedia` creates and returns early media. If
that same media stack should become the final answered media, call
`AnswerEarlyMedia` with the returned media.

```go
earlyMed, err := inDialog.ProgressMedia(diago.ProgressMediaOptions{})
if err != nil {
	return err
}
defer earlyMed.Close()

w, err := earlyMed.AudioWriter()
if err != nil {
	return err
}
_ = w

if err := inDialog.AnswerEarlyMedia(earlyMed, diago.AnswerOptions{}); err != nil {
	return err
}
```

Do not call `Answer` after `ProgressMedia` when the intent is to finalize the
same early media stack. Calling `Answer` creates a separate new media stack.

### WebRTC

WebRTC follows the same returned-media model. `InviteWebrtc` and
`AnswerWebrtc` use Diago's direct WebRTC media stack and return
`*DialogWebrtc`.

The direct stack performs non-trickle ICE gathering, ICE connectivity checks,
DTLS negotiation, and SRTP/SRTCP setup. The call methods do not return usable
media until this setup is complete and RTP monitoring has started. The context
passed to `InviteWebrtc` controls how long the outgoing setup may block;
`AnswerWebrtc` uses the dialog context.

```go
webMed, err := dialog.InviteWebrtc(ctx, diago.InviteWebrtcOptions{})
if err != nil {
	return err
}
defer webMed.Close()
```

```go
webMed, err := inDialog.AnswerWebrtc(diago.AnswerWebrtcOptions{})
if err != nil {
	return err
}
defer webMed.Close()
```

ICE is UDP-only. Its IP family, candidate policy, address filters, port range,
and liveness timeouts are configured through
`media.MediaSessionWebrtcConfig`. The optional ICE state callback is installed
while media is initialized and must not block.

```go
webMed, err := dialog.InviteWebrtc(ctx, diago.InviteWebrtcOptions{
	WebrtcConfig: media.MediaSessionWebrtcConfig{
		IPFamilies:     []media.ICEIPFamily{media.ICEIPFamilyIPv4},
		CandidateTypes: []media.ICECandidateType{media.ICECandidateHost},
		IPFilter:        localIPFilter,
		RemoteIPFilter:  remoteIPFilter,
	},
	OnICEStateChange: func(state ice.ConnectionState) {
		// Observe state without blocking the ICE callback.
	},
})
```

When `EarlyMediaDetect` is enabled, retain the returned `*DialogWebrtc` after
`ErrClientEarlyMedia` and pass it to `WaitAnswerWebrtc` to complete the SIP
answer.

A Pion PeerConnection-backed variant remains available through the explicitly
suffixed `InviteWebrtcPion`, `AnswerWebrtcPion`, and `DialogWebrtcPion` APIs.

### Bridging

Bridges operate on returned media objects.

```go
dialog, med, err := dg.Invite(ctx, recipient, diago.InviteOptions{})
if err != nil {
	return err
}
defer dialog.Close()
defer med.Close()

if err := bridge.AddDialogMedia(med); err != nil {
	return err
}
```

For WebRTC, use the returned `*DialogWebrtc` with the WebRTC bridge method.

```go
webMed, err := dialog.InviteWebrtc(ctx, diago.InviteWebrtcOptions{})
if err != nil {
	return err
}
defer webMed.Close()

if err := bridge.AddDialogWebrtc(webMed); err != nil {
	return err
}
```

## Dialog vs Media Ownership

`DialogClientSession` and `DialogServerSession` are SIP dialog sessions. They
own SIP operations such as:

- sending and receiving SIP requests and responses;
- maintaining dialog state;
- tracking remote contact target changes;
- handling ACK, BYE, REFER, NOTIFY, and re-INVITE;
- exposing `Hangup`, `Close`, `Do`, and SIP dialog access.

`DialogMedia` owns RTP media operations such as:

- SDP generation and application for RTP media;
- RTP session setup and update;
- audio reader/writer access;
- playback, recording, DTMF, echo, and RTP media updates.

`DialogWebrtc` owns WebRTC media operations such as:

- WebRTC SDP offer/answer application;
- ICE and DTLS-SRTP transport setup;
- encrypted RTP and RTCP over the selected ICE candidate pair;
- RTP packet reader/writer access;
- WebRTC playback, recording, DTMF, and echo handling.

The important rule is: once an API returns media, use that media object for media
operations. Do not depend on the dialog session to find the media stack later.

## Migration Checklist

- Replace `d.Media()` usage with the media object returned by `Invite`,
  `Answer`, `ProgressMedia`, `InviteWebrtc`, or `AnswerWebrtc`.
- Update outgoing helper calls to capture both dialog and media:
  `dialog, med, err := dg.Invite(...)`.
- Update manual outgoing calls to capture media:
  `med, err := dialog.Invite(...)`.
- Pass media into `WaitAnswer(ctx, med, opts)` after early media detection.
- Replace `ProgressMedia` followed by `Answer` with
  `AnswerEarlyMedia(earlyMed, opts)` when finalizing the same early media.
- Move playback, recording, bridge, audio reader/writer, and DTMF calls to the
  returned media object.
- Add `defer med.Close()` and `defer dialog.Close()` where the caller owns those
  objects.
- For WebRTC, use `*DialogWebrtc` returned by `InviteWebrtc` or `AnswerWebrtc`.
- Configure direct WebRTC transport policy through
  `media.MediaSessionWebrtcConfig` on the Invite or Answer options.
- If code specifically requires the Pion PeerConnection implementation, use
  the APIs suffixed with `Pion`.

## Notes for Maintainers

The direction of the API is to keep SIP sessions independent from media stack
implementations. New media behavior should be implemented by registering or
extending media callbacks, not by adding direct media pointers back into
`DialogClientSession` or `DialogServerSession`.

When adding a new media stack or changing re-INVITE behavior:

- keep SIP request/response handling in dialog session code;
- keep SDP/media mutation inside the media stack;
- use callbacks for signaling-to-media coordination;
- avoid returning SIP objects from media stack functions;
- return SDP bodies or media errors from media logic and let SIP code build SIP
  responses.
