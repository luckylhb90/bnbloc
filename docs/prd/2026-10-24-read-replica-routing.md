# Read replica routing: primary for writes, replica for opted-in reads (2026-10-24)

Status: built and tested against MySQL 8.4 primary and replica (2026-10-24). Owner: Owner: this repository (`internal/store`, `internal/config`).
Priority: first, before the Pulse backend (`docs/prd/2026-10-24-executive-pulse-backend.md`),
which needs a replica to read from without loading the primary.

## 1. Why

Every store call goes through one `*sql.DB` (93 read sites, 146 write sites,
27 transactions across 48 files). Analytics and reporting reads (the team agent
dashboard, usage reports, platform usage) run on the same primary as chat
delivery and job claiming, so a heavy report can slow the product. There is no
replica configuration today: one `BNBLOC_MYSQL_DSN`.

## 2. What it is

- **One handle, two targets.** `store.DB` replaces `*sql.DB` in every store. It
  has the same methods the stores use (`ExecContext`, `QueryContext`,
  `QueryRowContext`, `BeginTx`, `Conn`, `Exec`, `QueryRow`, `Close`), so the store
  code changes its field types and nothing else.
- **Writes, transactions, connections, locks: always the primary.** `Exec*`,
  `BeginTx`, `Conn` (advisory locks) never leave the primary.
- **Reads: the primary by default.** A read goes to the replica only when its
  context was marked with `store.WithReplicaReads`, which a handler calls
  explicitly. A handler that opts in must be read-only by construction. Nothing
  is routed to a replica by accident, so read-your-writes cannot break anywhere
  that did not opt in.
- **Replica health, checked in the background.** Every `BNBLOC_MYSQL_REPLICA_CHECK`
  (default 2 s), the store runs `SHOW REPLICA STATUS` on the replica (falling back
  to `SHOW SLAVE STATUS` on MySQL older than 8.0.22). The replica is usable only
  if the IO and SQL threads run, `Seconds_Behind_Source` is known and at most
  `BNBLOC_MYSQL_REPLICA_MAX_LAG` (default 5 s), and the last check is fresh.
  Otherwise every opted-in read falls back to the primary. Correct answers, more
  load on the primary, and a logged reason.
- **Routing decision is a pure function** (`route(...)`), tested without a
  database. The integration tests run against a real primary and replica.
- **Configuration:** `BNBLOC_MYSQL_REPLICA_DSN` (unset = no replica, identical to
  today), `BNBLOC_MYSQL_REPLICA_MAX_LAG`, `BNBLOC_MYSQL_REPLICA_CHECK`,
  `BNBLOC_MYSQL_REPLICA_MAX_CONNS` (default 20; the server's `max_connections` is
  shared by every replica of every service, so this is a budget, not a free
  option).
- **Schema and migrations run on the primary only.** The replica receives them
  through replication. Opening the replica never applies DDL.

## 3. Placement table

