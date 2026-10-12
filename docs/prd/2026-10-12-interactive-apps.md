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

### 4.1a Concurrency: per-document, like any document database

The concurrency model is the same whichever backend is used (MongoDB is also
atomic per document):

- **One row per document, never a shared JSON blob.** Concurrent inserts are
  independent rows (time-ordered `_id`), so a thousand people voting at once
  never contend.
- **Every write to one document is atomic.** `PUT`/`PATCH`/`DELETE` is a
  single `UPDATE … WHERE id=?` under its row lock; a `PATCH` applies its
  `JSON_SET` paths in that one statement.
- **Optimistic concurrency.** Each document carries a server-incremented
  `_version`. A write with `ifVersion` (`If-Match`) that no longer matches gets
  409, and the SDK returns it so the app can re-read and retry. Without
  `ifVersion`, the last writer wins.
- **Atomic counters.** `PATCH` supports `$inc` on numeric fields
  (`UPDATE … SET body = JSON_SET(body, '$.n', COALESCE(JSON_EXTRACT(body, '$.n'), 0) + ?)`),
  so a shared counter never loses an update.
- **Create-only with the app's own id.** `PUT ~app/c/{collection}/{id}` with
  `ifAbsent` (`If-None-Match: *`) succeeds once and returns 409 afterwards,
  enforced by the primary key. An app gets race-free "one record per key" by
  choosing the key, e.g. `id = me().id`, with no platform rule about votes.

