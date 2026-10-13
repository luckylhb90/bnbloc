# Interactive apps: agent-built sites with their own data (2026-10-12)

Status: **accepted** by the product owner on 2026-10-12. Development starts
once code-health batches 2, 4 and 5 have landed. This feature builds on the
gateway route table (2.1), `svcauth.NewRequest` (2.2), `httpx` (2.3), the
event-kind registry (2.5), the store rig (4.1) and the generated wire types
(5.1).

Progress: delivery steps 1–6 **landed** on 2026-10-13 (store, cipher, the
room service's data owner, the gateway's capabilities, visitor tokens, SDK
and change feed, the agent's read-back — `app_data`, `.room/apps.md` and
the built-in content — the web's app view and data line, and the sites
origin with the audited export and agent reads; see §7 "Step status").

## 1. Why

A member asks an agent in a room to build a small interactive project, such
as a poll. The member gets a working page, sends the link to a group chat, and
people use it. The same agent then reads the collected data and writes the
report. All of this happens in the same room.

A poll is only one example. Sign-ups, surveys, lunch orders, retro boards,
approval forms and games have the same shape: a page, some data, and an agent
that reads the data back. The platform never knows or cares what the app is.

## 2. Principle: a base, not a product

The platform provides only what an app cannot provide for itself.

**The test:** anything that can live safely in the app's own code stays out
of the platform. The platform keeps only what fails that test: durable
storage, verifiable identity, the rules client code cannot enforce, safety
caps and isolation.

**The app's code, written by the agent, decides everything else:**
- who may take part;
- one vote per person;
- opening and closing;
- validation;
- results pages;
- exports.

So the platform has no manifest, no participant modes, no schemas and no app
lifecycle.

## 3. What exists today (verified 2026-10-12)

| Step | Today | Gap |
|---|---|---|
| Build | After each turn the bridge publishes any workspace folder with `index.html`, or its `dist/`, as a versioned `site` artifact (`services/bridge/sites.go`). The artifact id is stable across versions. | none |
| Serve | The gateway serves sites behind a member ticket (`/v1/sites/{ticket}/…`) or a public share link (`/v1/public/shares/{token}/site/…`). Sites run with `CSP: sandbox allow-scripts …` and an opaque origin. | Sites share the web app's origin. The share page wraps an app in file-viewer chrome. |
| Distribute | Public share links: anonymous, with an expiry, revocable, and org admins can disable them. | none |
| Collect | The live-state relay holds only the last 16 KiB per artifact, in memory, for members only. It is not wired to sites. | **Durable data is missing.** |
| Read back | Room tools read chat and Library components. | **No tool reads an app's data.** |

## 4. The platform base

### 4.1 App data: a document store per app

- **Data space.** Each published site owns one data space, keyed by its
  artifact id (the `app_id`). The space lives in the artifact's room and
  tenant. Inside it are named **collections** of **JSON documents**, each
  created on first write. There is no schema.
- **API.** Every path is relative to the capability the page was opened
  with, so the page handles no tokens:

  | Method and path | Purpose |
  |---|---|
  | `POST ~app/c/{collection}` | insert (`{public}` option) |
  | `GET ~app/c/{collection}?filter=&sort=&limit=&after=` | find, with paging |
  | `GET` / `PUT` / `PATCH` / `DELETE ~app/c/{collection}/{id}` | read, replace, merge or `$inc`, delete. `PUT` takes `ifAbsent`; writes take `ifVersion`. |
  | `GET ~app/events` | change feed (SSE) |
  | `GET ~app/me` | the caller's id, and their display name when it is the caller's own |

- **Filters.** `filter` accepts a fixed set of operators: `$eq $ne $in $gt
  $gte $lt $lte $exists`, on top-level and dotted fields. Any other operator
  is refused.
- **SDK.** A small first-party SDK is served at `~app/sdk.js`:
  `app.collection("votes").insert(doc, {public})`, `.find(filter)`,
  `.get(id)`, `.put(id, doc, {ifAbsent, ifVersion})`, `.patch(id, ops)`,
  `.remove(id)`, `.subscribe(fn)`, `app.me()`. It is versioned with the
  gateway.