| Part | Content / component / platform | Why (platform: the named reason) | What an organization would swap |
|---|---|---|---|
| `store.DB` routing, health, lag rule | Platform | Infrastructure every store uses; one owner of "which connection a read gets" | Nothing; a replica is a deployment fact |
| Which handlers opt in to replica reads | Platform (the handler's own choice, read-only by construction) | Each read site knows its own tolerance for lag | Any handler can opt in |
| Replica DSN and tuning | Deployment configuration | Hosts and credentials are per deployment | The org's topology |

Name test: no product, feature or use case named in the router. Fork test: an
organization with one database changes nothing. Replace test: a proxy-based
router (ProxySQL) could sit in front of the replica DSN; the store does not need
to change.

## 4. Design review

| Lens | Answer |
|---|---|
| Actors | Members and admins see reads that may lag by up to the max lag. Writes are unaffected. Operators choose the topology. |
| Time and lifecycle | A replica that falls behind is removed from routing within one check interval and returns when its lag is back under the bound. A check that stops running is treated as unusable after three intervals. Replica restart: reads fall back until the first healthy check. |
| Scale and cost | Health check: one `SHOW REPLICA STATUS` every 2 s per process. Routing cost: a context lookup and an atomic load per read. Replica connections are a separate pool with a stated budget. |
| Failure and recovery | Replica down or lagging: opted-in reads fall back to the primary, reason logged once per change. A replica read that fails after the call returns (`QueryRowContext` errors surface at `Scan`) is returned to the caller; the watcher marks the replica unusable within one interval. Primary down: unchanged from today. |
| Boundaries and privacy | No new data crosses a boundary. Replica credentials are read from the environment like the primary's. |
| Abuse and untrusted input | Routing is not driven by input; only by the handler's code. |
| One owner | "Which handle a read uses" is decided in `store.DB` only. |
| Defaults and fallbacks | No replica DSN: every read is on the primary, byte-for-byte the current behaviour. Unknown lag: unusable. |
| External contracts | MySQL 8.0.22+ `SHOW REPLICA STATUS` and older `SHOW SLAVE STATUS`; both column sets are handled. Tested against MySQL 8.4 with real replication. |
| Load on users and agents | None added. |
| Release and compatibility | Additive. Old services without the variable are unaffected. Rollback: unset the variable. No schema change. |
| Observability | Log line on every routing-state change (usable, reason, lag). Metrics (`internal/store/replica_metrics.go`, documented in `docs/monitoring.md`): `bnbloc_store_replica_usable`, `bnbloc_store_replica_lag_seconds`, `bnbloc_store_reads_total{target=replica\|fallback}`. |
| AI capability | None; no model is in this path. |

**Pre-mortem.** It shipped and a month later it failed badly:

1. **A report read stale data and someone acted on it.** Answer: only handlers that
   accept lag opt in; the PRD names them; the lag bound is visible in logs and metrics.
2. **The replica's connection budget exhausted `max_connections`** and took down
   the primary's writes. Answer: a separate capped pool per process, the budget
   documented, and an alert on connection count.
3. **Someone opted in a handler that writes**, and the replica refused the write
   with a read-only error that only appeared in production. Answer: the router sends
   `Exec*` and transactions to the primary regardless of the context, so the write
   succeeds and the mistake is a silent extra read path, not an error; a test
   asserts opt-in does not change where writes go.

## 5. Acceptance

- Without `BNBLOC_MYSQL_REPLICA_DSN`: the full store test suite passes on the
  primary exactly as before.
- With a replica: an opted-in read returns the replica's rows; a default read
  returns the primary's rows (proven with a deliberately stopped SQL thread on the
  replica, so the two differ).
- Stop the replica's SQL thread for longer than the max lag: opted-in reads return
  the primary's rows; restart the thread: they return to the replica within one
  interval after the lag is back under the bound.
- Writes and transactions with a replica-read context go to the primary.
- Replica unreachable: opted-in reads fall back; no panic; the log says why.
- Hot path: a benchmark of a primary read through `store.DB` is within noise of the
  bare `*sql.DB` call.

## 6. Landing

All steps below are landed. Verification at landing: the full repository suite passes
without MySQL, and with the primary and replica configured (`go test ./...`). The replica
tests exercise real replication with a stopped-delay replica. Measured on the primary
with the router in the path: about 8 µs more per read over the bare pool, one more
allocation per read, within the noise of a Docker VM network (160 µs vs 168 µs); the
default path adds no allocation of its own (`TestReaderDefaultPathAllocatesNothing`).

1. This PRD.
2. `store.DB`, `route`, the watcher, configuration and tests (routing unit tests;
   integration tests gated on `BNBLOC_TEST_MYSQL_DSN` and
   `BNBLOC_TEST_MYSQL_REPLICA_DSN`).
3. Type change across the stores; the full existing suite green against a primary.
4. Opt-in for the first read-only handlers: the team agent dashboard, the usage
   reports and the platform-agent usage view (checked: no store write on those paths).
5. Operator documentation (`docs/production-deployment.md`, `docs/environment-variables.md`)
   and a local replica recipe.
