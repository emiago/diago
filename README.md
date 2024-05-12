# Diago

Short from **dialog** + **go** is library (framework) to create media services for calls over SIP/RTP, with reduced abstraction and focus on media control.

If you know services like Asterisk,FreeSwitch this is all about

# Specs

- **Endpoint** - is transport layer on which new dialog and media is received or sent out
- **DialogSession** - provides interface to control a call leg. Call leg can be incoming or outgoing.
    - **DialogMedia** - provides interface to control dialog media. It is part of dialog Session
- **Bridge** - connects media path between 2 or more dialogs
- **Playback** - 



# Features:

- Progress, Ringing, Answer response
- Playback files
- Playback URL 
    - audio/wav with 16 bit PCM 8000 HZ
    - Streaming with HTTP Ranges
- [ ] Hold Unhold
- [ ] Mute Unmute
- [ ] Recording



# Concepts




## Dial uses optional Bridge to add participant in order to avoid early media issue

```go
bridge := diago.NewBridge()
// If this already has originator (dialog) then add to bridge
if dialog != nil {
    bridge.AddDialogSession(dialog)
}

dialog, err := endpoint.Dial(ctx, recipient, &bridge, sipgo.AnswerOptions{})
if err != nil {
    return err
}
```