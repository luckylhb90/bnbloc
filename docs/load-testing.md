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
   holds, as `web/src/lib/sse.ts` `subscribeRoom` opens it:
   `GET /v1/rooms/{id}/events?access_token=…&channelId=<default>&allChannels=1&includePeople=1`.
   The streams count as present members and receive presence events. They
   open evenly over the ramp-up. A dropped stream reopens with the web
   client's backoff: 1 s doubling to 30 s, with jitter.
4. **Sends.** After the ramp-up it sends chat at the target total rate, the
   way `api.sendChat` does: `POST /v1/rooms/{id}/chat` with `text`,
   `atUnixMs`, a unique `clientMsgId` and the default `channelId`. Sends go
   round-robin over a shuffled list of every (room, member) pair, so each
   member's rate stays as low as the population allows. The send loop is
   open-loop: a send is due by the clock, not when the previous one answered.
5. **Measurement.** The tool notes which of the room's streams were open
   when each send started. Every `chat_message` that arrives on a stream is
   matched to its send by `clientMsgId`. The first arrival on each expected
   stream is one latency sample, from send start to arrival. The sender's own
   stream counts too: that is the echo the web client renders.
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
    reconnects;
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
  isolation):
  - The stores open at most 20 connections and keep 10 idle. A 30 s run at
    50 msgs/s opened 165 new MySQL connections.
  - Every query is a server-side prepared statement, so each one costs
    Prepare, Execute and Close round trips. A chat message costs about five
    of them.
- **Sign-in.** It took 29.5 s for 1000 members with `--forwarded-for` and 16
  parallel sign-ins. Each sign-in writes a refresh-token family.