- **Server stamps.** The server stamps every document with `_id`, `_by`,
  `_createdAt`, `_updatedAt` and `_version`. Clients can never set or change
  them.

### 4.2 Concurrency: per document, like any document database

- **No shared blob.** Each document is its own row. Concurrent inserts are
  independent rows with time-ordered `_id`s, so a thousand simultaneous
  voters never contend.
- **Atomic writes.** A write to one document runs in one transaction: lock the
  row with `SELECT … FOR UPDATE`, decrypt, apply the replace, merge or `$inc`,
  re-encrypt, then `UPDATE`. Writes are atomic per document, and `$inc` never
  loses an update.
- **Optimistic concurrency.** A write that sends `ifVersion` gets 409 when the
  document has moved on. The app re-reads and retries. Without `ifVersion`,
  the last write wins.
- **Create-only.** `PUT …/{id}` with `ifAbsent` succeeds exactly once; the
  primary key enforces it. An app gets a race-free "one record per key" by
  choosing the key, for example `id = (await app.me()).id`.
- **Recommended tally pattern.** Write one document per action and count when
  reading. That leaves no hot row at all.

### 4.3 Identity: a fact the app can trust

- **Who `_by` is.** The server derives `_by`, and `me().id` returns the same
  value. It is an opaque per-app pseudonym: `HMAC(app_id, participant)`.
  - A member who opens the app from bnbloc, through a member ticket, is that
    member. Subjects are exact (D2).
  - A visitor on a public link is a **visitor id**. The share page mints a
    signed, random visitor token once per browser, keeps it in its own
    storage, and hands it to the app frame. Sandboxed app frames have no
    storage of their own.
- **One vote per person** is the app's own logic, built on a value nobody can
  forge.
- **Real identity.** The real identity behind `_by` (a member's name) is
  returned only to the app's owners (4.4).

### 4.4 The two rules the platform owns

Client code can't stop someone who calls the API directly. So these two
rules, and only these, live in the platform.

**The app's owners** are the members of the artifact's room and the room's
agents.

1. **Only a document's author or an owner may change or delete it.**
   Otherwise anyone holding the link could wipe everyone else's data.
2. **Documents are private by default.** A document is readable only by its
   author and the owners, unless the app inserted it with `{public: true}`.
   Typical public documents are options, a published tally or a shared board.
   Ballots, contact details and free text stay private, even against direct
   API calls.

### 4.5 Safety limits: cost and abuse, not product rules

- **Size caps** are wire constants:
  - 64 KiB per document;
  - 100k documents or 256 MiB per app;
  - 64 collections per app.
- **Rate limits.** Route-table rows set per-IP, per-link and per-participant
  limits. Writes are stricter than reads.
- **Writes stop immediately** when the share link is revoked or expires, when
  an org admin disables public links, or when the room is deleted.

### 4.6 Agent read-back

- **Room tool** `app_data(artifactId, collection?, filter?, limit?, after?)`.
  - Agents can read and write with paging, for example to seed the options or
    to clear test data.
  - The agent does the analysis itself; the platform doesn't aggregate.
  - Results are fenced as untrusted third-party content, never as
    instructions.
- **`.room/apps.md`** lists the room's apps with their collections and
  document counts.
- **Builtin content.** These are not platform rules, and any member can copy
  and edit them:
  - an "Interactive apps" skill pack, covering the SDK, private-by-default,
    `ifAbsent` with `me().id`, closing in code, and treating data as
    untrusted;
  - an "App builder" expert;
  - starter prompts `/poll`, `/survey` and `/signup`.

### 4.7 Privacy and security

**Scope comes only from the capability.**
- The gateway resolves a member ticket, a share link or the agent's session to
  (tenant, room, app). The data owner binds every query to that scope.
- No route takes an app, room or tenant id from the request body or query.
- Enforced by `TestAppDataScopeFromCapabilityOnly` (a static scan) and a
  behavioural test: app A's capability never reaches app B.

**Isolation from bnbloc.**
- Sites keep their opaque origin: a sandbox CSP and no `allow-same-origin`.
- `BNBLOC_SITES_ORIGIN` serves sites and their data API from a separate
  origin. It is recommended in production, and once it is set the gateway
  refuses site paths on the main origin.
- The data API accepts no cookie and no session header for app traffic, only
  the URL capability plus the visitor token. That makes it CSRF-proof, and an
  app can never act as its viewer anywhere else.

**Lifecycle.**
- Capability tokens are redacted from gateway access logs, and sites already
  send `no-referrer`.
- A participant can delete their own documents. Owners can export, clear or
  delete an app's data.
- Every agent read and every export is audited.
- Room deletion and room retention remove app data along with the room.

**Rendering.** Bodies are JSON only. bnbloc's own UI never renders them as
HTML, so an XSS in an app stays inside that app's sandbox.

### 4.8 Encryption: the database never holds a readable body

Disk or RDS encryption at rest protects against stolen disks and snapshots,
but not against anyone with SQL access (a DBA, a leaked backup, an injection
elsewhere). So app data is encrypted in the service before it reaches the
database.

- **Envelope encryption, one data key per app.**
  - Each app gets a random 256-bit data key, generated on first write.
  - Every document body is sealed with AES-256-GCM, with
    `app_id|collection|doc_id` as additional data, so a ciphertext can't be
    moved to another document or app.
  - The data key is stored only after the master key wraps it, through the
    existing owner `internal/sealbox`.
  - The master key is `BNBLOC_SECRET_STORE_KEY` today; a KMS-backed key can be
    swapped in later. It never sits in the database.
- **What stays plaintext:** only routing metadata: `app_id`, `collection`,
  `doc_id`, the pseudonymous `by_key`, `public`, `version` and the
  timestamps. The body and the real author identity are ciphertext.
- **Crypto-shredding.** Deleting an app or its room, or a retention purge,
  destroys the app's data key. Every copy of its documents becomes
  unreadable, including copies in database backups.
- **Rotation.** Rotating the master key re-wraps only the data keys; documents
  are not re-encrypted.
- **Cost.** SQL can't see body fields. SQL narrows by metadata (collection,
  author, time, public), and the service applies `filter` to the decrypted
  page. The per-app cap bounds this.