The recommended pattern for tallies is still one document per action, counted
on read. There's no hot row at all.

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
2. **Documents are private by default.** Only the author and the app's
   owners (room members and the room's agents) can read a document, unless
   the app inserts it with `{public: true}`: options, a published tally, a
   shared board. Anything the app does not mark public, such as a ballot,
   contact details or free-text answers, is never readable by other link
   holders, even through direct API calls.

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

### 4.6 Privacy and security (platform responsibility)

**Isolation between apps, rooms and tenants**
- The app id comes only from the capability. The gateway resolves the
  ticket or share link to (tenant, room, app) and binds every query to it.
  No route takes an app id, room id or tenant id from the request.
  `TestAppDataScopeFromCapabilityOnly` is a static scan, and a behavioural
  test checks that app A's capability never reads app B.

**Isolation from bnbloc itself**
- Sites keep the opaque origin: a sandbox CSP and no `allow-same-origin`.
  Production uses the separate `BNBLOC_SITES_ORIGIN`.
- App code can't read sessions, cookies, the room or other apps.
- The data API accepts no cookie or session, only the URL capability. That
  makes it CSRF-proof, and a malicious app can't act as its viewer anywhere
  else.

**Who sees what inside an app**
- Private by default (rule 2). Only documents the app marks `public` are
  readable by link holders.
- Visitors get pseudonyms. `_by` is an opaque per-app id, `HMAC(app_id,
  participant)`:
  - The same person can't be linked across apps.
  - A public-link visitor never sees a member's real subject or name.
  - Real identities (`_byName`) are returned only to the app's owners and its
    agents.
- The app's owners are the room members and the room's agents. They read
  everything, including private documents. Every agent read and every export
  is audited.

**Transport, storage, lifecycle**
- TLS in transit. Document bodies are encrypted at the application layer;
  see 4.7.
- Bodies are stored and returned as JSON only. bnbloc's own UI never renders
  them as HTML, and an app's XSS stays inside its own sandbox.
- Capability tokens are redacted from gateway access logs. Sites already send
  `Referrer-Policy: no-referrer`.
- Revoking or expiring a link stops reads and writes immediately. Org
  admins' "disable public links" applies to app data too.
- A participant can delete their own documents, and owners can wipe an app.
  Room deletion and room retention remove app data along with the room.

**App data is untrusted input to agents**
- Anything a visitor writes can carry prompt injection.
- `app_data` returns documents fenced as untrusted, third-party content and
  never as instructions. The builtin skill pack tells agents to treat it
  that way.
- Borrowed or platform agents that read app data stay inside the existing
  sandbox and confinement rules.

### 4.7 Encryption: the database never holds a readable body

Disk or RDS encryption at rest only protects stolen disks and snapshots.
Anyone with SQL access, such as a DBA, a leaked backup or an injection
elsewhere, would still read plaintext. So app data is encrypted in the
gateway, before it reaches the database.

- **Envelope encryption, one key per app.**
  - Each app gets a random 256-bit data key, used for AES-256-GCM over each
    document body. The additional authenticated data is `app_id|collection|doc_id`,
    so a ciphertext can't be moved to another document or app.
  - The data key is stored only wrapped by the master key, through the one
    existing owner `internal/sealbox`, which already seals member and org
    credentials.
  - The master key never sits in the database: it is `BNBLOC_SECRET_STORE_KEY`
    today, with a KMS-backed key as a drop-in later.
- **What stays plaintext:** only routing metadata: `app_id`, `collection`,
  `doc_id`, the pseudonymous `by_key` (an HMAC, not the identity), `public`,
  `version` and the timestamps. Bodies and the real-identity mapping are
  ciphertext.
- **Crypto-shredding.** Deleting an app, its room, or a retention purge
  destroys the app's data key, so every copy of its documents becomes
  unreadable at once, including copies in database backups.
- **Rotation.** A master-key rotation re-wraps the data keys only; documents
  are not re-encrypted.
- **Cost.** Body fields can't be filtered in SQL. Queries narrow by metadata
  in SQL (collection, author, time, public), and the gateway applies `filter`
  to the decrypted page. The per-app cap bounds this. The tallies, lists and
  full reads apps and agents actually do are unaffected.
- **Who can still decrypt:** the running gateway, which holds the master key.
  It has to, to serve the app. A database-only compromise reveals no
  content.
- **Later, if needed: owner-only end-to-end encryption.** The page encrypts a
  private document to the owners' public keys, and only an owner's desktop,
  which also runs the bridge and its agents, can decrypt it. Not in this
  round: it needs member key management, and it breaks reads by the gateway
  and by agents running anywhere other than the owner's desktop.
- One owner: `appdata.Cipher`, which wraps `sealbox` for keys and AES-GCM for
  bodies. A static test forbids writing `app_docs.body` anywhere except
  through it.

## 5. Storage backend

The API is a document model whatever the backend. The real choice is
operational.

| | MySQL 8 JSON (existing DB) | MongoDB (new dependency) |
|---|---|---|
| Fit for schemaless docs + simple filters | Good: `JSON` column, `JSON_EXTRACT`, generated-column indexes for hot fields | Native |
| Expected volume per app (hundreds to ~100k docs) | Comfortable | Comfortable |
| Operations | Nothing new: same backups, migrations, retention, `DeleteRoom`, tenant isolation, memory-store parity | A new cluster to run, back up and secure on every deploy surface (compose, prod overlay, EKS; AWS DocumentDB has compatibility gaps). Room deletion and retention then span two databases |
| Arbitrary per-field indexes, heavy aggregation | Limited | Strong |

**Layout: one shared table for every app, never a table per app.**

```
app_docs(
  app_id, collection, doc_id,          -- PRIMARY KEY, in this order
  tenant_id, room_id, by_key, private,
  version, body JSON, created_at_ms, updated_at_ms)
INDEX (app_id, collection, by_key, doc_id)   -- "mine" / dedupe reads
```

- InnoDB clusters rows by primary key. One app's documents are stored
  contiguously, and every query is a range scan inside that app's prefix, so
  in practice each app behaves like its own table. One app's size doesn't slow
  another app.
- A table per app would mean DDL at runtime: thousands of tables against the
  table cache and file handles, outside `migrations.lock` and the schema
  fingerprint, invisible to `roomOwnedTables`/retention, and an `ALTER` per
  app for any change. All of that is ruled out.
- What the shared table gives up is arbitrary per-app indexes. A filter on a
  non-key field scans only that app's range, which the per-app cap bounds
  (100k docs). The same `body` can later get generated-column indexes for
  common fields, if needed.
- Growth path, without changing the API: `PARTITION BY KEY(app_id)`, then move
  hot apps to another database instance by `app_id`. In a MongoDB backend
  this is one collection with `app_id` leading every index; it is not a
  collection per app either.
- Deleting an app or a room is a batched range delete on the key prefix.

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
- `ifVersion` conflicts return 409.
- `$inc` loses no update under concurrency.
- `ifAbsent` creates exactly once under concurrency.
- A non-author can't update or delete a document.
- A document not marked public is hidden from other link holders.
- Visitors only ever see pseudonymous `_by`.
- Capability tokens never appear in access logs.
- The raw `app_docs.body` column is never readable plaintext; ciphertext
  moved to another document fails to open.
- After an app is deleted, its documents are unreadable even from a restored
  backup (data key destroyed).
- A revoked link stops writes.
- The filter whitelist rejects unknown operators.

## 7. Open decision

- Storage backend: MySQL JSON first behind `store.AppDocs` (recommended), or
  MongoDB from day one.
