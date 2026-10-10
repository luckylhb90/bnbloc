# Enterprise scale: load testing, realtime scale-out, retention, search

Status: approved 2026-10-10, in progress. Section 1 landed: `cmd/bnbloc-loadtest`, with the baseline in `docs/load-testing.md`.

## Problem

A large organization (thousands of members online, hundreds of active rooms)
hits four limits in the current design:

1. **No measured capacity.** Nobody knows how many streams, rooms or messages
   per second one deployment sustains.
2. **realtime and relay are singletons.** Room streams, per-room sequences,
   presence, the agent roster, plugin delivery and the relay pairing table
   live in one process. The Helm chart refuses replicas. A restart drops
   every stream (history is durable, so nothing is lost, but everyone
   reconnects), and capacity is one machine.
3. **Data only grows.** Messages, artifact versions, audit rows and blobs are
   never deleted. Organizations need retention to meet their compliance rules
   and to bound storage.
4. **Search is MySQL FULLTEXT (ngram).** Fine for small deployments, slow at
   hundreds of millions of messages.

What is already fine: gateway, room, activity and shareagent are stateless
and scale horizontally; background workers (notification outbox, schedules,
agent jobs) claim with leases, so several replicas are safe; blobs are
content-addressed in S3-compatible storage; agents run on members' machines,
so agent compute never lands on the server.

Out of scope: multi-region deployment (needs a concrete customer requirement
and touches every store).

## 1. Load test tool

`cmd/bnbloc-loadtest` drives a running gateway with simulated members:

- N members (dev sign-in, or API tokens from a file), R rooms, each member
  holding the room SSE stream the web client holds;
- a configurable chat send rate; each message carries a client id so the tool
  measures send → delivery latency on every receiving stream;
- reports p50/p95/p99 delivery latency, send errors by status, stream drops,
  and achieved throughput, as text and JSON.

`docs/load-testing.md` explains how to run it against the compose stack and a
cluster, and records the baseline measured on the compose stack.

## 2. realtime and relay scale-out by key ownership

Each room is owned by exactly one realtime replica, so ordering, per-room
sequences, presence and the roster stay in-process exactly as today. No new
infrastructure (no Redis or NATS).

- **Peer set.** `BNBLOC_REALTIME_PEERS` is either a DNS name resolved
  periodically (a Kubernetes headless Service) or a comma list of
  `host:port`. `BNBLOC_REALTIME_SELF` is this replica's own address (pod IP
  through the downward API). Both unset = single node, today's behavior with
  no forwarding. relay gets the same pair (`BNBLOC_RELAY_PEERS`,
  `BNBLOC_RELAY_SELF`).
- **Ownership.** Rendezvous hashing of the key over the sorted peer set. One
  shared package (`internal/shard`) serves realtime, relay and the gateway.
- **Forwarding.** A replica that receives a request for a key it does not own
  proxies it to the owner (one hop; SSE streams are proxied with flushing).
  Callers (gateway, activity, room, bridge) are unchanged: they keep calling
  the load-balanced Service. A forwarded request is marked so it is never
  forwarded twice. A replica that receives a forwarded request it does not
  own (the peer views disagree for a moment) serves it locally instead of
  looping.
  - Room-scoped routes (`/v1/rooms/{roomId}/…`, `/internal/v1/rooms/{roomId}/…`)
    use the room id as the key.
  - Member streams (`/v1/my/plugin-events` and its ack) use the member.
    Plugin and notification deliveries produced by a room owner are handed to
    the member's owner over an internal route.
  - relay keys on the session key.
  - Read-only, store-backed routes (`/v1/rooms-unread`, blobs) are served by
    any replica. More than one realtime replica requires the S3 blob backend;
    startup refuses the disk backend with peers configured.
- **Ownership change.** When the peer set changes, a replica closes the
  streams of keys it no longer owns. Clients reconnect, land on the new owner
  and catch up from history (the existing reconnect path). The new owner
  seeds the room's counter from the durable max (`Hub.SeedSeq`). The durable
  `(room_id, seq)` primary key fences two replicas that briefly both believe
  they own a room: the loser's commit fails and the client's retry
  (idempotent by `clientMsgId`) lands on the new owner. Sequence numbers that
  transient events used must never make a client drop a later durable event;
  the implementation verifies the client and bridge dedup rules and fixes the
  root cause if they can.
- **Deploy.** The Helm chart runs realtime and relay as StatefulSets behind
  a headless Service with `replicas` allowed above 1, and drops the singleton
  guard. compose stays single-node. Every deploy surface and the env docs
  change together.

### Gateway rate limiting with replicas

The per-subject token bucket lives in each gateway replica's memory, so N
replicas allow N times the configured rate. The gateway reads the same kind of
peer set (`BNBLOC_GATEWAY_PEERS`) and divides the configured rate and burst by
the live replica count. This is the standard local-rate-limit approximation;
it needs no shared store.

## 3. Data retention

- **Policy.** A platform default (super-admin) and a per-tenant override
  (tenant admin), in days, for:
  - room messages;
  - artifact versions other than the latest;
  - the audit log;
  - finished agent jobs.

  Unset means keep forever (today's behavior).
- **Worker.** Deletes in small batches by age. It is safe to run on several
  replicas, because concurrent batch deletes of the same expired rows are
  harmless. It reports deleted counts as metrics.
- **Blobs.** After rows are deleted, a garbage-collection pass removes blobs
  that no remaining row references (attachments, artifact versions, sites).
  A grace period protects blobs uploaded but not yet referenced.
- **UI.** The settings live where tenant and platform settings already live.
  The UI states plainly what is deleted and when.

## 4. Search backend

- `internal/search` defines the index interface. MySQL FULLTEXT stays the
  default implementation.
- An OpenSearch implementation is selected by `BNBLOC_SEARCH_OPENSEARCH_URL`
  (plus credentials). It talks plain REST with no SDK dependency. The index
  is kept current on publish, edit, delete and retention deletes; a failure
  is logged and counted as a metric and never fails the chat write.
- `bnbloc search reindex` backfills or rebuilds the index from MySQL.
- Search results always pass the same membership and people-chat filters as
  today; OpenSearch returns candidates and the store stays the authority.

## Order

1 and 2 first (2 is measured with 1), then 3, then 4. Each lands with its own
tests, deploy surfaces and docs, and updates this file's status.

## Verification

- The load test runs before and after scale-out. The scale-out run uses 3
  realtime replicas and reports latency and throughput.
- A replica killed mid-test: clients reconnect, no durable message is lost or
  duplicated, and no client drops a later message.
- Retention: rows older than the policy are gone, newer ones untouched, and
  unreferenced blobs are collected.
- Search: the same queries return the same visible results on MySQL and on
  OpenSearch, including the people-chat and membership filters.
