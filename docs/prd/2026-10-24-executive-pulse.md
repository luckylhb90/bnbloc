# Pulse: an organization board for leadership (2026-10-24)

Status: designed. Prototype (sample, simulated data) in the live-monitor-demo
folder, outside this repository. Not built. Owner: this repository for the
read model, the access rule and the API; the board itself is a sample in its
own repository (§6). Audience: executives and org admins, not engineers. The
engineering view is `docs/prd/2026-10-24-live-monitor.md` and stays separate.

## 1. Why

A leader asks four questions, and today nothing answers them in one place:

1. Are people using it, and who is still not? (adoption)
2. What is it producing? (outputs)
3. What is it costing, and is that on track? (spend)
4. What is happening right now, and where? (presence)

What exists, checked in the code:

- The team agent dashboard (F1, `services/activity/team.go`) shows org admins
  every member's agents, turns and cost. It is an operations view of agents,
  not an organization view.
- Usage reports and budgets are tables: `usage_budgets` (per member, tokens and
  cost, warn threshold), `tenants.cost_allowance`.
- Cost is reported only when the core reports it. `costUsd` is omitted when no
  source exists ("absent cost is not zero cost", `internal/wire/agent_runs.go`).
  Some cores report no usage at all (`docs/prd/2026-10-24-context-usage-meter.md`).
  So any spend number has a coverage, and the board must show it.
- Department exists from directory sync (`org_sync_subjects.department`). Member
  location does not exist anywhere in the schema, and this design does not add it
  by default (§4).

## 2. What it shows (the end state)

### 2.1 Definitions (one catalogue; §5 "One owner")

| Metric | Definition | Source |
|---|---|---|
| Active people | Distinct members with a meaningful action in the window: a message sent, an agent run requested, an artifact version or app document written. A login or an open tab is not activity. | events on room messages, `agent_jobs`, `artifact_versions`, `app_docs` |
| Adoption | Weekly active members / members in the organization, with the list of departments below k (§7) shown as "under 5". | `tenant_members`, `org_sync_subjects` |
| Deliverables | Artifacts created, and new versions, per day. | `artifacts`, `artifact_versions` |
| Published | Items the organization made visible (an org-public app or document, a published content component). Only these are ever shown by title. | `app_docs.public`, publish flow |
| Agent work | Replies completed (`agent_jobs.status = replied`); success rate = replied / (replied + failed + timed_out) over finished jobs; median duration from `started_at_ms` to `finished_at_ms`. | `agent_jobs` |
| Tokens | Input + output (and cache) tokens reported by replies, per day and per minute. Reported tokens only; a turn that reports none adds nothing. | reply metrics |
| Cost | Sum of `costUsd` reported, labelled "≈" and with coverage: "cost reported for 86% of replies". | reply metrics |
| Allowance | `tenants.cost_allowance` against the month's reported cost, with the forecast from the month so far. | `tenants`, rollups |
| People now | Distinct members connected to the product now, from presence, by zone (§4). Shown only where k ≥ 5. | realtime presence |
| Agents working now | Jobs in a live phase (`claimed`, `running`, `reflowing`) whose lease has not expired. A job that is `queued` is waiting, not working, and is counted apart as "waiting". Peak of the day shown beside it. | `agent_jobs` status and `lease_expires_at_ms` |

"Time saved" is not a default metric. It needs the organization's own
assumptions (minutes per deliverable, say), entered by an admin and labelled as
their estimate. The board never invents it.

### 2.2 The screen

```
+------------------------------------------------------------------------------+
| Pulse · Acme (simulated)            ● LIVE  14:32  [geography: off]  [Lobby]  |
+------------------------------------------------------------------------------+
|  PEOPLE NOW  412      WEEKLY ACTIVE  78%  ▲     DELIVERABLES TODAY  1,284      |
+-----------------------------------------+------------------------------------+
|                                         |  TOKEN PUMP                         |
|     dotted globe                        |  ┌──────────────┐  1.84 M today     |
|     day/night terminator                |  │ ░░ bubbles ░░│  +24k / min       |
|     pulses where work starts            |  │ ▓▓▓▓▓▓▓▓▓▓▓▓│  ≈ $412 reported  |
|     (country labels: geography on,      |  │ ▓▓▓▓▓▓▓▓▓▓▓▓│  coverage 86%    |
|      only where k ≥ 5)                  |  └──────────────┘  budget ▓▓▓░ 62%  |
+-----------------------------------------+------------------------------------+
|  AGENTS AT WORK  ▣▣▣▣▣▣▣▣ 37 working  ·  6 waiting  ·  peak today 58          |
|  (a tile per live job; lit on claim, dark on finish; no labels)              |
+-----------------------------------------+------------------------------------+
|  OUTPUT · deliverables/day sparkline    |  AI BRIEFING (draft, numbers only)  |
|  AGENT WORK · replies 9.2k · ok 98.1%   |  "Output is up 14% on last week…"   |
+-----------------------------------------+------------------------------------+
|  ticker: Sales published a deck · Support closed 40 threads · …  (k-safe)     |
|  showcase wall: titles of org-published items                                 |
+------------------------------------------------------------------------------+
```

