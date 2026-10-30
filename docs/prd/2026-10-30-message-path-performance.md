# The message path: one write, one encode, the rest from memory (2026-10-30)

Status: proposed (2026-10-30); the owner asked for the best solution, built
("性能问题，也是一个严重问题啊。一定要认真对待，找到最优解。看看dc, slack, tg都是怎么搞的？
他们感觉非常稳定啊"). Owner: the room stream and chat path
(`services/realtime`), the hub (`internal/realtime/hub.go`), the notification
producer (`internal/notify`), the web stream client (`web/src/lib/sse.ts`,
`web/src/lib/useRoomStream.ts`). Follows section 1b of
`docs/prd/2026-10-10-enterprise-scale.md` and slice 4 of
`docs/prd/2026-10-28-channels-and-threads.md`.

## 1. Why

The channels delivery acceptance (1,024 members in one room over 16
channels, 100 msgs/s, `docs/load-testing.md` "One large room over channels")
passed with no loss but missed its p99 (266 ms against 100 ms), and its
first run stormed (`docs/bugs/2026-10-30-presence-snapshot-storm.md`). On a
host under other load, the same run went to seconds. A product people work
in all day has to feel like Slack or Discord: a message appears at once, a
large room does not slow a small one, a reconnect is invisible.

## 2. How Discord, Slack and Telegram stay steady

Read from their engineering writing (2026-10-30):

- Slack, "Real-time Messaging" (2023-04): https://slack.engineering/real-time-messaging/
- Discord, "Maxjourney" (2023-10): https://discord.com/blog/maxjourney-pushing-discords-limits-with-a-million-plus-online-users-in-a-single-server
- Discord, "How Discord Scaled Elixir to 5,000,000 Concurrent Users" (2017): https://discord.com/blog/how-discord-scaled-elixir-to-5-000-000-concurrent-users
- Discord, "How Discord Stores Trillions of Messages" (2023-03): https://discord.com/blog/how-discord-stores-trillions-of-messages
- Telegram, "Working with Updates": https://core.telegram.org/api/updates

| Practice | Slack | Discord | Telegram | bnbloc today |
|---|---|---|---|---|
| One owner per conversation, its state in memory | Channel Servers "are stateful and in-memory", a channel mapped to one by consistent hashing | one process per guild holds its roles, channels and members; "some actions … require checking permissions, which requires knowing the user's roles as well as information about the roles and channels" | a message box per channel with its own sequence (`pts`) | the room's realtime owner holds subscribers, the sequence and the replay buffer, but every post re-reads the channel, its sections, the room and the member from MySQL |
| Persist, then fan out | Webapp stores the message, then the channel's Channel Server fans out to Gateway Servers | the API writes, then the guild process fans out | the server assigns `pts`, then pushes | the same: commit, then deliver |
| One write per message | MySQL on Vitess | ScyllaDB: insert p99 "from 5-70ms … to a steady 5ms" | own storage | two transactions per message (the message and a `channel_follows` upsert): 2 inserts and 4.5 fsyncs per message measured |
| Fan out only what is looked at | presence only "for a subset of users that are visible in the app screen" | passive sessions: "around 90% of user-guild connections in large servers were passive, resulting in the fanout work being 90% less expensive" | short low-priority updates for big chats (typing) | lanes (slice 4b): the open channel in full, the others as one `channel_activity` a second |
| Work per event done once, shared | Gateway Servers fan one message to their clients | Manifold: one send per remote node; FastGlobal: shared read-only data, lookups 7 µs to 0.3 µs | — | every stream decodes and re-encodes every event: 36% of realtime's CPU in the 1,024-member run was one per-stream payload decode |
| A lagging client is not a failure | Gateway Servers drain and resubscribe | Semaphore: bail early instead of piling up | "updatesTooLong indicates that there are too many events pending to be pushed to the client, so one needs to fetch them manually" | a full stream is closed; the client reconnects and is sent the whole replay buffer again |
| Resume from where the client was | — | sessions resume by sequence | the client keeps `pts` per box and asks for the difference (`getChannelDifference`) | a reconnect replays the room's recent buffer; the client does not say what it has |
| Measure per operation | — | the guild loop records count and time per message type; wall-time stack sampling | — | request histograms per route; no per-step timing, no profiler |

What the three agree on: **a message costs one durable write and one
encode; its fan-out reaches only who is looking; everything derived (who
follows, unread counts, presence, notifications) comes from memory or is
coalesced off the hot path; a client that falls behind fetches the
difference from its own position instead of reconnecting.**

## 3. What a message costs now (measured)

Local compose stack, `bnbloc-loadtest`, MySQL `performance_schema` digests.
A run of 40 members, 4 channels, 10 msgs/s; marginal cost per message
(a 75 s run minus a 15 s run, 600 messages):

| Per message | Count |
|---|---|
| `SELECT` | 28.7 |
| `INSERT` | 2.0 (`room_messages`; `channel_follows` upsert) |
| InnoDB log fsyncs | 4.5 |

Where the selects come from (600 messages): room sections 6,918 (access
checks: the post's own and one per notification listener), notification
preferences 5,220 (one per listener per message, read before the collapse
window is checked), channels 1,805, rooms 682 (the rules check), follows of
the channel 600, the client-id lookup 600, channel admission 600, members
541. The two inserts took 7.7 s of the database's 9.6 s; the `channel_follows`
upsert alone (4.3 s) cost more than the message (3.4 s).

Every count that is per listener grows with the channel: in a 1,024-member
default channel the notification producer would read sections and
preferences for every poster, for every message.

The 1,024-member run's realtime CPU profile (`go tool pprof`, 20 s): 36% in
`mentionsSubscriber` (each lanes stream JSON-decodes every chat payload to
look for a mention), 18% in `json.Marshal` (each stream encodes every event
again), 17% in write syscalls (a flush per stream per wake). Step timing of
`PostChat` under host load: the commit dominates, then each database round
trip before it (client id, channel, access, the follow upsert), each tens of
milliseconds at the tail.

Two client costs the load tool does not model yet: every `channel_activity`
schedules a `GET /v1/rooms-unread` from every client (1,024 clients, 15
active channels: thousands of unread queries a second), and every reconnect
asks for the whole replay buffer.

## 4. The design

**One rule: a message costs one durable write and one encode. Everything
else is read from the room owner's memory, or is coalesced and written off
the hot path.**

### 4.1 Room state at the owner (Slack's Channel Server, Discord's guild)

realtime's room owner keeps the room's state it decides with in memory: the
room (rules version, whether agreement is required), its channels (settings,
section, archive, kind), its sections (visibility, members), and each
member's role and rules agreement. Every read on the post path and in the
notification producer (`channelOf`, `MayReadChannel`, channel admission,
the rules check, the listeners' access) is answered from it.

- **Invalidated by the room's own events**, which already pass through the
  owner: `channel_updated`, `section_membership`, `member_joined`,
  `member_removed`, `room_deleted`, plus a new transient `room_updated`
  (room settings, rules, a member's agreement) the room service publishes.
- **A bounded age** (30 s) as the net for a lost event; a removed member's
  stream already ends on `member_removed`.
- **Dropped with the room** when ownership moves (`ReleaseRooms`).
- One owner: `store.LoadChannelAccess` stays the rule; the cache holds its
  inputs, never a second rule.

### 4.2 One write per message

- **Slow mode and "posted" from memory.** The owner keeps each member's last
  post per channel for slow mode (it is the room's only writer); the
  `channel_follows.last_posted_at_ms` mark (which makes "all" the default
  level, posting joins the channel) is written behind, coalesced: one
  upsert per (member, channel) at most every few seconds, off the request.
  A restart forgets in-flight slow-mode windows (a member may post once
  early after a deploy) and at most a few seconds of "posted" marks.
- **No client-id pre-read.** The hub's batch insert already answers a
  stored client id with its stored sequence (section 1b, "History
  duplicates retry the rest"); the read before it goes.
- The message insert stays the one durable write, with the per-room group
  commit.

### 4.3 Fan-out: once per event, only to who is looking

- **Encode once.** The hub encodes each event to its SSE frame once at
  delivery; every stream writes those bytes.
- **Who it mentions, once.** The chat path records the message's mentioned
  member ids and broadcast reach on the event (not serialized); lanes read
  it instead of decoding the payload per stream.
- **A room's delivery holds the room, not the hub.** Subscribers are kept
  per room under the room's own lock, so a 1,024-stream room never holds up
  another room's delivery or a subscribe.
- **Presence coalesced.** Joins and leaves are gathered per room and sent
  as one `presence_delta` a second (who came, who went); a stream still
  writes its own snapshot on opening.
- **Commits stay one batch at a time per room** (measured, §10): what
  queues behind a commit goes in the next batch.

### 4.4 A lagging or returning client fetches the difference (Telegram)

- **Resume by sequence.** A stream opens with the last sequence its client
  holds (`since=<seq>`, the SSE `Last-Event-ID`'s value); the replay buffer
  sends only what is newer. When the buffer does not reach back that far,
  the stream says so (`stream_gap`) and the client reads the difference
  from history, as Telegram's `getChannelDifference`.
- **Overflow is a gap, not a hang-up.** A stream that falls a whole buffer
  behind drops its queued events, keeps its connection and is sent one
  `stream_gap` with the last sequence; the client fetches the difference.
  Only a stream that cannot take even that is closed.
- **Unread from the activity itself.** `channel_activity` carries the number
  of new messages since the previous one; the client adds it to the
  channel's badge (deduplicated by `lastSeq`) instead of re-reading every
  room's unread. The REST read stays for opening a room and after a gap.

### 4.5 Notifications without a read per recipient

- The collapse window (one `channel_message` a minute per member and room)
  is asked before anything is read; a collapsed recipient costs nothing.
- Preferences are cached per member for 30 s on the replica that produces
  the room's notifications; a change saved on that replica drops the entry
  at once, one saved on another takes effect within 30 s. Listeners' access
  comes from 4.1; the channel's followers are read once per channel and
  kept 10 s.

### 4.6 Measure it, always

- `realtime_chat_step_seconds{step}`: the post path by step (access,
  admission, commit wait, commit, delivery), the measure Discord used to
  find its hot spots.
- A profiler on the internal metrics listener (`BNBLOC_PPROF=1`), never on
  a public one.
- `docs/load-testing.md` gains the per-message statement count (the
  digest procedure above) as the regression check: it does not depend on
  how loaded the host is, the latency does.

## 5. Targets

| Measure | Now | Target |
|---|---|---|
| `SELECT` per plain message (post path and notifications) | 28.7 | ≤ 1 (amortized) |
| `INSERT` per message | 2 | 1, plus coalesced follow marks |
| fsyncs per message | 4.5 | ≤ 2 (one commit, shared by a batch) |
| realtime CPU per delivered event | per-stream decode and encode | one encode per event |
| `GET /v1/rooms-unread` per `channel_activity` per client | 1 | 0 |
| Reconnect cost | the whole replay buffer | only what the client lacks |
| 1,024 members, 16 channels, 100 msgs/s, quiet host | p99 266 ms | p99 under 100 ms (the commit), no loss, no reconnect |

## 6. Placement

| Part | Place | Why |
|---|---|---|
| Room state cache, slow mode in memory, write-behind marks | Platform (realtime) | The realtime contract and isolation: the access rule's inputs |
| Encode once, per-room delivery, presence deltas, lanes mentions | Platform (realtime) | The realtime contract |
| Resume by sequence, `stream_gap`, local unread | Platform (realtime, web) | The stream contract |
| Notification collapse first, preference cache | Platform (notify) | Who is told |
| Step metrics, profiler | Platform (obs) | Observability |

Name test: room state, frame, gap, delta, resume. No use case.

## 7. Design review

| Lens | Answer |
|---|---|
| Actors | The cache answers for people, agents (as their requester) and previews exactly as the store does: the same `LoadChannelAccess` over cached inputs. |
| Time and lifecycle | Restart: caches empty, slow-mode windows and a few seconds of "posted" marks forgotten (said in §4.2). Ownership move: the room's state dropped with its subscribers. |
| Scale and cost | Per-message work no longer grows with the channel's listeners on the database; memory per room is its channels, sections and members (a 1,024-member room: well under a megabyte). |
| Failure and recovery | A lost invalidation is bounded by 30 s; a stream that overflows recovers by the difference without reconnecting; a write-behind failure is retried and logged. |
| Boundaries and privacy | The cached access is the rule's input, invalidated by the events that change it; a removed member's stream ends on `member_removed` as today; a 30 s net is the worst case for a section change whose event was lost (pending owner). |
| One owner | `LoadChannelAccess` stays the one rule; the hub owns encoding; the activity payload owns the unread delta. |
| Release and compatibility | `room_updated`, `presence_delta`, `stream_gap` are new kinds (an old web ignores them; it re-reads unread on activity as today). `since` is optional. An old realtime pod ignores `since` and replays as now. |
| Observability | §4.6. |
| Pre-mortem | A stale cache admits a post to a channel a member just lost (bounded, and the read paths re-check); a coalesced mark lost on a crash makes a member's default level "mentions" for a channel they posted in once (acceptable); a client that misses a `stream_gap` keeps a stale badge until it opens the channel (the REST read on open trues it up). |

## 8. Slices

1. **Measure**: step metrics, the profiler, the statement count in
   `docs/load-testing.md`; a re-run as the baseline.
2. **The cheap ones**: no client-id pre-read; mentions once per event;
   encode once; notification collapse before preferences, a preference
   cache; local unread from `channel_activity` (with a message count).
3. **Room state at the owner** (4.1) and one write per message (4.2).
4. **Fan-out**: per-room delivery lock, presence deltas.
5. **Resume and gaps** (4.4).
6. Re-measure each slice on the same stack; record before/after in
   `docs/load-testing.md`; then the QA run (§9).

## 9. Decisions pending owner

- **P1.** Run the acceptance load on QA (real database and disk) once
  slices 1 to 3 are in, after hours, with its own sandbox room and tokens?
  The laptop's disk and the host's other load set the tail here; QA's
  managed MySQL is the number that matters.
- **P2.** The 30 s bound on a lost invalidation (§4.1, privacy lens).
- **P3.** Slow-mode windows and "posted" marks may be forgotten on a
  restart (§4.2).

## 10. Progress

**Slices 1 to 3** (2026-10-30):

- Measure: `realtime_chat_step_seconds{step}` (access, admit, commit,
  after), `realtime_feed_commit_seconds`, `realtime_feed_deliver_seconds`
  (a histogram series in `internal/obs`); Go's profiler on internal
  listeners with `BNBLOC_PPROF=1`.
- No client-id read: the hub remembers each room's recent client ids
  (`Hub.RecentClientMsg`); an older retry is answered by the commit.
- Lanes read a message's text without a JSON round trip
  (`wire.ChatAuthorText`); the hub encodes each event once
  (`wire.Event.Encoded`) and every stream writes those bytes.
- Notifications: the collapse window is asked before preferences; the
  producer keeps preferences 30 s (`notify.prefsCache`), a change saved on
  the same replica drops its entry.
- `channel_activity` carries `messages`; the web adds it to the badge once
  per `lastSeq` (`web/src/lib/channelActivity.ts`) and no longer re-reads
  every room's unread on every activity.
- Room state at the owner (`services/realtime/room_state.go`): channels,
  default channel, sections, members' sections, admission, the room, its
  members and a channel's followers, read through `channelStore()` and
  `roleStore()`; dropped by `channel_updated`, `section_membership`,
  `member_joined`/`member_removed`, `thread_updated`, `room_deleted`, the new
  `room_updated` (room settings and rules, a member's agreement) and
  `channel_follow` (internal: never streamed), at most 30 s old, never a miss
  or a failed read.
- One write per message: slow mode from the owner's memory (the store's mark
  read on a miss while slow mode is on, so it holds across a restart); the
  follow mark written behind and only when it changes something (a first
  post in a channel since the owner started, a post after leaving it, or
  every post where slow mode needs its time).

Measured (40 members, 4 channels, 10 msgs/s, marginal per message):

| Per message | Before | After |
|---|---|---|
| `SELECT` | 28.7 | 1.04 (the membership check, the room service's) |
| `INSERT` | 2.0 | 1.0 |
| InnoDB log fsyncs | 4.5 | 3.2 |

The 1,024-member run (16 channels, 100 msgs/s, host load 20 to 60): no
loss, no disconnect, every stream heard of every channel; per message the
access step fell to 0.1 ms and admission to 0.01 ms (mean); what is left is
the commit: 17.5 ms mean wait for the room's batch (6.6 ms per commit,
1.18 events a batch), which sets the tail (delivery p99 622 ms on this
host).

**Slices 4 and 5** (2026-10-30):

- Resume by sequence: a stream opens with `since` (the web keeps the last
  sequence it received and sends it on every reconnect); the replay sends
  only what is newer (`Hub.SubscribeSince`). When the room's buffer does
  not reach back (freed when the room had no stream, evicted, or a new
  owner's), the stream sends `stream_gap` first.
- Overflow is a gap: a stream that falls a whole buffer behind once keeps
  its connection and is sent `stream_gap` with the last sequence it wrote
  (`realtime_sse_gaps_total`); a second overflow before it took the first
  closes it, as before. The web, on `stream_gap`, merges the open channel's
  latest page (`useRoomFeed.catchUp`) and re-reads unread and jobs, without
  reopening; the load tool reads history the same way and counts it.
- Presence deltas: arrivals and departures go out as one `presence_delta`
  a second per room (`presence_join` and `presence_leave` are gone; an
  arrival and a departure in one window cancel).

**Measured and not done: pipelined commits.** Committing a room's next
batch while the previous one is still in flight (MySQL merging their
fsyncs) was built and measured in a hot room (200 members, one channel,
100 msgs/s): each batch's commit took 3 times longer (concurrent inserts
into one room's index), the wait did not fall, and the burstier delivery
overflowed streams on a loaded host. Reverted; batches of a room commit one
after another.

**Measured and not done yet: the per-room delivery lock.** Delivering a
batch to 1,024 streams holds the hub's lock 0.8 ms (mean); worth splitting
when one replica serves many busy rooms at once, not before.

What is left of the tail is the commit on this host's disk. The number
that matters is QA's (P1).
