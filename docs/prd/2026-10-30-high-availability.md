# No single point of failure (2026-10-30)

Status: built (2026-10-30). The owner, on hearing the services run as one
replica each: "现在服务都是单点，怎么更好的解决单点问题？" and "有问题就解决啊". Owner:
the service runtime (`internal/obs`), the scale-out set (`internal/shard`),
realtime's room hand-over (`services/realtime/handover.go`), the Helm chart
(`deploy/k8s`), the web stream client (`web/src/lib/sse.ts`,
`web/src/lib/sendRetry.ts`). Follows section 2 of
`docs/prd/2026-10-10-enterprise-scale.md` (scale-out by key ownership).

## 1. Where it stood

Every service already ran correctly with more than one replica: stateless
ones share the database, realtime and relay own keys by rendezvous hashing,
workers claim with leases. What made replicas fragile, found by a failover
run under load (`docs/bugs/2026-10-30-owner-change-loses-messages.md`):

- Stopping a pod cut its requests and streams at once.
- A request to a dead owner failed; two owners for a moment lost messages;
  sends that met a 5xx were not resent; reconnects backed off to 30 s.
- The chart had no disruption budget, no spreading, a 10 s readiness period.

## 2. The design

- **Drain** (every service, `internal/obs/drain.go`): on SIGTERM `/readyz`
  answers 503, event streams end and new ones are refused (503,
  `Retry-After`), a key-owning replica leaves its peer set at once
  (`shard.Set.Leave`), then after 5 s the listener stops and waits up to 20 s
  for requests in flight, then shutdown hooks run (realtime: follow marks
  written behind, queued notifications). A request its client or the drain
  ended is a 499, never an error line.
- **A dead owner's request goes on** (`shard.Forward`): when the owner could
  not be connected to, the forward waits for the view to drop it (at most
  3 s) and serves the request through the new owner, its body kept (up to
  1 MiB).
- **Hand-over** (`services/realtime/handover.go`): for 6 s after a replica
  starts serving a room it reads the room's newest rows every 300 ms and
  delivers those it did not (`Hub.DeliverCommitted`), and a stream resuming
  then is told to read the difference (`stream_gap`).
- **Clients**: a send that meets a 5xx or no answer is resent with the same
  `clientMsgId` (400 ms, 1.2 s, 3 s); a `stream_gap` reads history at once
  and 3 s later; reconnect backoff stops at 8 s.
- **Room events reach realtime** after a moment of trouble: the room
  service's publish is retried in the background on a 5xx or no answer.
- **Deployment**: the chart's PodDisruptionBudget (`maxUnavailable: 1`),
  topology spread over nodes and zones, `terminationGracePeriodSeconds: 40`
  and a 2 s readiness period; `compose.ha.yml` and
  `scripts/failover-check.sh` for the same test anywhere; `_SELF=auto` for
  Compose replicas; a blob directory every replica mounts
  (`BNBLOC_ATTACHMENTS_DIR_SHARED`) as an alternative to S3.
- **Stays single by design**: pulse (one stream consumer; the organization
  board pauses, nothing on the message path waits for it). MySQL and Redis
  are the cloud's (Multi-AZ).

## 3. Placement

| Part | Place | Why |
|---|---|---|
| Drain, 499 accounting, profiler hooks | Platform (obs) | Every service's runtime |
| Forward retry, Leave | Platform (shard) | The scale-out contract |
| Hand-over, DeliverCommitted | Platform (realtime) | The realtime contract |
| Send resend, gap reads, backoff | Platform (web) | The stream contract's client |
| Chart settings, Compose overlay | Deployment | Operators' |

## 4. Design review

| Lens | Answer |
|---|---|
| Failure and recovery | Drain, crash, rejoin each measured under load (`docs/load-testing.md`, "Failover"): no send failed, no delivery lost. |
| Time and lifecycle | A drain takes at most 25 s; the grace period is 40 s. A crash moves keys in about 2 s; a join up to 5 s (DNS), covered by the hand-over's 6 s. |
| Scale and cost | The hand-over reads a room 20 times once per change of owner; the resend and the second gap read are one request each, only on trouble. |
| Abuse and untrusted input | A resend carries the same `clientMsgId`: stored once. The kept body is bounded (1 MiB); uploads are never kept. |
| Release and compatibility | Old clients still reconnect (no `since`: a full replay, as before) and do not resend; nothing they send is refused. |
| Pre-mortem | A hand-over watch that misses a row (more than 200 rows in 300 ms in one room) leaves the client to its gap reads; a replica that cannot read the store during its watch delivers nothing extra (the gap reads cover). |

## 5. Decisions pending owner

- **H1.** QA's own manifests (not the chart): realtime to 3 replicas with S3,
  the others to 2, and the drain settings (internal runbook
  `docs/runbooks/2026-10-11-eks-realtime-scale-out.md` §11 in the private
  repository). An operator applies them.
- **H2.** The joint load test on QA (the owner offered to run it together):
  1,024 members over 16 channels at 100 msgs/s, then the failover drill
  during it.