Two modes. **Lobby**: full screen, large numbers, panels cycle slowly, no
controls beyond the mode and the geography switch. **Desk**: the same numbers
with a window selector (today, 7 days, month), a table twin of every chart,
and an explanation of each definition on hover or focus.

### 2.3 Every light means a number

The look is "mission control at night", and the rule is that nothing glows for
decoration. A light, a pulse or a glow is bound to a real measurement:

- A pulse at a place is at least one qualifying event there in the last few
  seconds, and its brightness follows the active count (log scale, so one large
  place does not wash out the rest).
- The night side of the globe is the real sun position at the org's clock. It
  carries no data, and it is drawn quieter than the day side so it does not
  read as a metric.
- A ticker line appears only when its group passes k (§7).
- Milestones (for example 10,000 deliverables this month) are organization
  totals, shown once, and never about one person.

### 2.4 Agents at work

The effect: a field of tiles, one per agent job that is working right now. A
tile lights when its job is claimed, breathes while it runs, and goes dark when
the job finishes. Queued jobs appear as dim tiles at the edge, waiting for a
bridge. The count is the number on screen, and the number is exact.

- **Count means live jobs.** A tile is a job in `claimed`, `running` or
  `reflowing` with an unexpired lease. Leases are renewed by the bridge while
  the job runs, and a sweeper reclaims a lease that lapsed (a bridge that
  crashed or went offline), so a dead bridge cannot leave a lit tile behind. A
  tile that stays lit beyond its lease is a bug the board must not hide: the
  count drops on the sweep, and the sweep is visible in the audit.
- **Events, not polls.** A tile lights on the claim event and goes dark on the
  finish event, so the board's count is the true concurrency at each moment. The
  peak line over the day is the maximum of that step function, not an average.
- **Scale of the picture.** Up to 200 tiles are drawn one for one. Above that,
  the field draws 200 tiles scaled to the count and writes the exact number
  beside it; the picture never pretends a count it does not have.
- **Waiting is shown separately.** Queued jobs older than a set age (a
  threshold the org sets, default 60 s) raise a visible "waiting too long" note
  with their count. Waiting is a capacity signal: it means bridges are short,
  and the board says so rather than letting the reader infer it.
- **Privacy.** A tile is not a person, room or agent name. Tiles carry no label.
  A department's share of the field appears only above k (§7); the field's
  total is organization-wide.
- **Connection to the pump.** Concurrency and tokens per minute are shown on
  the same time axis, so the reader sees whether more agents mean more
  reported work. The pump and the field use the same rollup, so they cannot
  disagree about a minute.
- **Motion safety.** The breathing is a slow, low-amplitude change in opacity,
  with no more than one change per tile per second. `prefers-reduced-motion`
  shows the tiles lit without breathing.

Why this matters: a leader can see at a glance whether the organization is
using its agents now, and whether work is queuing behind them. The picture is
only as honest as the lease and the event rules above, so those rules are the
acceptance tests, not the drawing.

### 2.5 People online

The effect: a swarm of soft points, one per person connected right now, drifting
inside a small field. The count is the number of points, and the number is
exact, but the field is what the eye reads.

- **Points are a representation, not identities.** The swarm draws the count
  (scaled above 300, with the exact number beside it, as with the agent field).
  A point is not a person, carries no label, and is never clickable to a name.
- **Joins and leaves are events.** A join flies a point in from the field edge
  and gives a short ring at the centre. A leave fades one point out. A ring
  appears on every five-person change, so a burst of arrivals reads as a burst.
  The rings come from the presence events, not from polling.
- **Presence is connected, not idle.** "Online" means a live presence stream to
  the product, as the realtime service counts it (`services/realtime`), with a
  grace period so that a flaky network does not make people flicker out. The
  grace period is part of the definition on the board.