- **Who can decrypt:** only the running service that holds the master key,
  because it has to serve the app. A database-only compromise reveals no
  content.
- **One owner:** `appdata.Cipher` (sealbox for keys, AES-GCM for bodies). A
  static test forbids writing `app_docs.body` anywhere else.
- **Later, if needed:** owner-only end-to-end encryption. The page would
  encrypt to the owners' public keys, and only an owner's desktop, where the
  bridge and its agents run, could decrypt. It is out of this round because
  it needs member key management and would block gateway-side and remote
  agent reads.

## 5. Where it lives and how it is stored

### 5.1 Services

- **The room service owns app data** (`services/room/appdata.go`). It already
  owns artifacts, room scoping and room-owned tables.
- **The gateway** resolves each capability and forwards a signed internal
  request (`svcauth.NewRequest`, including the resolved app and participant).
  Each route is a route-table row carrying its rate budget.
- **The change feed** rides the realtime hub's sharded fan-out as a transient
  `app_changed` kind, which is never replayed. It carries only `(collection,
  id, version)`, so the feed leaks nothing. The page re-fetches under its own
  read rights.

### 5.2 Storage: MySQL first, behind one interface

**Decided:** `store.AppDocs` is one interface (insert, find, get, locked
update, delete, wipe, count). It ships with memory and MySQL backends and one
contract suite. MongoDB is a possible later backend behind the same
interface, as OpenSearch is for search, if an app's volume or query needs ever
outgrow MySQL. Apps and the SDK don't change when the backend does.

| | MySQL (existing) | MongoDB (later, if needed) |
|---|---|---|
| Schemaless documents | Fine: bodies are encrypted blobs, and filtering happens after decryption anyway | Native |
| Expected volume (hundreds to ~100k documents per app) | Comfortable | Comfortable |
| Operations | Nothing new: same backups, migrations, retention, `DeleteRoom`, tenant isolation and memory parity | A new cluster on every deploy surface; room deletion and retention would span two databases |

