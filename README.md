# Diago

Short from **dialog** + **go** is library (framework) to create b2b call services.

If you know services like Asterisk,FreeSwitch this is all about.

# Specs

- **Endpoint** - is transport layer on which new dialog and media is received or sent out
- **Dialog** - provides interface to control a call leg. Call leg can be incoming or outgoing.
- **Bridge** - connects media path between 2 or more dialogs