- **Density where it matters.** Where geography is on (§4) the swarm's points
  cluster toward the places with live presence; where it is off, the swarm is
  uniform. Below k (5 people) the field shows no points at all and reads
  "under 5".
- **Motion safety.** Drift is slow and low-amplitude; no point flashes; no more
  than one ring per second. `prefers-reduced-motion` shows the points still, with
  rings replaced by a single number change.

Why this matters: a leader sees the organization's attention as it moves. The
swarm shows a crowd forming and thinning without showing anyone who is in it.

### 2.6 The three headline figures

Each headline figure has its own effect; none is a bare number.

| Figure | Effect | Data it is bound to |
|---|---|---|
| People online | The swarm (§2.5) | Presence count, k-safe |
| Weekly active | An arc that sweeps to the share of members active this week, with a light travelling along its tip; the arc only moves when the rollup moves | Weekly active / members (§2.1) |
| Deliverables today | A stack whose height follows the day's count on a log scale (capped). Each new deliverable lands one brick, with one shine sweep; the bricks that land are the real events, the stack height is the count | Deliverable events, org-wide |

Each of these changes only when its data changes. A landing brick is one real
deliverable event. The stack height is a log-scaled reading of the count, not a
brick per deliverable: at a thousand-plus a day, a brick per deliverable cannot
be drawn, and the board must not pretend it can.

### 2.7 The token pump

The request is that the token counter "pumps up" in a way that feels alive. It
must not lie to make that happen. The contract:

- **Steps, not ticks.** Tokens arrive as reply usage, once per completed reply
  (as the reply metrics report them). The board does not animate per token.
  Each reported reply adds its tokens to the counter with an ease-out roll of at
  most about 1.2 s, so the number climbs in visible steps and never runs ahead
  of the data.
- **Rate drives the pump.** The fluid column rises and falls with the tokens per
  minute over the last 60 s (log scale), and bubbles rise at a speed set by the
  same rate. An idle org shows a still column, not a fake drip.
- **In flight is shown, not counted.** A running turn that has not reported
  usage yet appears as a shimmer at the column's surface, labelled "running";
  its tokens join the counter only when reported.
- **Coverage is part of the picture.** A lane per core shows whether it reports
  usage. A core that reports nothing is a grey lane marked "not reported", never
  a zero. The total carries "≈" and a coverage figure.
- **Cost moves only on `costUsd`.** The cost roll is driven by reported cost,
  with the same step rule, and the coverage sits next to it.
- **Budget.** Where `cost_allowance` is set, the gauge fills against it, and the
  colour changes at the org's warning threshold (the existing `warn_at_percent`
  pattern). Forecast to month end is a line on the gauge, labelled as a
  forecast.
- **Motion safety.** `prefers-reduced-motion` replaces the roll with a plain
  update and stops the bubbles. No element flashes faster than 3 times a second.

Why this matters for the decision: the pump gives a sense of scale and speed.
The number under it is still exact, reported, and labelled with its coverage.
A board that pumps faster than the data is the failure this section forbids.

## 3. Access

- A new grant, `read_org_pulse`, given by an org admin to named people (an
  executive, a board observer). It is read-only and sees only the aggregates in
  §2.1. It does not imply `manage_org`.
- Org admins hold it. Super admins do not get an organization's Pulse by
  default; that would need a platform reason of its own.
- Every session that opens the Pulse is recorded in the audit log (who, when,
  which organization), not every view.
- Lobby screens are shared displays. They sign in as the executive, lock after
  an idle period with no interaction, and show no more than the Desk view
  does. The screen is not a way around k (§7).

## 4. Geography: three options, one default

| Option | What it shows | Privacy cost | Decision |
|---|---|---|---|
| A. Country from the member's device time zone | Countries light up with people now and work starting | Coarse, but location-adjacent. Needs an org opt-in and an employee notice | Later, opt-in, default off |
| B. Org sites (offices with coordinates, members assigned by a directory attribute) | Office dots | Reveals where people are assigned, not where they are; needs the directory attribute | Later, if the org has the attribute |
| C. No geography: follow the sun | Day/night terminator and bands of working hours, no labels | None beyond aggregate counts | **Default** |

