## Unreleased

### Added
- DialogMedia.OnRTCP hook to receive raw RTCP (RR/SR) packets.
  - Non-blocking callback execution.
  - Backward compatible: no changes when not set.
- Unit tests for RTCP hook: callback invocation, non-blocking behavior, deferred registration.


