# Interactive apps: agent-built sites with their own data (2026-10-12)

Status: proposed, for product-owner review. Build after the code-health
batches 2/4/5 land (they rework the gateway route table, the store rig and the
web wire types this feature sits on).

## 1. Why

A member asks an agent in a room to "build a poll for Friday's offsite", gets a
working page, sends the link to a group chat, people vote, and the same agent
then reads the votes and writes the report — all in one room. Polls are only
the first example: sign-up sheets, surveys, lunch orders, retro boards,
approval forms, small games and quizzes are the same shape (a page plus a small
data set plus an agent that reads it back).

This closes the loop no single-purpose tool closes: build (agent) → distribute
(share link) → collect (app data) → analyse and act (agent, same room, same
context). It reuses what already exists — published sites, public share links,
room tools, scheduled prompts — and adds the one missing piece, durable app
data.

## 2. What exists today (verified 2026-10-12)

| Step | Today | Gap |
|---|---|---|
| Build | The bridge publishes any workspace folder with `index.html` (or its `dist/`) as a versioned `site` artifact after each turn (`services/bridge/sites.go`) | The agent has no documented contract for an app that stores data |
| Serve | Gateway serves sites under a member ticket (`/v1/sites/{ticket}/…`) or a public share (`/v1/public/shares/{token}/site/…`), with `CSP: sandbox allow-scripts allow-forms …` and an opaque origin (`services/gateway/sites.go`) | Same origin as the web app; isolation relies on the sandbox header and the iframe alone |
| Distribute | Public share links: anonymous, 1 h–90 d, revocable, org admins can disable (`services/room/artifact_shares.go`) | No identity for visitors; no "post to group chat" event |
| Collect | Nothing durable. The live-state relay keeps only the last 16 KiB state per artifact in memory, members only, not wired for sites | **Missing entirely** |
| Analyse | Room tools read chat (`read_room`) and Library components; `.room/live.md` keeps 32 events in bridge memory | No way to read an app's data set |

## 3. Design

### 3.1 The app contract: `bnbloc.app.json`

A site becomes an *app* when its folder holds `bnbloc.app.json` next to
`index.html`. The bridge publishes it exactly like a site (same artifact, same
versions); the manifest travels in the bundle and the gateway reads it from the
pinned bundle (`LoadPinned`), never from the client.

```json
{
  "collections": {
    "votes": {
      "write": "participant",
      "read": "own",
      "onePerParticipant": true,
      "maxRecordBytes": 2048,
      "fields": { "option": "string", "comment": "string?" }
    },
    "options": { "write": "owner", "read": "all" }
  },
  "results": { "read": "all" },
  "participants": "link"
}
```

- `participants`: who may take part through a link — `room` (room members),
  `org` (any signed-in member of the room's organization), `link` (anyone with
  the link; optionally `name` to ask for a display name).
- Per collection: `write` (`owner` = room members who can drive the app,
  `participant`, `none`), `read` (`all`, `own`, `owner`), `onePerParticipant`
  (upsert keyed by participant), a record size cap and an optional field schema
  validated server-side.
- Unknown keys are refused at publish (the bridge validates; the gateway
  re-validates), so the contract has one owner: `internal/appdata.Manifest`.

### 3.2 Data store (one owner: `internal/appdata` + `store.AppDataStore`)

- Table `app_records`: `tenant_id`, `room_id`, `artifact_id`, `collection`,
  `record_id` (time-ordered `ids.New`), `participant_key` (`utf8mb4_bin`),
  `participant_label`, `body` JSON, `created_at_ms`, `updated_at_ms`. Unique
  `(artifact_id, collection, participant_key)` for `onePerParticipant`
  collections (enforced by a generated key column).
- Table `app_state`: per app `open|closed`, `closed_at_ms`, record counters
  for caps.
- Caps (wire constants): records per app (default 50 000), per collection,
  bytes per record, total bytes per app. A cap hit is a typed 409 the SDK
  shows.
- Room-owned: both tables join the `roomOwnedTables` registry (DeleteRoom) and
  room retention; memory and MySQL share one contract suite.

### 3.3 The data API and SDK

- Path-relative endpoints under the same capability the page was opened with,
  so the page needs no token handling and no CORS beyond today's `ACAO: *`:
  - member: `/v1/sites/{ticket}/~app/…`
  - public: `/v1/public/shares/{token}/site/~app/…`
  - routes: `GET ~app/me`, `GET|POST ~app/collections/{name}/records`,
    `PUT|DELETE ~app/collections/{name}/records/{id}`, `GET ~app/results`
    (aggregates: count by field), `GET ~app/events` (SSE, live results).