Option A design, if enabled: the web or desktop client sends its IANA zone name
(for example `Europe/Berlin`) once per session. The server accepts only a
well-formed zone name and keeps only counts: distinct members per zone in a
HyperLogLog keyed by an HMAC of the member id, so no member id is stored. The
zone-to-country table is the public tzdata `zone.tab` (a fact about the outside
world, so it is data, not platform logic, per AGENTS.md). A zone that is not in
the table, or `Etc/*`, counts as "other" and is never shown. Zones in the same
country are added together on the board. Zone counts below k are suppressed
before the merge, and the board says "approximate".

## 5. Architecture

- **Events.** The platform already records the facts: room messages, agent jobs
  and their status changes, artifact versions, app document writes, reply usage.
  Pulse adds one consumer, not new writes to product tables.
- **Read model.** One aggregator per organization writes **minute rollups** into
  a `pulse_minutes` table: `(tenant_id, minute, metric, dimension, value)`, where
  dimensions are coarse only (department, kind, core, zone). Distinct people are
  counted with HyperLogLog, so the table holds no member ids and no per-person
  rows. Cardinality is capped: departments beyond the top 30 fold into "other".
- **Live window.** The last 15 minutes live in memory and in Redis (a separate
  keyspace `pulse:`, sharing the Redis that the ops monitor PRD introduces, and
  adding no dependency to the product path). One aggregator fans the snapshot
  out over SSE to every open board; a board never queries the database per
  refresh.
- **API.** `GET /v1/tenant/pulse/snapshot` (for the first paint and for a reload)
  and `GET /v1/tenant/pulse/stream` (SSE, one snapshot every 2 s). Both gated by
  `read_org_pulse` at the gateway, the same way the team dashboard is gated by
  `manage_org`. Responses are the rollups after k is applied, never raw counts.
- **Rollup latency.** A minute is "settling" for up to 5 minutes (late events
  from jobs that finish late). The board marks settling minutes and never shows
  them as final.
- **Restart and gaps.** Rollups are idempotent by event id, so a replayed event
  adds nothing. A missing range is a visible gap, never a zero.

## 6. The board (sample)

The board is a sample app in its own repository, built on the public API and
the snapshot contract, like the engineering monitor. Its presentation
(globe, pump, colours, layout, sound if any) is the org's to change. The platform
ships none of those names.

The AI briefing and "ask the board" use the platform's existing assist seam
(`docs/architecture/ai-assist.md`), with the organization's own model budget.

## 7. Privacy and governance

- **Threshold k = 5.** Every displayed cell (a department, a country, a zone, a
  ticker group, a showcase count by department) is shown only with at least 5
  distinct people behind it. Below that the cell reads "under 5" and its value
  is not sent. Totals are computed before suppression. Complementary suppression
  is applied so a suppressed cell cannot be recovered by subtracting the shown
  ones from a total (a test pins this).
- **No individuals.** No names, no avatars, no per-person ranking or
  leaderboard, no "top users". Ranking teams is allowed only above k, and the
  product does not offer a ranking of people at all.
- **No content.** Room names, message text, artifact titles that are not
  org-published, and app data never reach the board. Showcase titles are shown
  only for items the organization published, and an admin can withdraw one.
- **Not for evaluation.** The organization's page states the purpose and what
  the numbers are not: activity counts are not a measure of an individual's
  performance. The organization may adopt its own policy; the product states
  this default.
- **Transparency for members.** Every member can open a page that lists what
  the Pulse aggregates, the threshold, and whether geography is on. Turning
  geography on is an org admin action that the page announces to members.
- **Legal.** Employee monitoring rules differ by jurisdiction and may require
  notice or consultation. This PRD does not settle that; it is an open decision
  (§12).

## 8. AI capability

- **Daily briefing.** A short paragraph written from the rollups: what changed
  against last week, where spend is running against the allowance, and any
  anomaly (for example, a failure-rate rise in one department above k). The
  input is numbers and kind labels, never content. It is marked "draft" and a
  person reviews it. With the model off, the board shows a templated sentence
  built from the same numbers, and says that the AI part is off.