**One shared table for every app, never a table per app:**

```
app_docs(
  app_id, collection, doc_id,               -- PRIMARY KEY, in this order
  tenant_id, room_id, by_key, public,
  author_sealed, version, body_sealed,
  created_at_ms, updated_at_ms)
INDEX (app_id, collection, by_key, doc_id)  -- "mine" reads
app_keys(app_id PRIMARY KEY, tenant_id, room_id, wrapped_key, created_at_ms)
```

- **Why it scales per app.** InnoDB clusters rows by primary key, so one app's
  documents sit together. Every query is a range scan inside that app's
  prefix, so one app's size never slows another.
- **Why not a table per app.** That would mean runtime DDL: thousands of
  tables, outside `migrations.lock`, retention and `roomOwnedTables`, and one
  `ALTER` per app. Ruled out.
- **Growth path, with no API change:** first `PARTITION BY KEY(app_id)`, then
  hot apps move to another instance by `app_id`.
- **Room-owned.** Both tables are listed in `roomOwnedTables`. Deleting an app
  or a room destroys its key, then range-deletes its documents in batches.

## 6. From zero to a working product: the member's journey

1. **Start.** In any room the member types: "build a poll for Friday's
   offsite: three venues, one vote each, live results, closes Thursday
   18:00". Alternatively, they pick the **App builder** expert or a starter
   prompt (`/poll`).
2. **Build.** The agent writes the app with the "Interactive apps" skill pack
   loaded. After the turn the bridge publishes the folder, and the deliverable
   card appears under the reply.
3. **Try it.** The member opens the card. The app runs on real data as the
   member, and teammates in the room can try it too. "Change the colour, add a
   comment box" is simply the next turn: a new version of the same app. **The
   data carries over**, because the data space is keyed by the artifact, not
   the version.
4. **Reset test data.** "Clear the test votes" (the agent uses `app_data`), or
   the card's **Clear data** action.
5. **Share.** **Share link** on the card. The link follows the latest version,
   so later fixes reach everyone who already has it. The member pastes it into
   the group chat, or asks the agent to post it through an installed chat
   tool.
6. **Collect.** Voters open the link on their phones and see the app
   full-screen, with no sign-in. Votes land encrypted in the app's data space.
   Live counts show in the app itself and on the card.
7. **Report.** At close time a scheduled prompt runs ("summarise the votes and
   recommend a venue"), or the member simply asks. The agent reads the data
   with `app_data` and writes the report into the room as a normal
   deliverable.
8. **Reuse.** "Save this as a template" uses `save_component`, and the app
   becomes a Library item others can copy and adapt.

**New UI (small):**
- **App view for site shares.** The app shows full-screen and mobile-friendly,
  without file-viewer chrome or a source download. The share page also holds
  the visitor token.
- **Card data line.** A document count, plus owner-only **Clear data**,
  **Export** (JSON/CSV) and **Delete app data**.

## 7. Delivery steps

| Step | Scope |
|---|---|
| 1 | `store.AppDocs`, with memory and MySQL backends, the migration (`app_docs`, `app_keys`), the contract suite and `roomOwnedTables` entries; `appdata.Cipher` |
| 2 | Room service data owner: scope, the two rules, caps, filters, locked updates |
| 3 | Gateway: capability resolution for member tickets, share links and agent sessions; visitor tokens; route-table rows; `~app/sdk.js`; change feed |
| 4 | Room tool `app_data`, `.room/apps.md`, and the builtin skill pack, expert and starter prompts |
| 5 | Web: app view for site shares; the card data line |
| 6 | `BNBLOC_SITES_ORIGIN` on every deploy surface |

### Step status

- **Step 1: landed (2026-10-13).** `store.AppDocs` (memory and MySQL, the
  `TestContractAppDocs…` suite), migration `2026_10_app_data` (`app_docs`,
  `app_keys`), both tables in `roomOwnedTables`, `appdata.Cipher`. Decided
  while building:
  - `app_keys` also carries `KEY (tenant_id, room_id)`, so a room deletion
    finds its apps without a scan.
  - A crypto-shred destroys the key in place first (`wrapped_key = ''`,
    which refuses writes while the rows go) and deletes the key row last,
    so an interrupted wipe resumes on the next one. Room deletion destroys
    the keys in its transaction and deletes the rows in batches after the
    commit. An app key is only created in a live room.
  - `_by` is an HMAC keyed by the app's data key (a per-app secret, not the
    guessable app id), so a wipe also renews the pseudonyms.
  - Retention of app data (a purge kind with an age index) is not part of
    steps 1–2; until it lands, room deletion and the owners' **Delete app
    data** are the ways app data goes.