- The gateway rewrites nothing in the agent's JS; it serves a tiny first-party
  SDK at `~app/sdk.js` (`bnbloc.app.records("votes").add({...})`,
  `.list()`, `.mine()`, `.results()`, `.subscribe()`), versioned with the
  gateway.
- Every call resolves the capability → (tenant, room, artifact, version) and
  the participant, then authorizes against the manifest. One owner:
  `appdata.Authorize(manifest, participant, op, collection)`.
- Rate limits are route-table rows (Batch 2.1): per IP, per link, per
  participant; writes stricter than reads.

### 3.4 Participants and identity

- Member ticket: the participant is the member (subject, exact per D2).
- `participants: "org"`: the public link page asks the visitor to sign in
  through the normal login, then mints an **app participant token** scoped to
  that one app (kind `auth.KindAppParticipant`, short TTL, carried by the SDK,
  never a session). Outsiders are refused.
- `participants: "link"`: an anonymous participant key — a random per-device
  id the SDK keeps in the page's storage partition, plus an optional display
  name. `onePerParticipant` then means one per device, stated in the UI; for a
  binding vote use `org`.
- Visitors never see room details; owners see participant labels (or
  "anonymous") in results.

### 3.5 Agent side

- Room tool `read_app_data(artifactId, collection?, since?, aggregate?)`:
  members-and-agent read of the full data set with paging, plus server-side
  aggregates so a 10 000-vote poll does not flood the context.
- Room tool `set_app_state(artifactId, open|closed)` and the same control in the
  artifact viewer.
- `.room/apps.md` lists the room's apps with counts and state.
- A builtin skill pack "Interactive apps" documents `bnbloc.app.json`, the SDK
  and good patterns (results page, closing a poll, exporting), so any agent
  (any core, any vendor) builds apps correctly without bespoke prompting. A
  builtin expert "App builder" wraps it for one-click use.
- The report step is a normal prompt ("summarise the votes") or a scheduled
  prompt at the poll's close time.

### 3.6 Web

- Artifact viewer shows an app badge, open/closed toggle, live participant
  count, a results tab (generic table + aggregates from the manifest fields)
  and CSV export.
- Share dialog offers the participant mode chosen by the manifest and copies a
  link; the existing public-share expiry and revocation apply.

### 3.7 Security

- Sites keep the opaque origin (sandbox CSP, no `allow-same-origin`). Add
  `BNBLOC_SITES_ORIGIN`: when set, sites and their data API are served only on
  that separate origin (recommended for production; the gateway refuses site
  paths on the main origin). Session tokens never become reachable from app
  code either way.
- The data API never reads cookies or session headers; only the URL capability
  and, for `org`, the app participant token.
- Manifest and field validation are server-side; record bodies are stored and
  returned as JSON only, never rendered by bnbloc's own UI as HTML.
- Closing an app, revoking its link or deleting the room stops writes
  immediately; org admins' "disable public links" also disables `link` apps.
- Abuse: per-IP/link/participant limits, record and app caps, audit rows for
  open/close/export.

## 4. Delivery steps

1. `internal/appdata` (manifest, authorize, caps) + store tables, migration,
   contract suite, room-owned registry.
2. Gateway data API + SDK under both capabilities, rate-limit rows, SSE.
3. Participants: member, `link` (device key + name), `org` (participant
   token).
4. Room tools `read_app_data` / `set_app_state`, `.room/apps.md`, builtin skill
   pack and expert.
5. Web: app badge, results tab, open/close, CSV, share-dialog modes.
6. `BNBLOC_SITES_ORIGIN` separate origin (all deploy surfaces).

Each step lands with its owner's tests (authorize table test, cap contract
cases, capability-scope test: a ticket for app A never reads app B, a closed
app refuses writes, a `link` visitor never reaches room data).

## 5. Open questions for the product owner

- Q1. Default participant mode for a shared link: `link` (lowest friction) or
  `org` (one vote per real person)? Proposal: the agent picks per app from the
  request; the share dialog shows it and lets the owner change it.
- Q2. Should non-org outsiders ever be able to sign in as named guests (email
  one-time code)? Proposal: not in this round; `link` with a display name
  covers it.
- Q3. Post the link to a group chat automatically? Proposal: via an installed
  chat MCP tool component the agent calls; no new notifier event this round.
