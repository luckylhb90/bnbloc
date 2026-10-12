# Interactive apps: agent-built sites with their own data (2026-10-12)

Status: proposed (rev 2, after product-owner review). Build after the
code-health batches 2/4/5 land: they rework the gateway route table, the store
rig and the web wire types this feature sits on.

## 1. Why

A member asks an agent in a room to build a small interactive project, like a
poll. They get a working page and send the link to a group chat. People use
it, and the same agent then reads the collected data and writes the report,
all in the same room. Polls are only one example. The platform does not
know or care what the app is; sign-ups, surveys, lunch orders, retro boards and
games are all "a page plus some data plus an agent that reads it back".

## 2. Principle: a base, not a product

The platform provides only what an app cannot provide for itself. Everything
else is the app's own code, written by the agent:

- who may take part;
- one vote per person;
- open/close;
- field validation;
- results pages;
- exports.

The platform adds no manifest, no participant modes, no schemas and no app
lifecycle.

The test for adding anything to the platform: the app cannot do it in its own
code, or doing it there would be unsafe. Durable storage, verifiable identity
and isolation pass that test. Business rules do not.

## 3. What exists today (verified 2026-10-12)

- **Build:** the bridge publishes any workspace folder with `index.html` (or
  its `dist/`) as a versioned `site` artifact after each turn
  (`services/bridge/sites.go`).
- **Serve:** the gateway serves sites under a member ticket
  (`/v1/sites/{ticket}/…`) or a public share link
  (`/v1/public/shares/{token}/site/…`), with `CSP: sandbox allow-scripts …`
  and an opaque origin.
- **Distribute:** public share links already exist: anonymous, with an expiry,
  revocable, and org admins can disable them.
- **Missing: durable data.** The live-state relay keeps only the last 16 KiB
  per artifact, in memory, members only, and is not wired for sites.
- **Missing: agent read-back.** No room tool reads an app's data.

## 4. The core capabilities (the whole feature)

### 4.1 App data: a document store per app

- Each published site gets its own data space, keyed by (tenant, room,
  artifact). Within it there are named **collections** of **JSON documents**,
  created on first write. There is no schema; the app decides the shape.
- API, path-relative under the capability the page was opened with, so the
  page handles no tokens:
  - `POST ~app/c/{collection}`: insert.
  - `GET ~app/c/{collection}?filter=…&sort=…&limit=…&after=…`: find.
    `filter` is a small JSON subset (`$eq $ne $in $gt $gte $lt $lte $exists`
    on top-level and dotted fields).
  - `GET|PUT|PATCH|DELETE ~app/c/{collection}/{id}`.
  - `GET ~app/events`: SSE stream of changes, for live results.
  - `GET ~app/me`: who the caller is.
- A tiny first-party SDK at `~app/sdk.js` wraps these calls:
  `app.collection("votes").insert({...})`, `.find(filter)`, `.subscribe()`,
  `app.me()`.
- The server stamps every document with `_id`, `_by`, `_byName`,
  `_createdAt` and `_updatedAt`. The client cannot set or change them.

### 4.2 Identity: a fact the app can trust

- `_by` and `me()` are the caller as the server knows it. A member who opens
  the app from bnbloc is that member (exact subject, per D2). A visitor on a
  public link gets a stable anonymous id for that browser.
- That is all the platform decides. "One vote per person" is the app's own
  logic: it reads its documents and counts the latest one per `_by`. Because
  `_by` is server-stamped, nobody can forge it, even by calling the API
  directly.

### 4.3 The only two rules, and why the platform must own them

Front-end code can't stop someone who calls the API directly, so two rules
can't live in the app:

1. **Only the author or a room member can change or delete a document.**
   Without this, anyone with the link could wipe everyone else's data.
2. **A document can be private:** `insert(doc, {private: true})`. Only its
   author and room members can read it. This is the only way an app can
   collect something like a secret ballot or contact details without exposing
   it to every link holder.

Everything else is the app's choice.

### 4.4 Safety limits (cost and abuse, not product rules)

- Size caps: per document (default 64 KiB), per app (default 100k documents
  or 256 MiB).
- Per-IP and per-link rate limits, configured as route-table rows.
- Writes stop the moment the share link is revoked or expires, or the room is
  deleted.
- App data is room-owned: it is in `roomOwnedTables` and follows room
  retention.

### 4.5 Agent read-back

- Room tool `app_data(artifactId, collection?, filter?, limit?, after?)`: the
  agent reads, and can also write (e.g. seeding the options), with paging. The
  agent does the analysis itself; the platform doesn't aggregate.
- `.room/apps.md` lists the room's apps, their collections and document
  counts.
- A builtin skill pack "Interactive apps" documents the SDK and the two rules,
  so any agent from any vendor builds apps correctly.

### 4.6 Isolation (platform responsibility)

- Sites keep the opaque origin: a sandbox CSP and no `allow-same-origin`.
- Add `BNBLOC_SITES_ORIGIN`, a separate origin for sites and their data API,
  recommended in production.
- The data API never reads cookies or session headers, only the URL
  capability. A capability for app A can never reach app B's data or the
  room's data.

## 5. Storage backend

The API is a document model whatever the backend. The real choice is
operational.

| | MySQL 8 JSON (existing DB) | MongoDB (new dependency) |
|---|---|---|
| Fit for schemaless docs + simple filters | Good: `JSON` column, `JSON_EXTRACT`, generated-column indexes for hot fields | Native |
| Expected volume per app (hundreds to ~100k docs) | Comfortable | Comfortable |
| Operations | Nothing new: same backups, migrations, retention, `DeleteRoom`, tenant isolation, memory-store parity | A new cluster to run, back up and secure on every deploy surface (compose, prod overlay, EKS; AWS DocumentDB has compatibility gaps). Room deletion and retention then span two databases |
| Arbitrary per-field indexes, heavy aggregation | Limited | Strong |

**Recommendation:** define one interface, `store.AppDocs` (insert, find with
the filter subset, get, update, delete, change feed), and ship the MySQL JSON
backend first. Filters translate to parameterized `JSON_EXTRACT` predicates
from a fixed operator whitelist. MongoDB stays a pluggable second backend,
like OpenSearch is for search, to add when an app's volume or query needs
outgrow MySQL. Apps and the SDK don't change when the backend does.

## 6. Delivery steps

1. `store.AppDocs`: the interface, the MySQL JSON and memory backends, the
   migration, a contract suite over both backends, and registration as
   room-owned.
2. Gateway data API and SDK under both capabilities, server stamps, the two
   rules, caps, rate-limit rows, SSE.
3. Room tool `app_data`, `.room/apps.md`, and the builtin skill pack.
4. `BNBLOC_SITES_ORIGIN` on every deploy surface.

Tests:
- A capability for app A never reads app B.
- The client can't set `_by`.
- A non-author can't update or delete a document.
- A private document is hidden from other visitors.
- A revoked link stops writes.
- The filter whitelist rejects unknown operators.

## 7. Open decision

- Storage backend: MySQL JSON first behind `store.AppDocs` (recommended), or
  MongoDB from day one.
