# Diago

Short from **dialog** + **go** is library (framework) to create media services for calls over SIP/RTP, with reduced abstraction and focus on media control.

If you know services like Asterisk,FreeSwitch this is all about

# Specs

- **Endpoint** - is transport layer on which new dialog and media is received or sent out
- **DialogSession** - provides interface to control a call leg. Call leg can be incoming or outgoing.
- **Bridge** - connects media path between 2 or more dialogs