- **Step 2: landed (2026-10-13).** `services/room/appdata.go`, gateway-only
  internal routes under `/internal/v1/app/` (the file's comment is the
  contract the gateway step calls). Until step 3, the scope arrives as
  svcauth-signed headers: `svcauth.Key.AssertApp` after `Assert` (subject
  `service:gateway`, the room's tenant) adds the room, the app and the
  participant `<member|agent|visitor>:<id>` under one signature
  (`X-bnbloc-App-Sig`); `svcauth.AppScopeOf` is the only reader. Decided
  while building:
  - Owners are the room's members who may chat (a room visitor is a
    participant, not an owner) and the room's agents.
  - `ifVersion` is `If-Match: <version>`, `ifAbsent` is
    `If-None-Match: *` on PUT; PUT without either replaces, or creates a
    free id. PATCH takes `{merge, $inc, public}`; inserts and replaces
    `{doc, public}` (`wire.AppDocWrite`, `wire.AppDocPatch`).
  - A find sorts by `_id` (`sort=_id` or `-_id`); it scans at most 2000
    documents per request after decryption and returns a `next` cursor.
  - The per-app caps are counted per replica (re-read every 10 s, own
    writes added between reads): other replicas' writes inside the window
    may overshoot a cap, which still bounds cost and abuse.
  - A document's `_author` (the real participant) is rendered for owners
    only.
