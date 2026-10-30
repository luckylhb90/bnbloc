# Load testing

`cmd/bnbloc-loadtest` drives a running gateway with simulated members and
measures the room message path end to end. It is section 1 of
`docs/prd/2026-10-10-enterprise-scale.md`. Use it to get a capacity number
for a deployment and to compare runs before and after a change.

## What it does

1. **Members.** It signs in N members, or reads their session tokens from a
   file (see [Authentication](#authentication)).
2. **Rooms.** It creates R private rooms, or uses existing room ids. Room
   `r` gets members `r*M .. r*M+M-1` (mod N), where M is the number of
   members per room. With `R*M == N` the rooms partition the members. Above
   that, members wrap into more rooms and hold one stream per room, like a
   person with several tabs. The first member of a room owns it and adds the
   others.
3. **Streams.** Every member holds the room SSE stream the web room view
   holds, as `web/src/lib/sse.ts` `subscribeRoom` opens it, a lanes stream
   (channels PRD §2.4):
   `GET /v1/rooms/{id}/events?access_token=…&channelId=<channel>&lanes=1&open=<channel>&includePeople=1`.
   With `--channels C` (default 1) each room has C channels (its default
   plus ones the tool creates, or reuses in `--room-ids` rooms), and the
   room's members are spread over them round-robin: member k has channel
   k mod C open. The streams count as present members and receive presence
   events. They open evenly over the ramp-up. A dropped stream reopens with
   the web client's backoff: 1 s doubling to 8 s, with jitter.
4. **Sends.** After the ramp-up it sends chat at the target total rate, the
   way `api.sendChat` does: `POST /v1/rooms/{id}/chat` with `text`,
   `atUnixMs`, a unique `clientMsgId` and the `channelId` the member has
   open. Sends go
   round-robin over a shuffled list of every (room, member) pair, so each
   member's rate stays as low as the population allows. The send loop is
   open-loop: a send is due by the clock, not when the previous one answered.
5. **Measurement.** The tool notes which of the room's streams with the
   message's channel open were open when each send started. Every `chat_message` that arrives on a stream is
   matched to its send by `clientMsgId`. The first arrival on each expected
   stream is one latency sample, from send start to arrival. The sender's own
   stream counts too: that is the echo the web client renders. A stream
   with another channel open receives the message as that channel's
   `channel_activity` (coalesced to one a second); the report's activity
   line counts them and how many streams heard of every other channel, the
   unread badges' source.
6. **End.** It waits for outstanding deliveries (`--drain`, 5 s by default),
   then prints the report. Rooms it created are deleted unless
   `--cleanup=false`.

The first SIGINT ends the send window. The tool skips the drain, reports,
and still deletes its rooms. A second SIGINT exits immediately.

## Authentication

The tool only calls public gateway routes, with the same credentials a
person's client uses.

- **Dev sign-in (default).** A gateway without OIDC that is not running with
  `BNBLOC_ENV=production` uses the dev verifier. `POST /v1/auth/login
  {"idpToken": "<subject>"}` signs in as that subject. The tool signs in
  `<prefix>-0001 … <prefix>-N` (`--member-prefix`, default `loadtest`).

  The gateway throttles direct login **per client IP**: a burst of 10, then
  5 per minute. One load generator therefore cannot sign in more than about
  10 members. To lift that limit, pass `--forwarded-for`. The tool then sends
  a distinct synthetic `X-Forwarded-For` (`10.0.0.1`, `10.0.0.2`, …) with
  each sign-in. The gateway believes the header only from a trusted proxy, so
  the gateway must list the load generator's address in
  `BNBLOC_TRUSTED_PROXIES`. Never trust a load generator on a production
  gateway. Without that setting, the tool stops at the first 429 and names
  both remedies.
- **Tokens file (`--tokens-file`).** One session bearer per line; lines
  starting with `#` are comments. The member's subject comes from the token
  payload. Use this when the dev sign-in is unavailable, for example on a
  gateway with OIDC: mint sessions with your identity provider and put one
  per line. Sessions last one hour. The tool warns when a token expires
  before the run ends.

Sends use `Authorization: Bearer`. Streams pass the token as `access_token`,
as `EventSource` must.

## Running against the compose stack

The tracked compose files stay unchanged. Put the changes in an untracked
overlay and pass both files with `-f`. Passing `-f` also stops compose from
auto-loading any local `compose.override.yml`. This overlay:

- publishes the gateway on another host port;
- skips the web bundle build;
- trusts the load generator as a proxy.

```yaml
# loadtest.override.yml (untracked)
services:
  gateway:
    build: { args: { WEBUI: none } }
    ports: !override
      - "17090:8080"
    environment:
      BNBLOC_TRUSTED_PROXIES: "0.0.0.0/0,::/0"   # dev stack only
```

```sh
export COMPOSE_PROJECT_NAME=bnbloc-loadtest
docker compose -f compose.yml -f loadtest.override.yml up -d --build

go build -o bin/bnbloc-loadtest ./cmd/bnbloc-loadtest
bin/bnbloc-loadtest --gateway http://localhost:17090 --forwarded-for \
  --members 200 --rooms 20 --rate 50 --duration 60s --ramp-up 10s

docker compose -f compose.yml -f loadtest.override.yml down -v
```

`--json` prints the report as JSON on stdout. Progress lines go to stderr
every `--progress` interval (default 5 s).

The per-service request histograms help find where time goes. Every service
serves `/metrics`; the gateway serves it on its internal `:9090` listener. On
the compose network:

```sh
docker run --rm --network bnbloc-loadtest_default curlimages/curl -s http://realtime:8082/metrics
```

## Running against a cluster

- Run the tool close to the cluster, for example as a pod in the same
  region. Otherwise you measure your own network.
- With OIDC configured, use `--tokens-file`. The dev sign-in exists only on
  non-production gateways. If you do use the dev sign-in on a staging
  gateway, set `BNBLOC_TRUSTED_PROXIES` to the load generator's address (or
  its pod CIDR) only for the duration of the test.
- Use `--room-ids` to reuse rooms. Every member assigned to a room must
  already belong to it. A run with `--cleanup=false` leaves suitable rooms
  behind; the tool logs their ids.
- The gateway rate-limits chat **per subject**, at about 2 messages per
  second with a burst of 10. Keep `--rate` divided by the number of members
  well below that, or the report measures the limiter. The report flags any
  429s.
- One process holds one connection per stream over HTTP/1.1. Over HTTPS, the
  streams multiplex on HTTP/2, as browsers do. Check the open-file limit
  (`ulimit -n`) for large runs.

## Flags

| Flag | Default | Meaning |
| --- | --- | --- |
| `--gateway` | (required) | Gateway base URL |
| `--members` | 200 | Members to sign in through the dev sign-in |
| `--member-prefix` | `loadtest` | Dev sign-in subjects are `<prefix>-0001`, `<prefix>-0002`, … |
| `--tokens-file` | | One session token per line, instead of the dev sign-in |
| `--forwarded-for` | false | Synthetic `X-Forwarded-For` per member on sign-in |
| `--rooms` | 20 | Rooms to create |
| `--room-ids` | | Existing rooms to use instead |
| `--members-per-room` | members/rooms | Members in each room |
| `--rate` | 50 | Total sends per second |
| `--duration` | 60s | Send window |
| `--ramp-up` | 10s | Streams open evenly over this period |
| `--settle` | 10s | Extra wait for every stream to be open before sending |
| `--drain` | 5s | Wait for outstanding deliveries after the window |
| `--text-bytes` | 64 | Message size |
| `--max-inflight` | 512 | Outstanding sends; a send due beyond this is skipped and counted |
| `--timeout` | 15s | Per-request timeout for sign-in, setup and sends |
| `--stream-idle` | 45s | Reopen a stream silent for this long (the server sends a keep-alive every 15 s) |
| `--login-parallel` | 16 | Concurrent sign-in and setup requests |
| `--cleanup` | true | Delete the rooms the tool created |
| `--json` | false | Print the report as JSON |
| `--progress` | 5s | Progress interval on stderr; 0 disables |

## Reading the report

- **sends:**
  - `attempted`, `ok`, `rate-limited (429)`;
  - `errors` by HTTP status, plus `error: <kind>` for transport failures;
  - `skipped`: sends never issued because `--max-inflight` sends were
    already outstanding. The offered rate was not reached;
  - the achieved rate;
  - the POST round trip of successful sends.
- **delivery:**
  - `expected`: the sum, over successful sends, of the room's streams that
    were open at send start;
  - `delivered`: the first arrival on each of those streams;
  - `lost`: expected but not delivered by the end of the drain;
  - `extra`: matching arrivals that were not expected. These are a stream
    that opened after the send and got the message from the replay buffer,
    or a repeat after a reconnect;
  - `out-of-order`: an event whose sequence was lower than one already seen
    on the same connection;
  - deliveries per second over the send window;
  - send-to-arrival latency.
- **streams:**
  - opens, disconnects (an open stream that ended during the run) and
    reconnects. realtime closes a stream that falls a whole buffer (64
    events) behind (`bnbloc_realtime_sse_overflows_total`); the web client
    then catches up from history, but the load tool does not, so the events
    it missed while reconnecting are not counted as expected;
  - failed opens by status;
  - streams open when sending started and at the end;
  - connect time, from request to response headers.

Percentiles use the nearest-rank method, so every reported value is a real
observation. Latencies are measured in one process on one monotonic clock.
No clock synchronization is involved.

## Baseline (compose stack, 2026-10-10)

**Caveats.** These numbers come from a single developer machine. The load
generator, every service and MySQL ran on the same host, inside Docker
Desktop's Linux VM. They show the order of magnitude and the shape of this
build, not production capacity. A deployment on real servers with local SSDs
behaves differently, mostly in commit latency (see below). Re-measure on the
target environment.

- **Machine:**
  - Apple M4 Pro, 12 cores, 48 GB RAM, macOS 15.7;
  - Docker Desktop 29.4, with a VM of 12 CPUs and 7.75 GiB;
  - MySQL 8.4.11 with the compose defaults (binlog on, `sync_binlog=1`,
    `innodb_flush_log_at_trx_commit=1`);
  - images built from commit `293acdf`, with the overlay above.
- **Commands:** the compose commands above, then:

```sh
# A: 200 members, 20 rooms of 10, 50 msgs/s, 60 s
bnbloc-loadtest --gateway http://localhost:17090 --forwarded-for \
  --members 200 --rooms 20 --rate 50 --duration 60s --ramp-up 10s
# B: 1000 members, 20 rooms of 50, 50 msgs/s, 60 s
bnbloc-loadtest --gateway http://localhost:17090 --forwarded-for \
  --members 1000 --rooms 20 --rate 50 --duration 60s --ramp-up 20s
```

| Run | Streams | Deliveries/s | Delivery p50 / p95 / p99 / max (ms) | POST p50 / p99 (ms) | Lost | Disconnects | Stream connect p50 / p99 (ms) | Sign-in |
| --- | --- | --- | --- | --- | --- | --- | --- | --- |
| A: 200 members, 50 msgs/s | 200 | 500 | 200 / 862 / 1799 / 2988 | 211 / 1854 | 0 of 30,000 | 0 | 60 / 365 | 3.8 s |
| B: 1000 members, 50 msgs/s | 1000 | 2500 | 358 / 2688 / 4146 / 5367 | 381 / 4163 | 0 of 150,000 | 0 | 144 / 1178 | 29.5 s |

In both runs every send succeeded, no 429 occurred, every stream stayed
open, and no event arrived out of order.

### Diagnostic runs

Further runs on the same machine and stack separated the disk from the rest
of the path. Most of them restarted MySQL with
`--innodb-flush-log-at-trx-commit=2 --sync-binlog=0`, so a commit no longer
waits for an fsync. That setting is **not** a recommended production
setting; it only shows how much of the latency is the commit.

| Run | MySQL | Delivery p50 / p95 / p99 (ms) |
| --- | --- | --- |
| 10 members, 2 rooms, 5 msgs/s (unloaded) | default | 81 / 213 / 235 |
| 20 members, 2 rooms, 5 msgs/s (unloaded) | relaxed | 29 / 78 / 182 |
| A: 200 members, 20 rooms of 10, 50 msgs/s | relaxed | 39 / 373 / 864 |
| A with 100 rooms of 2 (same members and rate) | relaxed | 43 / 221 / 408 |
| 200 members, 20 rooms of 10, 100 msgs/s (1000 deliveries/s) | relaxed | 98 / 574 / 951 |
| B: 1000 members, 50 msgs/s | relaxed | 54 / 758 / 1203 |

A rate-limit check also ran: 20 members in one room at 60 msgs/s (3 per
member per second) for 20 s. It got 980 sends through and 220 answers of 429.
That matches the per-subject bucket: 20 members × (10 burst + 2/s × 20 s) =
1000.

### What the baseline shows

- **Delivery latency is the chat POST latency.** Delivery p50 tracks POST
  p50 within a few milliseconds. Fan-out to the streams is cheap: 2500
  deliveries per second over 1000 streams lost nothing. The cost sits in
  front of the publish. realtime's own `POST /v1/rooms/:id/chat` histogram
  accounted for about 88% of the gateway's chat time.
- **The commit dominates, and it is serialized per room.** realtime persists
  a message before delivering it, by design. `Hub.PublishCommit` holds the
  room's publish lock across that MySQL commit, so the messages of one room
  queue behind each other's commit. On this disk, a commit was slow and
  spiky:
  - redo log writes averaged 5.6 ms, with a maximum of 1.47 s;
  - binlog writes averaged 11.6 ms, with a maximum of 1.06 s.

  Relaxing the fsync cut the median about five-fold. At the same total rate,
  spreading the messages over more rooms halved the p99 (408 ms instead of
  864 ms). The tail therefore grows with the message rate per room, not only
  with the total rate.
- **No container was CPU-bound.** At 100 msgs/s (relaxed), each container
  used at most about one core of the VM's 12:
  - MySQL about 95%;
  - gateway about 70%;
  - realtime about 61%;
  - room about 6%.

  The latency is waiting (commit, the room lock, the database pool), not
  compute.
- **Database client settings worth examining** (candidates, not measured in
  isolation; both changed in section 1b, see below):
  - The stores open at most 20 connections and keep 10 idle. A 30 s run at
    50 msgs/s opened 165 new MySQL connections.
  - Every query is a server-side prepared statement, so each one costs a
    Prepare and an Execute round trip, plus a Close packet. A chat message
    costs about five of them.
- **Sign-in.** It took 29.5 s for 1000 members with `--forwarded-for` and 16
  parallel sign-ins. Each sign-in writes a refresh-token family.

## Write path (section 1b, 2026-10-11)

Section 1b of `docs/prd/2026-10-10-enterprise-scale.md` changed the write
path: per-room group commit of durable events, idle connections kept open,
client-side parameter interpolation. This section compares the build before
it (`9d3332d`) with the build after it (`aca4e3d`; later commits do not
change the server path) on the same machine and compose stack as the
baseline.

**Caveats, stronger than for the baseline.** During these runs the host was
heavily loaded by unrelated workloads (load average 60 to 330, other
containers using several cores), and the Docker VM's disk stalled for 1 to
15 s at a time. Latency tails are set by those stalls: identical runs of the
same build differed by 2 to 3 times at p99. Medians are more stable, and the
MySQL counters (from `SHOW GLOBAL STATUS` before and after each run) are
exact. Treat the latency numbers as a direction, not a measurement of this
build's capacity.

Two shapes were added to A and B, to load one room's commit path:

```sh
# C: 40 members, 2 rooms of 20, 40 msgs/s (20 per room), 60 s
bnbloc-loadtest --gateway http://localhost:17090 --forwarded-for \
  --members 40 --rooms 2 --rate 40 --duration 60s --ramp-up 5s
# D: 40 members, 1 room, 60 msgs/s, 60 s
bnbloc-loadtest --gateway http://localhost:17090 --forwarded-for \
  --members 40 --rooms 1 --rate 60 --duration 60s --ramp-up 5s --max-inflight 2048
```

**Paired runs** (back to back, before then after, load average 58 to 104):

| Run | Build | Delivery p50 / p95 / p99 / max (ms) | POST p50 / p99 (ms) | New MySQL connections | Prepared statements |
| --- | --- | --- | --- | --- | --- |
| A | before | 26 / 4229 / 5808 / 6466 | 28 / 5876 | 153 | 16,818 |
| A | after | 18 / 4660 / 5859 / 6335 | 19 / 5892 | 64 | 0 |
| D | before | 26 / 7785 / 8531 / 8631 | 28 / 8531 | 30 | 15,311 |
| D | after | 11 / 3417 / 5617 / 6172 | 12 / 5606 | 14 | 0 |

**Runs under heavy load** (load average 167 to 330), every run listed:

| Run | Build | Delivery p50 / p99 (ms) | New MySQL connections | Notes |
| --- | --- | --- | --- | --- |
| A | before | 154 / 5893 | — | |
| A | before | 835 / 9102 | 210 | |
| A | before | 85 / 3345 | 180 | |
| A | after | 42 / 2911 | 65 | |
| A | after | 16 / 9212 | 55 | |
| B | before | 582 / 7614 | 604 | |
| B | after | 26 / 12333 | 39 | |
| C | before | 264 / 2076 | 56 | |
| C | before | 121 / 1714 | 37 | |
| C | after | 24 / 11811 | 46 | 53 answers of 429 and one 502: the load generator itself stalled, then sent a burst |
| C | after | 20 / 5878 | 29 | |
| D | before | 3161 / 6417 | 31 | the room saturated: the POST queue grew for the whole run |
| D | before | 9958 / 14994 | — | collapsed: 1013 of 3600 sends timed out (15 s), 49 answered 500 |
| D | after | 39 / 8210 | 16 | 255 sends timed out during a stall longer than 15 s |
| D | after | 10 / 2614 | 24 | |

Every other run delivered every expected event, in order, with no 429.

**What the comparison shows.**

- **Medians dropped** in every pair and every shape: paired A from 26 to
  18 ms and D from 26 to 11 ms; under heavy load from 85 to 835 ms (D
  saturated aside) to 10 to 42 ms. One round trip per query instead of a
  Prepare and an Execute, and no message waiting for another message's
  commit.
- **Tails did not improve measurably on this host.** A 1 to 15 s disk stall
  delays everything queued behind it, whatever the client does. Group
  commit drains the backlog after a stall in a few commits instead of one
  per message (the mean batch grew to 3.3 events in one D run), but the
  stall itself sets the p99.
- **One busy room no longer saturates on slow commits.** Under heavy load,
  one room at 60 msgs/s saturated the before build (p50 3.2 s, then a
  collapse), while the after build kept its median at 10 to 39 ms. At low
  host load the before build did not saturate (p50 26 ms), so this is
  headroom when commits are slow, not a capacity number.
- **Connection churn fell** from 153 to 210 new connections per A run to 55
  to 65, and from 604 to 39 per B run (sign-in included).
- **Prepared statements fell to zero**: about 16,800 per A run before. Per
  query, the server sees one request instead of two plus a Close packet.
- **Batches are small at these rates**: about 1.2 events per commit in A
  and 1.3 to 1.8 in C and D. MySQL already merges concurrent
  commits of different rooms into one fsync (binlog group commit), so the
  fsync count did not change; per-room batching matters when one room's
  messages queue behind a slow commit.

**Defects the measurement found, fixed in section 1b.**

- The first group commit build lost 13 and 322 deliveries in two D runs with
  no disconnect. A batch reaches each stream as one burst, and the hub
  skipped any subscriber whose 64-event buffer was full, silently. realtime
  now closes such a stream (the client catches up from history) and writes
  every queued event before one flush.
- The next build gave each batch commit a 10 s timeout. One A run hit a
  stall longer than that: 16 commits timed out client-side but committed
  server-side, and the re-seeded counter then collided with those rows (383
  sends answered 500). A commit now waits for its real outcome.

## One large room over channels (channels slice 4, 2026-10-30)

The delivery acceptance of `docs/prd/2026-10-28-channels-and-threads.md`
(§2.4): one room of 1,024 members spread over 16 channels, 100 msgs/s
across them, each member with one channel open. Same laptop and compose
stack as above (default MySQL settings).

```sh
bnbloc-loadtest --gateway http://localhost:17090 --forwarded-for \
  --members 1024 --rooms 1 --channels 16 --rate 100 --duration 60s --ramp-up 20s --settle 20s
```

| Run | Build | Deliveries/s | Delivery p50 / p95 / p99 / max (ms) | POST p50 / p99 (ms) | Lost | Disconnects |
| --- | --- | --- | --- | --- | --- | --- |
| 16 channels, lanes | presence snapshot broadcast | 772 | 11,334 / 16,647 / 18,481 / — | 9,915 / 14,902 | 6,439 of 52,739 | 22,455 |
| 16 channels, lanes | snapshot per stream | 6,400 | 9 / 202 / 266 / 1,591 | 10 / 253 | 0 of 384,000 | 0 |
| 1 channel (every stream gets every message) | snapshot per stream | 102,062 | 62 / 1,433 / 5,403 / 39,050 | 63 / 3,068 | 8,810 of 6,132,736 | 1 |

- **The first build stormed before a message was sent.** Every arrival
  broadcast a presence snapshot of every member to every stream: n
  snapshots of n ids per arrival. At 1,024 members the streams overflowed
  their buffers during the ramp-up, reconnected, and arrived again. A
  stream now writes its own snapshot when it opens; arrivals and departures
  stay one small event each (`docs/bugs/2026-10-30-presence-snapshot-storm.md`).
- **Lanes carry the room.** With each member's own channel in full and the
  other 15 as `channel_activity` (at most one a second each), 1,024 streams
  took 100 msgs/s with no loss and no reconnect, and every stream heard of
  every other channel (the badges' source). The same room with everyone in
  one channel is 16 times the deliveries and its tail goes to seconds.
- **The p99 is the commit, not the fan-out.** Delivery p99 (266 ms)
  follows the POST p99 (253 ms), as in the baseline above: the target of
  100 ms is not met on this disk; the fan-out adds about nothing.

## What a message costs the database (2026-10-30)

Latency on a laptop moves with whatever else the host runs; the number of
statements a message costs does not. It is the regression check of
`docs/prd/2026-10-30-message-path-performance.md`: read MySQL's counters
before and after two runs that differ only in length, and divide the
difference by the difference in messages.

```sh
# the compose stack's MySQL counters
stat() { docker exec bnbloc-loadtest-mysql-1 sh -c 'mysql -uroot -p"$MYSQL_ROOT_PASSWORD" -N -e "SHOW GLOBAL STATUS WHERE Variable_name IN (\"Com_select\",\"Com_insert\",\"Innodb_os_log_fsyncs\")"'; }
run() { stat > before; bnbloc-loadtest --gateway http://localhost:17090 --forwarded-for \
  --members 40 --rooms 1 --channels 4 --rate 10 --duration "$1" --ramp-up 3s --json > "run-$1.json"; stat > after; }
run 15s; run 75s   # (75 s minus 15 s) / 600 messages
```

`performance_schema.events_statements_summary_by_digest` (truncate it, run,
order by `COUNT_STAR`) says which statement a count comes from.

| Per message (40 members, 4 channels) | Before | After |
| --- | --- | --- |
| `SELECT` | 28.7 | 1.0 |
| `INSERT` | 2.0 | 1.0 |
| InnoDB log fsyncs | 4.5 | 3.2 |

Before, the selects were the room's sections (an access check per post and
per notification listener), notification preferences (one per listener per
message), the channel, the room, the follow list, a client-id lookup and the
channel's admission; the second insert was the poster's `channel_follows`
upsert, which cost more database time than the message. After, the room's
state is read from the owner's memory, preferences once per member per 30 s,
the client id from the hub, and the follow mark is written behind, only when
it changes something. The one select left is the room service's membership
check.

## Failover (2026-10-30)

`compose.ha.yml` runs the dev stack with replicas (realtime 3, relay 2,
room, activity and shareagent 2 each). `scripts/failover-check.sh` loads it
(300 members in 30 rooms of 3 channels, 50 msgs/s, 120 s) and meanwhile
drains one realtime replica (`docker stop`, SIGTERM), starts it again, kills
another (`docker kill`), and starts it again. It passes when every send is
stored, every delivery arrives, and every stream is open at the end; the
load tool behaves as the web client does (it resends a 5xx with the same
`clientMsgId`, reopens with `since`, reads history on `stream_gap`).

```sh
docker compose -f compose.yml -f compose.ha.yml up -d --build
scripts/failover-check.sh
```

| Build | Sends stored | Deliveries lost | Streams open at the end | Stream opens refused |
| --- | --- | --- | --- | --- |
| before (no drain, no resend, no handover) | 5,966 of 6,000 (34 answered 500/502) | 49 | 275 of 300 | 212 (500/502) |
| a dead owner's request sent on to the next, sends resent, shorter reconnect backoff | 6,000 | 10 | 300 | 203 (502) |
| + the new owner delivers what the previous one stored | 6,000 | 10 | 300 | 279 |
| + a stream resuming in a room that just changed owner reads the difference | 6,000 | **0** | 300 | 203 |
| + a draining replica leaves its peer set at once; new streams refused with 503 | 6,000 | **0** | 300 | 320 (503, retried) |

What each step fixed:

- **Sends failed** where a request reached a replica whose view still named
  the dead one as owner (502), or two replicas briefly both owned a room and
  one's commit hit the other's sequence (500). The forward now waits for the
  view to drop a peer it could not connect to and serves the request again
  through the new owner (nothing of it had reached the dead one); the web
  client and the load tool resend a 5xx with the same `clientMsgId`.
- **Streams stayed closed**: after a few failed reopens the client waited up
  to 30 s. The backoff now stops at 8 s.
- **Messages were lost** while the replicas' views disagreed (a replica
  joining is seen by the others up to 5 s apart: DNS is re-read every 5 s).
  A replica that still thought it owned a room stored a message whose live
  event reached nobody, and a client that resumed from its last sequence
  never learned of an earlier one the other owner had written. A replica
  that starts serving a room now reads the room's newest rows for 6 s and
  delivers those it did not, and a stream resuming in such a room is told to
  read the difference.
- **Drains cut new streams with 500s**: a stream opening on a draining replica
  failed on its own cancelled lookups. It is refused with 503 and
  `Retry-After: 1`, and a request cancelled by its client or the drain is
  logged as 499, not as a server error.

In Kubernetes a draining pod leaves its Service's endpoints within the 2 s
readiness period; in Compose the gateway keeps resolving it until it stops,
which is where the 503s come from.
