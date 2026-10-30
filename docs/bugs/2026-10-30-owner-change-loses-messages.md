# A change of realtime owner lost messages and failed sends (2026-10-30)

## What happened

A failover run on the HA dev stack (`docs/load-testing.md`, "Failover"):
300 members, 50 msgs/s, one realtime replica drained, brought back, another
killed, brought back. 34 of 6,000 sends failed (500, 502), 49 deliveries
never arrived, and 25 streams were still closed when the run ended.

## Why

- A request reaching a replica whose view still named a dead peer as the
  room's owner was forwarded there and answered 502.
- While views disagreed (a replica joining is seen up to 5 s apart: DNS is
  re-read every 5 s), two replicas briefly owned a room. One's commit hit
  the other's sequence (500, by design: the client's resend reaches the real
  owner, but nothing resent). A message one of them stored reached only its
  own streams, which had moved; a client resuming from its last sequence
  could not tell it lacked an earlier one the other owner had written.
- Reconnect backoff grew to 30 s after a few failed reopens during the
  change.
- Stopping a replica cut everything at once (no drain).

## Fix

The forward retries a request that never reached a dead owner through the
new one; clients resend a 5xx with the same `clientMsgId`; backoff stops at
8 s; a replica serving a room anew delivers the rows the previous owner
stored (`services/realtime/handover.go`) and tells resuming streams to read
the difference; services drain on SIGTERM and a key-owning replica leaves
its peer set at once (`internal/obs/drain.go`, `shard.Set.Leave`). The same
run then stored 6,000 of 6,000 sends and delivered every message.

## Lesson

A sequence number is a high-water mark only while one writer exists. Every
place that can briefly have two (an ownership change) needs a way for the
reader to learn what lies below its mark.
