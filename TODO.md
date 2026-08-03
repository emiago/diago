# Direct WebRTC / SIP TODO

This document tracks the production-hardening work for the audio-only direct
WebRTC implementation in `media.MediaSessionWebrtc`, its RTP/RTCP transport,
and the surrounding SIP dialog integration.

The intended scope remains deliberately narrow: one audio media section,
DTLS-SRTP, ICE, RTP/RTCP multiplexing, and SIP offer/answer. Items below are
ordered roughly by risk and dependency, not by implementation difficulty.

## P0 — Security and transport correctness

### Enable SRTP and SRTCP replay protection

The raw Pion SRTP contexts created by `MediaSessionWebrtc.Finalize` do not
enable replay protection by default.

- [ ] Enable inbound SRTP replay protection with a window of at least 64
  packets.
- [ ] Enable inbound SRTCP replay protection.
- [ ] Decide whether replay-window sizes are fixed safe defaults or advanced
  transport-level configuration; do not expose a disable switch in ordinary
  per-call configuration.
- [ ] Add tests proving duplicate and too-old RTP and RTCP packets are rejected.
- [ ] Add sequence-number wraparound and out-of-order packet tests.

References: [RFC 3711](https://www.rfc-editor.org/rfc/rfc3711),
[Pion SRTP](https://pkg.go.dev/github.com/pion/srtp/v3).

### Make SRTP contexts safe under concurrent RTP and RTCP traffic

The same outbound SRTP context is currently used by RTP writes and the RTCP
monitor, while the same inbound context is used by RTP reads and RTCP reads.
Pion's raw `srtp.Context` does not provide concurrency protection.

- [x] Give RTP and RTCP independent raw contexts derived from the same DTLS-SRTP
  key material. This avoids cross-protocol context sharing without hot-path
  locking; SRTP and SRTCP retain their separate index spaces.
- [ ] Add a sustained, bidirectional RTP+RTCP race test.
- [ ] Run the test under `go test -race` and with multiple SSRCs.

### Fix RTP/RTCP mux classification

The current second-byte classifier can treat marker-set RTP packets using
payload types 64–95 as RTCP.

- [ ] Implement standards-compliant RTP/RTCP demultiplexing, or reject payload
  types that cannot be safely used with the selected classifier.
- [ ] Add table tests covering the RTP/RTCP byte ranges and marker bit.
- [ ] Add an end-to-end test using payload types in the 64–95 range.

References: [RFC 5761](https://www.rfc-editor.org/rfc/rfc5761),
[RFC 7983](https://www.rfc-editor.org/rfc/rfc7983).

### Correct negotiated DTMF payload handling

The dialog-level DTMF reader/writer currently assumes telephone-event payload
type 101 even though SDP negotiation may select a different dynamic payload
type.

- [ ] Resolve telephone-event from the session's negotiated codecs.
- [ ] Use the negotiated payload type for both DTMF reading and writing.
- [ ] Reject DTMF operations clearly when telephone-event was not negotiated.
- [ ] Test non-101 payload types and offers without telephone-event.

### Validate SDP before changing live transport state

`RemoteSDP` can create or start ICE work before all DTLS and media attributes
have been validated, leaving a partially initialized transport after an error.

- [ ] Parse and validate the complete remote description before mutating the
  active session.
- [ ] Validate ICE credentials, candidates, DTLS setup role, fingerprints,
  media direction, `rtcp-mux`, codec intersection, and trickle generation as a
  single transaction.
- [ ] On failure, leave the previous valid transport usable.
- [ ] Add malformed-SDP and partial-failure tests with leak detection.

## P0 — Lifecycle and concurrency

### Introduce an explicit session state machine

Use an explicit lifecycle instead of inferring state from nullable fields and
booleans:

`new -> gathering -> remote-set -> connecting -> ready -> closed`

- [ ] Define allowed operations and transitions for `Init`, `LocalSDP`,
  `RemoteSDP`, `Finalize`, media reads/writes, and `Close`.
- [ ] Make invalid and repeated transitions return stable, documented errors.
- [ ] Prevent concurrent `Init` and `Finalize` execution.
- [ ] Prevent `Init` after `Close` from creating an ICE agent that will never be
  closed.
- [ ] Make `Close` idempotent and clear ready/connection status consistently.
- [ ] Ensure all setup failures roll back sockets, goroutines, ICE agents,
  DTLS state, and packet buffers.
- [ ] Add close-during-gather, close-during-connect, close-during-DTLS, and
  concurrent-close tests under the race detector.

### Do not hold lifecycle locks across network I/O

RTP writing currently holds the session mutex through encryption and socket
write. A blocked TURN/TCP path can consequently delay `Close` and other state
changes.

- [x] Snapshot immutable connection/context pointers under the lifecycle lock,
  then release it before packet encryption and network I/O.
- [x] Keep RTP serialization at the existing `RTPPacketReader`/`RTPPacketWriter`
  ownership boundary and recreate sessions during fork updates.
- [ ] Define read and write deadlines independently.
- [ ] Make `StopRTP` interrupt both blocked reads and blocked writes, or rename
  and document its narrower behavior.
- [ ] Test blocked writes over a simulated slow connection while closing.

### Fix the related Pion peer-connection SDP race

The Pion-backed `mediawebrtc.PeerConnection.RemoteSDP` path calls
`SetRemoteDescription` and then reparses Pion's internal remote description
while Pion operations may still be updating it.

- [ ] Parse a private copy of the original remote SDP bytes instead of reading
  Pion's mutable internal description.
- [ ] Add a focused race regression test.
- [ ] Re-run the broader Pion dialog tests under `-race`.

## P1 — SIP offer/answer and renegotiation

### Add direct-WebRTC re-INVITE support

Direct WebRTC dialogs currently do not install the remote/local SDP and
finalization callbacks used by the Pion-backed implementation, and
`MediaSessionWebrtc.RemoteSDP` rejects a second description once ICE is active.

- [x] Register re-INVITE callbacks for direct WebRTC dialogs.
- [ ] Support hold/resume and media-direction changes.
- [ ] Support codec and telephone-event renegotiation.
- [ ] Keep a stable SDP `o=` session ID and increment the session version for
  each locally generated offer or answer.
- [x] Treat unchanged ICE credentials as an update to the current generation.
- [x] Treat changed ICE username fragment/password as an ICE restart.
- [x] For an ICE restart, build a pending transport while the previous media
  path remains live, then atomically swap after successful validation and
  finalization.
- [x] Roll back cleanly if the replacement path fails.
- [ ] Reject stale candidates and SDP fragments from earlier ICE generations.
- [ ] Cover local and remote re-INVITEs, glare, hold/resume, restart success,
  restart failure, and rollback.

### Handle final SDP after early media

The direct path can finalize media on a `183` response, while the final `200`
response may contain a different SDP that is currently not applied.

- [ ] Compare the final answer with the provisional answer.
- [ ] Continue unchanged media when the descriptions are equivalent.
- [ ] Apply a valid replacement through the renegotiation path when permitted.
- [ ] Reject or terminate clearly when the final answer is incompatible.
- [ ] Add tests for `183 -> 200` with unchanged SDP, changed candidates,
  changed codecs, and changed ICE credentials.
- [ ] Cover reliable provisional responses and PRACK (`100rel`).

References: [RFC 3261](https://www.rfc-editor.org/rfc/rfc3261),
[RFC 3262](https://www.rfc-editor.org/rfc/rfc3262),
[RFC 3264](https://www.rfc-editor.org/rfc/rfc3264).

## P1 — Trickle ICE over SIP

Trickle ICE is possible with SIP, but it is a signaling feature as well as a
media feature. Implement it according to RFC 8840 rather than adding only a
candidate callback to `MediaSessionWebrtc`.

### Media-session API

- [ ] Split ICE startup from gathering completion so SDP generation does not
  always block on full STUN/TURN gathering.
- [ ] Add `OnLocalCandidate` (including end-of-candidates).
- [ ] Add `AddRemoteCandidate` and remote end-of-candidates handling.
- [ ] Associate every candidate operation with its ICE generation
  (`ice-ufrag`/`ice-pwd`).
- [ ] Expose gathering state and a safe local-description snapshot.
- [ ] Retain `WaitGatheringComplete` for non-trickle fallback.
- [ ] Support both initial gathering and ICE restarts.

### SIP signaling adapter

- [ ] Add SIP option-tag negotiation with `Supported: trickle-ice`.
- [ ] Advertise `a=ice-options:ice2 trickle` only when trickling is supported;
  advertise at least `ice2` for the RFC 8445 ICE path.
- [ ] Implement the `trickle-ice` INFO package using
  `application/trickle-ice-sdpfrag`.
- [ ] Route INFO requests correctly within early and confirmed dialogs.
- [ ] Handle cumulative SDP fragments, duplicate delivery, retransmission,
  reordering, and end-of-candidates.
- [ ] Permit at most one outstanding INFO request per dialog and aggregate
  candidates when SIP transport reliability or latency requires it.
- [ ] Keep candidate state separate for each early-dialog fork and remote
  target.
- [ ] Integrate trickled candidates with `183`, PRACK, final answers, and ICE
  restarts.
- [ ] Do not accept zero-candidate SDP unless trickle support was successfully
  negotiated.

### Fallback policy

- [ ] Define `Disabled`, `Auto`, and `Required` trickle modes.
- [ ] In `Auto`, use full trickle only when peer support is known.
- [ ] When support is unknown, use a compatible half-trickle strategy with at
  least one usable candidate in the initial SDP.
- [ ] In `Required`, fail with a specific signaling error if the peer cannot
  trickle.
- [ ] Test peers with and without RFC 8840 support over SIP UDP, TCP, and TLS.

References: [RFC 8445](https://www.rfc-editor.org/rfc/rfc8445),
[RFC 8838](https://www.rfc-editor.org/rfc/rfc8838),
[RFC 8840](https://www.rfc-editor.org/rfc/rfc8840),
[RFC 8842](https://www.rfc-editor.org/rfc/rfc8842).

## P1 — ICE, DTLS, and connection observability

- [ ] Expose ICE gathering and connection-state changes without leaking Pion
  implementation types into ordinary application code.
- [ ] Expose selected candidate-pair changes and useful pair statistics.
- [ ] Report `gathering`, `connecting`, `connected`, `disconnected`, `failed`,
  and `closed` distinctly.
- [ ] Stop or fail media predictably after ICE consent expires, and retain the
  reason for diagnostics.
- [ ] Return complete local/remote socket addresses, including ports; prefer a
  structured candidate-pair result over address-only strings.
- [ ] Add counters for candidate gathering duration, ICE connection duration,
  DTLS handshake duration, selected candidate type/protocol, packet drops,
  replay drops, RTP/RTCP bytes, and close reason.
- [ ] Add hooks that can feed the project's logger and metrics implementation
  without logging every packet or buffer drop.
- [ ] Support multiple fingerprint attributes by selecting a supported secure
  algorithm; retaining SHA-256 as the mandatory baseline is acceptable.

References: [RFC 7675](https://www.rfc-editor.org/rfc/rfc7675),
[RFC 8122](https://www.rfc-editor.org/rfc/rfc8122),
[RFC 8827](https://www.rfc-editor.org/rfc/rfc8827).

## P1 — RTCP correctness and health monitoring

- [x] Track whether reduced-size RTCP was offered and negotiated.
- [x] Include `a=rtcp-rsize` in an answer only when it was offered and is
  supported.
- [x] When reduced-size RTCP is not negotiated, send compliant compound RTCP
  packets, including SDES CNAME with SR/RR traffic.
- [x] Replace the fixed, synchronized five-second ticker with a randomized
  interval around the configured mean, deliberately constrained to the
  single-audio-stream case.
- [ ] Send SRTCP BYE on orderly shutdown where it will not delay teardown.
- [ ] Add receiver-report/consent-driven circuit-breaker behavior so persistent
  congestion or media-path failure cannot transmit indefinitely.
- [ ] Test loss, reordering, jitter, sequence wrap, SSRC changes, sender/receiver
  reports, compound packets, reduced-size packets, and BYE.

Possible later extensions, if required by target peers:

- [ ] NACK and retransmission.
- [ ] Transport-wide congestion control or REMB.
- [ ] RTP header-extension negotiation.

References: [RFC 3550](https://www.rfc-editor.org/rfc/rfc3550),
[RFC 5506](https://www.rfc-editor.org/rfc/rfc5506),
[RFC 4585](https://www.rfc-editor.org/rfc/rfc4585),
[RFC 8083](https://www.rfc-editor.org/rfc/rfc8083),
[RFC 8835](https://www.rfc-editor.org/rfc/rfc8835).

## P1 — Production configuration and scaling

### Separate shared transport configuration from per-call policy

Create a shared `WebRTCTransport`/ICE-agent factory owned by `Diago` or a
transport instance. Keep `MediaSessionWebrtc` lightweight and give it only
session-specific overrides.

Shared process/transport configuration:

- [ ] ICE UDP and TCP port ranges.
- [ ] Shared ICE UDP mux and optional TCP mux.
- [ ] Enabled network types (UDP4/UDP6/TCP4/TCP6).
- [ ] Interface and IP filters.
- [ ] Advertised-address/1:1 NAT mapping, integrated with the existing media
  external-IP configuration.
- [ ] ICE-lite mode for appropriate server deployments.
- [ ] Packet-buffer limits and drop policy.
- [ ] Default gathering, connection, DTLS, consent, read, and write timeouts.
- [ ] Logger and metrics hooks.
- [ ] Shared, rotating DTLS certificate provider/pool.
- [ ] An advanced ICE-agent factory/options escape hatch for uncommon
  deployments.

Tenant/account/service policy:

- [ ] Default STUN/TURN server set.
- [ ] Default codec profile and ordering.
- [ ] Allowed SRTP protection profiles.
- [ ] Default trickle and candidate policy.

Per-call/session overrides:

- [ ] Ephemeral TURN credentials.
- [ ] Candidate policy (`all` or `relay-only`).
- [ ] Trickle mode.
- [ ] Call-specific timeouts.
- [ ] Media direction and allowed codecs.

Keep these fixed for the direct audio-only implementation unless its scope is
explicitly expanded:

- [ ] Exactly one audio media section.
- [ ] RTP/RTCP multiplexing is required.
- [ ] DTLS-SRTP and fingerprint verification are required.
- [ ] BUNDLE behavior is fixed for the single-media design rather than exposed
  as a user policy toggle.

### Replace URL-only ICE server configuration

`ICEURLs []string` cannot represent ordinary authenticated TURN configuration
reliably.

- [ ] Add a structured ICE-server type with URLs, username, and credential.
- [ ] Support TURN over UDP, TCP, and TLS where the underlying ICE stack does.
- [ ] Avoid logging credentials or embedding them in generated SDP.
- [ ] Support short-lived credentials and safe per-call overrides.
- [ ] Validate malformed or unsupported URL/credential combinations early.
- [ ] Add coturn integration tests for UDP, TCP, TLS, and relay-only operation.

### Reduce per-call resource cost

- [ ] Reuse a shared ICE socket via UDP mux for server deployments.
- [ ] Measure and cap packet-buffer memory; the current three packet buffers can
  grow to roughly 3 MiB per call in the worst case.
- [ ] Make buffer size/count configurable at transport scope with safe defaults.
- [ ] Rate-limit or aggregate packet-drop logging.
- [ ] Reuse or pre-generate DTLS certificates rather than generating an ECDSA
  key and X.509 certificate on every call.
- [ ] Review per-call goroutines and timers from mux, RTCP monitoring, ICE, and
  DTLS; ensure every one has a deterministic shutdown path.

## P2 — Codec and WebRTC media profile

The project-wide default currently emphasizes SIP/G.711. A direct WebRTC
endpoint should have an explicit WebRTC audio profile rather than silently
reusing all global defaults.

- [ ] Offer Opus first when the application can actually source or consume it.
- [ ] Retain PCMU and PCMA for gateway interoperability.
- [ ] Negotiate telephone-event independently of a hard-coded payload type.
- [ ] Consider comfort noise for G.711 deployments.
- [ ] Extend codec capabilities to represent `fmtp`, `rtcp-fb`, `ptime`,
  `maxptime`, and RTP header extensions.
- [ ] Negotiate Opus parameters, including in-band FEC, instead of emitting a
  fixed `useinbandfec=0` value.
- [ ] Clearly distinguish passthrough capability from codecs that Diago can
  encode, decode, transcode, or inspect.
- [ ] Add Chrome, Firefox, and Safari audio interoperability tests for the
  supported profile.

References: [RFC 7874](https://www.rfc-editor.org/rfc/rfc7874),
[RFC 7587](https://www.rfc-editor.org/rfc/rfc7587),
[RFC 8854](https://www.rfc-editor.org/rfc/rfc8854).

## P2 — Consolidate duplicate implementations

The `mediaweb` package substantially duplicates the direct implementation in
`media`, increasing the chance that security and protocol fixes diverge.

- [ ] Choose `media` as the canonical direct-WebRTC implementation, or document
  why both packages must remain.
- [ ] Deprecate/remove `mediaweb`, or turn it into thin aliases/adapters over the
  canonical implementation.
- [ ] Move shared SDP/ICE/DTLS/SRTP logic behind one tested implementation.
- [ ] Add a migration note for users of the package being deprecated.

## Performance and reliability test plan

Existing generic RTP benchmarks are useful baselines, but they do not measure
ICE, DTLS, SRTP, packet muxing, certificate generation, or SIP signaling.

### Microbenchmarks

- [ ] Benchmark SRTP protect/unprotect for RTP at 20 ms (50 packets/s) and
  10 ms (100 packets/s) packetization rates.
- [ ] Benchmark SRTCP protect/unprotect and RTCP marshal/write paths.
- [ ] Compare AES-CM and AES-GCM profiles where supported.
- [ ] Track allocations per RTP/RTCP packet and eliminate avoidable hot-path
  allocations.
- [ ] Benchmark packet mux classification and dispatch.
- [ ] Compare fresh per-call certificates with a shared certificate provider.

Record benchmark results in CI or a reproducible report. Current local generic
RTP baselines observed during the review were approximately:

| Path | Time | Allocations |
| --- | ---: | ---: |
| RTP reader | 338–341 ns/op | 72 B/op, 2 allocs/op |
| RTP writer | 416–615 ns/op | 518–687 B/op, 3 allocs/op |
| RTCP unmarshal | 709–745 ns/op | 112 B/op, 2 allocs/op |

Treat these as orientation only; rerun them on a pinned machine before using
them as regression thresholds.

### Setup latency

- [ ] Measure host-only, STUN, TURN/UDP, TURN/TCP, and TURN/TLS setup.
- [ ] Measure unreachable and partially reachable ICE-server behavior.
- [ ] Separate ICE gathering, ICE connectivity, DTLS handshake, and total
  INVITE-to-media latency.
- [ ] Compare non-trickle, half-trickle, and full-trickle signaling.
- [ ] Measure fresh versus shared certificate setup.
- [ ] Track SIP message sizes as candidate counts grow, especially over UDP.

### Scale and soak

- [ ] Test 100, 1,000, and 10,000 concurrent calls, subject to host capacity.
- [ ] Record call setup rate, CPU, RSS, file descriptors, sockets, goroutines,
  timers, packet buffers, GC activity, and packet loss.
- [ ] Compare one socket per ICE agent with shared UDP mux operation.
- [ ] Run long-duration bidirectional audio with RTP and RTCP enabled.
- [ ] Exercise slow readers, absent readers, buffer saturation, and log-volume
  behavior.
- [ ] Verify no goroutine, timer, socket, ICE agent, or certificate leak after
  repeated setup/teardown and failed handshakes.

### Network impairment and interop

- [ ] Test loss, duplication, reordering, delay, jitter, and bandwidth limits.
- [ ] Test IPv4, IPv6, dual-stack, NAT rebinding, mDNS candidates, and consent
  loss.
- [ ] Test coturn via UDP, TCP, and TLS, including relay-only mode.
- [ ] Test Chrome, Firefox, and Safari against the supported audio profile.
- [ ] Test replayed SRTP/SRTCP, DTLS fingerprint mismatch, stale ICE-generation
  candidates, and malformed candidate attributes.

### SIP signaling stress

- [ ] Test large candidate sets over SIP UDP and confirm TCP/TLS fallback or
  trickle behavior avoids fragmentation problems.
- [ ] Test trickle INFO duplicates, retransmissions, reordering, aggregation,
  transaction failure, and end-of-candidates.
- [ ] Test early-dialog forking and distinct remote targets.
- [ ] Test `183`/PRACK/`200` sequencing and final-answer changes.
- [ ] Fuzz SDP, SDP fragments, ICE candidates, and offer/answer state changes.

## Suggested delivery order

1. Enable replay protection, serialize SRTP contexts, fix mux classification,
   and correct negotiated DTMF payload handling.
2. Add the explicit session state machine and deterministic cancellation.
3. Fix RTCP negotiation/compound behavior and add ICE state observability.
4. Introduce structured ICE servers, shared transport configuration, shared
   certificates, and UDP mux support.
5. Add re-INVITE and ICE-restart lifecycle support.
6. Add RFC 8840 trickle ICE APIs and SIP INFO signaling with fallback.
7. Expand the WebRTC codec profile and browser/coturn interoperability suite.
8. Consolidate `mediaweb` with the canonical implementation.

## Completion gates

- [ ] `go test ./media` passes.
- [ ] Direct WebRTC media and SIP integration tests pass under `go test -race`.
- [ ] The Pion-backed dialog race regression passes.
- [ ] Repeated setup/failure/close tests show no growing goroutine, file
  descriptor, timer, or buffer count.
- [ ] Replay, mux-demultiplexing, final-answer, re-INVITE, ICE-restart, and
  trickle fallback regression tests exist.
- [ ] Browser and coturn interoperability results are recorded for the supported
  configuration matrix.
- [ ] Public configuration defaults and scoping are documented with migration
  guidance for any API changes.