- **Step 3: landed (2026-10-13).** `services/gateway/apps.go` (the
  capability resolution and the forward), `services/gateway/appsdk`
  (`sdk.js`, `sdk.d.ts`), realtime's app change stream
  (`services/realtime/app_events.go`). The routes, each a route-table row:

  | Entry | Capability | Participant |
  |---|---|---|
  | `/v1/sites/{ticket}/~app/…` | a member's site ticket (its room and artifact; the room read re-checked like the site's files) | `member:<subject>` |
  | `/v1/public/shares/{token}/site/~app/…` | a public share link of a site, resolved on every call, plus the `X-Bnbloc-Visitor` token minted for that link | `visitor:<visitor id>` |
  | `/v1/rooms/{roomId}/apps/{artifactId}/…` | a session reaching the room; with `?job=` a bridge running that job in the room (the content routes' `jobGrant`) | `member:<subject>`, or `agent:<owner>/<core>/<provider>` |

  Under each: `POST|GET c/{collection}`, `GET|PUT|PATCH|DELETE
  c/{collection}/{id}`, `GET me`, `GET events` (SSE). The room entry adds
  the owners' `GET stats` and `DELETE data` (the card's data line and
  `app_data`, steps 4–5). The frame entries add `GET ~app/sdk.js` and
  `GET ~app/sdk.d.ts`; any other path under their `~app/` is a 404 (the
  namespace is reserved inside a site). `POST
  /v1/public/shares/{token}/visitor` mints a visitor token
  (`wire.AppVisitorToken`; body `{token}` renews a still-valid one of the
  same link for the same visitor). Decided while building:
  - **One resolution, one forward.** `appCapabilityOf` resolves (tenant,
    room, app, participant) from the capability alone (a static test
    allows no query parameter but `job` and no body but the visitor
    route's); `forwardApp` is the only call to `/internal/v1/app/…`,
    carrying only `Content-Type`, `If-Match`, `If-None-Match` and `Accept`
    of the client's headers. The room entry's writes need `drive_agent`
    (a viewer reads an app's data through it, never changes it); an app's
    page is open to anyone holding its link.
  - **Visitor tokens** are an `auth` kind of their own (`app-visitor`):
    subject a random visitor id, ID the share id. They last 24 hours and
    never outlive the link. A call without one answers 401
    `visitor_token_required`, a forged, expired or other link's one 401
    `visitor_token_invalid`. A dead link (revoked, expired, public links
    disabled, room gone) answers 404 at once: the app routes never reuse
    a cached resolution.
  - **The handshake** (for step 5's share page; the constants are in
    `internal/wire/apps.go` and `wire.gen.ts`):
    `frame → parent {type: "bnbloc.app.visitor.request", v: 1}`,
    `parent → frame {type: "bnbloc.app.visitor", v: 1, token}`, over
    `postMessage` with target `*` (the frame's origin is opaque). The
    parent answers only a request whose `event.source` is its own app
    frame's window and may push a grant unasked after renewing; the SDK
    accepts a grant only from `window.parent`. The SDK asks at load and
    again on a 401 with one of the two codes, then retries that call
    once (5 s timeout).
  - **The SDK** is a classic script (`<script src="~app/sdk.js">`) that
    defines `bnblocApp`, and `app` unless the page has its own. It uses
    relative URLs (its own directory), no storage and no credentials; the
    change feed is read with `fetch` streaming so the visitor token stays
    a header. The frame's calls are cross-origin (origin `null`), so every
    `~app/` path of the frame entries answers CORS for any origin without
    credentials.
  - **The change feed**: realtime's `GET
    /internal/v1/rooms/{roomId}/app-events` (gateway only) streams a
    room's `app_changed` events and nothing else; the gateway narrows it
    to the app (`appEventsOf`, re-encoding `wire.AppChanged`) and
    re-resolves the capability every 30 s, ending the feed when it died.
    `app_changed` is live only: a reconnecting page re-reads.
  - **Budgets** (per replica, divided like every gateway limit): per
    client IP, per link (the share link, else the app) and per
    participant, reads and the stricter writes (`appRates`); visitor
    minting 20/min (burst 30) per IP.
  - **Logs**: site tickets, share tokens and previewer tickets are
    redacted from request logs with the invite codes and relay keys; the
    visitor token is a header and never logged.
- **Step 5: landed (2026-10-13).** The web
  (`docs/architecture/web-client-multi-form.md`, "Public share links and
  interactive apps"):
  - **App view.** A public link whose share answers a `siteUrl` renders
    `views/share/SharedApp.tsx`: the site frame full-screen inside the
    safe areas, the site sandbox kept, no header, version line or
    download, no page scroll. Other files keep the file page.
  - **Visitor token.** `lib/appVisitor.ts` is its one web owner (boundary
    rule A1): minted on the frame's first request, stored per share under
    a hash of the share token (never the token), renewed an hour before
    expiry with the held token (same visitor) and pushed to the frame,
    answered only to the page's own frame window. Decided while building:
    a second request for the token the frame already holds renews first
    (the gateway refused it, e.g. after a signing-key change; renewal keeps
    the visitor when the token is still valid), falling back to the held
    token when the mint fails; a renewal that does not extend the token
    (the link ends first) is not repeated.
  - **Member tickets.** `SiteViewer` answers no visitor request; the SDK
    only waits for a token after a visitor refusal, which a ticket never
    gets. The desktop's `bnbloc-site` scheme forwarded reads only, so an
    app's writes and change feed failed there:
    `docs/bugs/2026-10-13-desktop-site-frame-drops-app-api.md` (landed
    with step 6).
  - **Data line** (`views/workbench/AppDataLine.tsx`, rules in
    `lib/appData.ts`) on a site's card and in the artifact viewer: counts
    per collection, shown only when the app has data and only to owners —
    the owners' stats route decides (403: no line), the web never
    re-implements `appOwner`. Export (JSON of all collections, CSV of one:
    stamps first, then fields by first appearance, nested values
    JSON-encoded in one cell, formula-like text defused) paged `find` in
    the web at first; step 6 moved it to the server's audited export
    route. Clear data (`DELETE …/data`) sits behind `ConfirmDialog` and
    needs `drive_agent`. The room stream's `app_changed` refreshes the
    counts, coalesced per app. **Delete app data** (the key and all rows)
    is what Clear data does today: the room entry has one wipe.
- **Step 4: landed (2026-10-13).** The room tool `app_data`
  (`services/bridge/roomtools/appdata.go`), `.room/apps.md`
  (`internal/roomlog/apps.go`), one session guidance line, and three
  built-ins (`internal/contentbuiltin/packages/`): the `interactive-apps`
  skill pack, the `app-builder` expert that requires it, and the
  `app-starters` prompt pack. Decided while building:
  - **One tool, eight actions.** `app_data(action, …)`: `list` (without
    `artifactId` the room's apps, with it the app's collections and
    counts from `stats`), `find` (filter, sort, limit, after), `get`,
    `insert`, `put` (`ifAbsent`, `ifVersion`), `patch` (`doc` merged, `inc`
    added), `delete`, and `wipe`, which needs `confirm: true`. It calls the
    room entry with the job's grant (`roomlog.AppPath`), so the agent is an
    owner and its writes are stamped as the agent. Every returned document
    is one JSON line in one fence (`roomlog.Fence`), at most 96 KiB a call;
    a find cut there continues with `next`. The descriptor costs 345
    tokens of tools/list, whose budget moved from 2,200 to 2,550 (session
    prefix 2,900 to 3,250); the guidance line costs 39.
  - **An app is a site with data.** "Which deliverables are interactive
    apps" is one rule (`roomlog.ReadApps`): a site among the newest listed
    deliverables whose data holds at least one document, as the owners'
    `stats` says. Scanning a bundle for `~app/sdk.js` was rejected: it
    downloads every site each turn and still says nothing about data. An
    app nobody used yet is not listed; `app_data` takes any site by id.
  - **`.room/apps.md`** appears once the room has an app and is then
    rewritten every turn of the session (`CtxState.AppsListed`), so it
    never claims data that was deleted; a room whose usage cannot be read
    and that never had the file gets none. It lists each app's producer and
    collection counts, never document content. `INDEX.md` names it.
  - **The skill documents the SDK, pinned to it.** The skill's SDK section
    may name only what `sdk.d.ts` declares and must name every app and
    collection method and option; its examples may call only declared
    methods; its filter operators and caps are the server's
    (`TestInteractiveAppsSkillMatchesSDK`). The always-on parts have
    budgets (`TestAppBuiltinsAlwaysOnBudget`); the long examples are the
    skill's `references/examples.md`.
  - **Starters.** `/poll`, `/survey`, `/signup` and `/app-report`, each
    with variables, not flagged as empty-room starters (a first message
    should need no form); the App builder offers them as its quick prompts
    (`ref`).
  - Not in this step: auditing an agent's reads of app data (§4.7); it
    landed with step 6.
- **Step 6: landed (2026-10-13).** `BNBLOC_SITES_ORIGIN`
  (`services/gateway/sites_origin.go`), the audited export and agent reads
  (`services/room/appdata_export.go`, the CSV owner
  `internal/appdata/export.go`), and the desktop's app API
  (`docs/bugs/2026-10-13-desktop-site-frame-drops-app-api.md`). Decided
  while building:
  - **The sites origin is a gate in front of the whole mux**
    (`withSitesOrigin`): with it set, a request is served only when it is a
    site path (`sitePath`: `/v1/sites/{ticket}[/…]`,
    `/v1/public/shares/{token}/site[/…]`, files, `~app/` and SDK alike) on
    the sites origin, or any other path on the main origin; everything else
    is a 404 with `no-referrer`. The sites origin therefore serves no API
    route, no web client and nothing that takes a session (the visitor mint
    and the share JSON stay on the main origin: the share page calls them).
    Hosts compare case-blind without the https default port; the ingress
    must keep the Host header.
  - **Validation at startup** (`ParseSitesOrigin`): a bare https origin (no
    path, trailing slash, query, fragment or credentials) whose host is not
    `BNBLOC_PUBLIC_URL`'s, since a same-host origin on another port would
    share the main origin's cookies. Anything else stops the gateway.
  - **One URL owner.** `Service.siteURL(path)` is the sites origin plus the
    path, or the bare path without one (the origin the client called, as
    before). Member site tickets and a public link's `siteUrl` use it. The
    web resolves a path against the gateway and uses an absolute URL as is
    (`siteGatewayUrl`); it never picks a host, and the web client has no
    `frame-src` to widen (the gateway serves it with
    `frame-ancestors 'self'` only).
  - **The desktop asks the gateway** where sites are:
    `GET /v1/public/client-config` (`wire.ClientConfig.sitesOrigin`, a
    public route of no data). The `bnbloc-site` scheme forwards there
    (cached 5 minutes per gateway), so the shell's static CSP keeps
    admitting only the scheme. A scheme responder takes one body, so the
    change feed is held by the shell and answered as successive batches
    (the bug record).
  - **An agent reads a site's files on the main origin** (closed gap). The
    room tool `read_artifact` used to read a site's files through a member
    site ticket with the bridge's session; with a sites origin set the
    ticket's `siteUrl` was absolute and the tool refused it ("unreadable
    result"). It now reads one file through a session route on the main
    origin,
    `GET /v1/rooms/{roomId}/artifacts/{artifactId}/versions/{v}/files/{path...}?job=`
    (`services/gateway/site_file_read.go`, a route-table row): the bound
    job's grant (`jobGrant`), the room service's read as the job's
    requester (a non-member or another room's artifact is a 404), the
    pinned bundle (`siteBundle`, `LoadPinned`), the file exactly as stored,
    4 MiB at most (413), served as a sandboxed attachment. The tool never
    opens a site ticket or site URL (`TestRoomToolsNeverOpenSiteTickets`);
    `TestReadArtifactReadsSiteFileWithSitesOrigin` reads a site with a sites
    origin set.
  - **Audited export** (`GET /v1/rooms/{roomId}/apps/{artifactId}/export?
    collection=&format=json|csv`, the room entry, owners only: a member who
    may chat, or the job's agent). The room service streams it
    (`/internal/v1/app/export`): JSON as `{"collections": {name: [doc,
    …]}}` of every collection (or the one named), CSV of one collection
    (`format=csv` needs `collection`), its columns collected in a first pass
    so the header is complete before any row. A failure after the answer
    began aborts the response, so a download is never short and whole-
    looking. The web's Export calls it and only names the file; its paging
    and CSV code are gone, and the CSV rules (`appdata.CSVColumns`,
    `CSVWriter`, `CSVCell`: CRLF, `encoding/csv` quoting, formula-like text
    defused, nested values as JSON text) have one owner on the server.
  - **Audited reads** (§4.7): `auditAppRead` in the room service is the one
    writer and `appReadAudited` the one rule. One `internal/audit.Record`
    row per call, never per document: kind `app`, name the app, action
    `export`, `find` or `get`, the actor (a member's subject; for an agent
    its owner, with `agent=<owner>/<core>/<provider>` in the detail), and
    `room=`, `collection=` (`*` for every collection), `rows=` and, for an
    export, `format=` in the detail. Every export is audited (outcome
    `failed` when it broke off), and every agent read of documents (`find`,
    `get`); a member's or a visitor's reads through the app itself are not
    (that is the app being used), nor reads of no document (`me`, `stats`).

### Tests

Each step lands with the tests for what it owns.

**Scope and isolation**
- app A's capability never reaches app B (static and behavioural);
- the client cannot set `_by` or any other stamp;
- a revoked or expired link, or a disabled org, stops reads and writes.

**Concurrency**
- `ifVersion` returns 409 on conflict;
- `$inc` loses nothing under concurrency;
- `ifAbsent` creates exactly once under concurrency.

**The two rules**
- a non-author, non-owner cannot update or delete;
- a document not marked public is hidden from other link holders;
- visitors see only pseudonymous `_by`.

**Encryption**
- the `body_sealed` column never holds plaintext;
- a ciphertext moved to another row fails to open;
- after deletion, documents are unreadable even with the rows restored (the
  key is destroyed).

**Other**
- capability tokens never appear in access logs;
- an unknown filter operator is refused;
- memory and MySQL pass the same contract suite.