- **Ask the board.** A question ("how many deliverables did Sales publish this
  quarter?") is mapped to a whitelisted metric and a window. The answer shows
  the query it ran, so the reader can check it. A question that matches no
  metric gets a clear "not available" answer. Questions are untrusted text and
  never become SQL or prompts with authority.
- **Explain a movement.** The assist may propose a reason for a change from the
  reason codes (for example, `quota_exhausted`), never from content.
- **Not used for tiles or the pump.** A number has no decision in it, so there
  is nothing for a model to add there.
- **Cost and fallback.** The organization's own budget and off switch. The
  board works without the model.

## 9. Placement table

| Part | Content / component / platform | Why (platform: the named reason) | What an organization would swap |
|---|---|---|---|
| Metric catalogue and definitions | Platform (one file, read by rollups, API, AI whitelist and the UI's definitions) | One owner of what "active" means; contracts | The definitions, through the catalogue only |
| Minute rollups, HyperLogLog counting, k-threshold, complementary suppression | Platform | Events only the platform sees; consent (privacy thresholds are enforced at the source, not in a client) | Nothing; the threshold is a platform rule |
| Read API and SSE stream, `read_org_pulse` grant and its gate | Platform | Identity and the audit row for each open | The grant's holders |
| Presence zones and the HMAC for member ids | Platform (mechanism) | Events only the platform sees (presence); the mapping of zones is data | Nothing; geography stays off by default |
| Zone-to-country table | Data (tzdata `zone.tab`) | A fact about the outside world | Another table |
| Reply usage, cost, coverage per core | Component data (each core reports what its agent gives) | Facts about one agent's output format; the platform names no core | The core |
| Board UI: globe, pump, colours, layout, lobby mode | Sample, own repository, on the public API | A presentation an org changes; no platform code names a panel or a colour | The org's own board |
| Daily briefing and ask-the-board | Assist on the platform seam | An AI capability on the assist contract; the model is the org's | The org's model |
| Live-job count, lease renewal and the lease sweep (what "working" means) | Platform (already exists in the job queue) | Events and leases only the platform sees; the count is the queue's own truth | Nothing |
| Agents-at-work field (tiles, breathing, layout) | Sample, in the board | Presentation of a count; no platform code names a tile | Any picture of the count |
| Milestones and celebration copy | Sample copy in the board | Presentation | Any copy |

Name test: `grep -ri pulse|globe|pump` in platform code returns nothing outside
the catalogue's neutral metric names. Fork test: an organization that wants
Power BI reads the same API and changes nothing in the platform. Replace test:
a second board installs beside this one.

## 10. Design review

| Lens | Answer |
|---|---|
| Actors | Readers: executives and org admins with `read_org_pulse`; super admins not by default. Members: contribute to counts, see only the transparency page. Lobby screens: a signed-in executive's display, idle lock, same aggregates. The AI model: sees rollup numbers and reason codes only. A visitor on a shared display sees what the executive sees, so it needs the lock. |
| Time and lifecycle | Live window 15 min; minutes "settling" for 5 min; day and month boundaries in the org's time zone; the forecast uses the month so far. A late job event lands in its finished minute and is marked settling until then. A replayed event is idempotent. A restart rebuilds the live window from the rollups. An executive whose grant is removed loses the stream on the next refresh. |
| Scale and cost | Rollups: metrics × coarse dimensions, with departments capped at 30 and zones capped by the table. The live window is one aggregator per org, shared by every open board. HyperLogLog keeps distinct counts at fixed size per cell. Board clients: a few per organization, not per member. |
| Failure and recovery | A bridge that dies mid-job: its lease lapses, the sweeper reclaims the job, and the agents-at-work count drops; the board never keeps a tile lit past its lease. Aggregator down: the board shows "data delayed since …" and freezes the frame, never zeros. Redis down: presence and the live window stop, and the rollups still answer the snapshot with a gap marked. Reply usage missing: the pump shows "not reported" for that core, never zero. Model down: the briefing falls back to the template. |
| Boundaries and privacy | k = 5 at the source, not in the client; no member ids stored; HMAC on the zone counts; no content, no names, no room names; org-published items only for titles; audit per session open; members can read exactly what is aggregated. |
| Abuse and untrusted input | Titles of published items are user-written: rendered as text, never as markup. Ask-the-board input is untrusted and maps only to whitelisted metrics. A flood of events from one member cannot exceed the k threshold to reveal anything, but it can inflate counts, so per-member event rate limits apply at the source (existing). Zone strings are validated. |
| One owner | Definitions: the catalogue. Thresholds: the rollup layer. Access: the gateway gate plus the grant. Zone-to-country: one table. The board never recomputes a metric. |
| Defaults and fallbacks | Geography off. Cost shown with its coverage, never as the full truth. Unknown department: "other", counted in totals, not shown. No data yet: "warming up". Time saved: absent unless the org enters its assumption. |
| External contracts | Core usage reports: each core's coverage is data, read from its reply metrics; a core that reports nothing is a visible grey lane. tzdata: the table is versioned with the platform. The AI provider is the org's and is never assumed to be available. |
| Load on users and agents | No prompt, tool or rule added to any agent or member. Members see no new screen except the transparency page. The briefing adds model use under the org's budget. |
| Release and compatibility | Additive. Rollout order: rollups and the catalogue first (no UI), then the API behind the grant, then the board, then geography last and per org. Existing team dashboard and usage reports unchanged. Rollback: revoke the grant or switch the feature off per org; the rollups can stay. |
| Observability | Lag from event to board (p95), settling count, suppressed-cell count (a rise is normal; a drop to zero is suspicious), coverage per core, gate denials, audit rows written. |
| AI capability | Briefing and ask-the-board on the assist seam; each with an off switch and a template fallback; the reason is shown when the model is off. Not used for numbers. |

**Pre-mortem.** It shipped and a year later it failed badly. The three most likely
reasons, and what this design does about each:

1. **The board became a surveillance tool for individuals.** Someone asked for
   "who is least active", and a manager used the Pulse in a review. Answer: no
   individual data, no ranking, k at the source, the purpose stated in the
   product, and the transparency page. A feature request for a person-level
   view is a new PRD with its own review, not a filter.
2. **Vanity metrics were optimised.** "Active people" rose because people sent
   "hi" messages, and the board praised it. Answer: "active" is defined as a
   meaningful action, the output and acceptance numbers sit beside it, and the
   definitions are visible on the board. Any new metric enters the catalogue
   with its own definition and a note on what it does not measure.
3. **Partial spend data drove a budget decision.** Cost looked low because one
   core reports no usage, and the budget was cut. Answer: coverage is on the
   pump, the cost is labelled "≈ reported", and a grey "not reported" lane shows
   which core is missing. A budget decision reads the coverage first.

## 11. Acceptance

- Two members, both active: the board shows 2 people only if the department
  behind them reaches k; otherwise "under 5". A test covers both.
- Complementary suppression: a suppressed department cannot be recovered by
  subtracting shown departments from the total.
- A reply with no usage moves nothing; a reply with usage moves the counter by
  exactly its tokens, in steps, with the rise never ahead of the reported total.
- A claimed job with an expired lease is not counted as working after the
  sweep; a job that finishes drops the count by one on the next step; a queued
  job is counted as waiting, never as working. Tests on the step function, with
  a simulated bridge crash mid-job.
- A core that reports no usage appears as "not reported", and the total says
  "≈" with coverage below 100%.
- A removed grant ends the stream on the next refresh; the audit row exists.
- Geography off: no zone counts stored at all; geography on: no member id is
  stored anywhere, checked by a test that scans the rollup and the live window.
- Aggregator down for 60 s: the board says "delayed", its frame is held, no
  zeros; on recovery the rollups fill the gap and the settling minutes resolve.
- `prefers-reduced-motion`: no roll animation and no bubbles; nothing flashes
  faster than 3 times a second.

## 12. Phasing (slices, each reviewable alone)

0. Prototype (sample, simulated data). Done in the live-monitor-demo folder.
1. Metric catalogue, rollups, k and complementary suppression, the snapshot
   API behind `read_org_pulse`, the audit row. No UI.
2. The board on the API with real rollups: counters, output, spend with
   coverage, the pump driven by reported usage.
3. Presence and the globe (geography off: follow the sun). Live window and SSE.
4. Geography option A, behind an org opt-in and the member notice.
5. Briefing and ask-the-board on the assist seam.
6. Lobby mode, showcase wall, milestones, polish.

## 13. Open decisions

| # | Decision | Recommendation |
|---|---|---|
| 1 | Geography default | Off. Follow the sun first; option A only with an org opt-in and a notice to members. |
| 2 | Where the executive board lives | A sample in its own repository on the public API (rule for use cases). A native page would be a platform surface and needs its own reason. |
| 3 | Definition of "active" | Meaningful actions only (§2.1). Confirm with the people who will read it. |
| 4 | Cost reliability | Show it with coverage now. Budget decisions read coverage first. Cores that report nothing are a product gap to close per core, not hidden. |
| 5 | Legal and employee notice | Needs a review for the jurisdictions the org operates in before geography or a people-now view is enabled. Not decided here. |
| 6 | Time saved | Off by default. If offered, only as the org's own assumption, labelled as an estimate. |
