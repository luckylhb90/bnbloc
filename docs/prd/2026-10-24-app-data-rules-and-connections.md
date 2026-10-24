# App data rules, limits and connections for apps (2026-10-24)

Status: **proposed**. Companion to
`docs/prd/2026-10-24-deliverable-backends-and-showcase.md`, which gives apps
backends. This record changes the **app base** and the **connections
contract**; hosting is not in it. Builds on
`docs/prd/2026-10-12-interactive-apps.md`,
`docs/prd/2026-10-15-app-identity-schedules-notify-ai.md`,
`docs/prd/2026-10-22-connections.md`,
`docs/prd/2026-10-23-connections-digital-workers.md` and
`docs/prd/2026-10-24-connections-room-identity.md`, and
`docs/architecture/service-components.md`.

Owner: the room service (`services/room/appdata.go`), the gateway
(`services/gateway/apps.go`, the SDK in `services/gateway/appsdk`), the public
`component/` contract (`appProxy`), and, in their own repositories, the
connections component and the channel component.

## 1. Why

Checked against the code on 2026-10-24:

| Finding | Where |
|---|---|
| **Any participant can create a document in any collection and mark it `public`**, and then everyone who reads the collection sees it. Rule 1 covers changing an existing document, rule 2 covers reading; nothing covers who may *create* a public one. A visitor on a link can put a public entry into a collection the owner uses for options, a published tally or an announcement, and the page lists it. Not yet reproduced by a test: slice 1 is that test. | `createAppDoc`, `handleAppInsert`, `handleAppPut` (`services/room/appdata.go`); the two rules in its package comment |
| Writes are slowed, not stopped: 2 per second per participant (burst 10), 5 per IP, 50 per link, and 100,000 documents per app. | `appRates` (`services/gateway/ratelimit.go`), `wire.MaxAppDocs` |
| A page cannot read the app's caps or usage. `stats` is an owner route and the caps are compiled constants, so a page hardcodes them. | `handleAppStats` (`appOwner`), `internal/wire/limits.go` |
| Every notification between participants is a stored document change; the feed carries ids only and the page re-fetches. There is no transient message, so a cursor, a typing hint or a game move must be written as a document. | `announceAppChange`, `wire.AppChanged` |
| A page has no network, and the connections component acts only for an agent's turn (`turnProxy`, `egress`). An app cannot use a system its member connected. | `docs/architecture/service-components.md` |

What is **not** a gap here: a find scans at most 2000 documents and answers a
cursor when it stopped early, sorts by `_id` only, and has no aggregation or
multi-document write. That is a document store's honest shape for hundreds to
tens of thousands of documents, and the answer past it is the app's own
backend with its own database (the companion record). Not changed here.

## 2. Principle

The platform adds only what client code cannot enforce: ownership and privacy,
verifiable identity, safety caps, isolation. Closing time, one vote per person,
validation and tallies are the app's own code, and where the app must be
trusted against a caller who skips its page, that code is a **backend**. This
record therefore adds one rule, one read, and one primitive for service
components (`appProxy`); no schema, no participant modes, no app lifecycle. A
realtime channel, which an earlier draft put in the platform, is a component
(§5).

### Placement: content, component or platform

The core rule (AGENTS.md "the platform provides only what it must"), per part.

| Part | Placed as | Why | What an organization would swap |
|---|---|---|---|
| Rule 3 and its one switch | **Platform** | ownership and privacy: a rule client code cannot enforce | the switch per collection is the app's |
| `limits` read | **Platform** | only the platform knows its caps; a constant read | nothing |
| A realtime channel | **Component**: a service component through `appProxy`, with WebSocket pass-through | needs a server, not the platform's identity or privacy rules; an organization may want a different one | install another |
| `appProxy` (attested app caller, admission, budgets) | **Platform** | identity and the attested caller; the member-authenticated proxy | nothing; services implement it |
| Everything connections does with an app call: consent, grants, whose access, held changes | **Component**: the connections component, in its repository | it already owns these for agents | its own policy |
| Guidance to agents (declare an opened collection, ask the page for limits, a tally is an owner's job) | **Content**: the app skill pack | agents already read skills | edit the pack |
| The summary of what an app can do with a connection, drafting `bnbloc.data.json` | **Content**: assist packages | the assist seam exists | their own prompts |
| Example apps that use a channel or connect | **Content / sample**: their own repository | a use case is a sample | copy and change |

## 3. Rule 3: a participant writes private documents; public ones are the owners'

- **The rule.** A document that is not an owner's is written private. A
  non-owner's insert, create-by-id, or change that sets `public: true`, on a
  collection the owners have not opened, is refused (`403 public_write_closed`).
  Owners are unchanged: the room's members who may act in it, the room's
  agents, and the app's backend token (companion record §5.3).
- **Opening a collection.** One owner switch per collection:
  `participantsMayPublish`, for a shared wall, a guest book, a board. It lives
  in `bnbloc.data.json` at the site root, read once per site version while its
  bytes are stored (as the SDK reference is detected, `appdata.SDKScan`), so it
  is versioned with the code and costs the agent no tool:

  ```json
  { "collections": { "wall": { "participantsMayPublish": true } } }
  ```

  Shape and size are validated by the platform; the file names no other rule.
- **Where it is enforced.** One function on `appCall` answers "may this call
  write a public document into this collection"; every path that creates or
  changes `Public` calls it; a static test fails if a new one does not.
- **Public aggregates stay possible**, written by an owner: the agent
  (`app_data`), the app's backend, or an `ai` schedule: options, a published
  tally, an announcement. A page that opens as an owner writes them directly.
- **The SDK's demo mode** applies the same rule (it already mirrors the API's
  rules), and the SDK drift test covers it.
- **The skill pack** says: participants write private documents; a collection
  that people publish into is declared in `bnbloc.data.json`; a tally people
  trust is written by an owner side.
- **Not proposed**, on purpose: per-collection `keyBy`, per-participant
  maximums, opening and closing times, or schemas. An app can defend a private
  collection at read time (an owner side counts only valid documents), and an
  app that must refuse a write at the door has a backend. If a real app shows
  a case neither can serve, it is a new record.

## 4. Limits a page can read

- `GET ~app/limits`, open to every participant of the app, no data: the caps
  that bound the app (documents and bytes per app, per document, collections,
  a find page, upload sizes, the AI prompt and input, notify recipients),
  read from the same wire constants the checks use. Owners get the app's usage
  with it (what `stats` returns today, still owner-only).
- SDK: `app.limits()`, in `sdk.d.ts`. The skill and the starters stop stating
  numbers; the page asks.
- One owner: the wire constants. The route builds its answer from them and the
  SDK drift test pins its shape.

## 5. The channel is a component, not the platform

Participants cannot reach each other without a server, and writing a document
per cursor move costs storage, change events and the app's document cap. But a
transient fan-out needs nothing the platform alone can provide: it is not
identity, privacy or isolation. By the core rule it is a **service component**
that declares `appProxy` (§6.1), in its own repository:

- A page calls it as `~app/connect/{channel-service}/…` under its capability.
  The platform attests the caller and its kind; the component fans messages out
  among the app's connected participants, never stores or replays them, stamps
  each message with the sender's per-app pseudonym it was given, and bounds
  topics, message size, subscribers and rates itself.
- `appProxy` therefore carries **WebSocket upgrades and streams** (a contract
  requirement with a conformance case), as the backend ingress does.
- An anonymous visitor's rights are the component's to decide from the attested
  kind; the default it ships is subscribe-only.
- An organization that wants no channel installs none; one that wants another
  installs another. A backend (the companion record) may also serve its own.
- The SDK gains nothing: a page uses the component's own small client, shipped
  with it and described in a skill, as with any service.

## 6. Connections for apps

### 6.1 The primitive: `appProxy`

A service component that declares `appProxy` receives calls an **app** makes,
as `turnProxy` gives it a turn's:

- A page calls `~app/connect/{service}/{path...}` under its capability. The
  gateway admits it (the app's scope, the capability's kind, the route's
  budget), forwards it to the service, held to its `permissions.network`, and
  signs it like every service call.
- The platform adds `X-Bnbloc-App`: `component.App` `{org, room, app, kind,
  participant, account?, origin}`, with `kind` one of `member`, `org-link`,
  `anyone-link`, `demo`. The service decides what a kind may do. The platform
  only attests.
- Bounds as the turn proxy: bodies at most 8 MiB, no redirects followed, no
  cookie in either direction, the upstream chosen by the service and never by
  the request. A service gets the primitive only if its package declares it
  (`servicecomp.Declares`).
- The public contract (`component/service.go`) gains the shape; nothing in the
  platform names connections.

### 6.2 What the connections component does with it (its own repository)

- **Whose access.** An app call acts as the **caller**, the person who opened
  the page: their own connection. It resolves the identity with the same
  `actingAs` function as an agent's request, with a drive kind `app`, so a room's
  required worker and an admin's audience bound it exactly as they bound an
  agent (`docs/prd/2026-10-24-connections-room-identity.md`). A worker serves an
  app only if its audience names apps. **Nobody's app ever acts as another
  person's own connection.** `anyone-link` and `demo` callers get nothing.
- **Consent is per app and per system, the caller's, and not the page's.** The
  first call to a system the caller has not allowed this app to use answers
  `403 consent_required` with the connector. The SDK
  (`app.connect.ensure(connector)`) opens the **component's own consent page**
  for the member, a platform-opened window the page cannot draw or script, where
  the caller allows this app to read, or to read and change, that system. The
  grant is the component's, keyed (member, app, connector, reach) and revocable
  on the Connections page. The page never sees a credential.
- **Reads work; changes are held** for the caller's click, as for agents: the
  call answers `202` with a held state and the SDK surfaces it and opens the
  component's page. Every call is recorded by the component's audit header with
  the app and the caller.
- **What an app asks for is shown.** `bnbloc.run.json`'s `connections[]` and
  `bnbloc.data.json`'s optional `connections[]` list the systems the app will
  ask to use; the gallery shows them ("This app asks to use: wiki, read"), and a
  copy carries no grant.
- **Not in this record**: an app acting with the **owner's** access for
  everyone who opens it. It is a different risk (a visitor acting with someone
  else's access, the case another hosted-page tool accepts and documents as a
  trade-off), so it would need its own record, an explicit per-app owner choice,
  and a banner on the app. A backend calling a system **on behalf of a caller**
  needs a short-lived delegation of the signed caller header (bound to the app,
  one audience, seconds of validity); until that exists a backend reaches only
  what its host's egress allows, and `connect` is a page's.

## 7. Delivery slices

| # | Slice | Acceptance, and the tests that would fail if wrong |
|---|---|---|
| 1 | **Reproduce and close the public-write hole** (§3). A failing test first, then the rule, the switch, the SDK demo-mode mirror and the skill update; a bug record in `docs/bugs/` if the test reproduces. | The test fails on today's code: a non-owner (member, org link, anyone link) inserts, creates by id and patches a public document. After: all three are `403 public_write_closed`; with the switch they succeed; an owner, an agent and a backend token are unaffected; every path that sets `Public` goes through the one function (static test). |
| 2 | **Limits** (§4). | The answer equals the wire constants (drift test); a non-owner gets caps without usage; the SDK types match. |
| 3 | **`appProxy`** (§6.1), the contract and the gateway route, with WebSocket and stream pass-through. | App A's capability never reaches another app's call; a service that does not declare it is refused; `X-Bnbloc-App` is signed and a client copy is stripped; the upstream is never taken from the request. |
| 4 | **The connections component's app side** (§6.2), in its repository. | Consent is required once per (member, app, connector), is revocable, and cannot be given from the page; an `anyone-link` and a demo get nothing; a held change waits for the caller; a copy from the gallery has no grant. |
| 5 | **The channel component** (§5), in its own repository, as a sample service built only on `appProxy`. | A message reaches only the app's subscribers and no other app's; nothing is stored or replayed; the sender stamp is the platform's pseudonym; an anonymous visitor cannot publish by default; the component's own limits hold. |

Slices 1 and 2 touch only the app base and are independent of the other record.
Slice 3 precedes slices 4 and 5 and needs no host.

## 8. Design review

| Lens | Answer |
|---|---|
| Actors | The owner, a member, an org-link visitor, an anonymous visitor, an agent, the app's backend, a worker account, a service. For each: writes private or public, subscribes or publishes, may use a connection or not (§3, §5, §6.2). A borrowed agent is an agent and keeps its rules. |
| Time and lifecycle | A grant outlives a page open, so it is revocable and ends with the member's deprovisioning like every connection. The switch is per site version and follows the version the app serves; deleting the app deletes its grants. |
| Scale and cost | `limits` is a constant read; the channel component bounds its own fan-out. `appProxy` shares the turn proxy's body and rate caps. |
| Failure and recovery | A refused public write says why in words. A consent that was not given is a state the SDK reports, never an empty answer. A held change is a state, not an error. A channel component's drops are its own, and its guidance says to write a document for anything that must arrive. |
| Boundaries and privacy | Scope only from the capability; the component sees the caller's kind and never a session; a grant is the member's and never the page's; messages are scoped to (tenant, app). |
| Abuse and untrusted input | The public-write rule is the abuse fix. The channel component's limits and the attested sender stop spam and impersonation; consent cannot be spoofed because the page cannot render it; every system response reaches the page as the page's data. |
| One owner | Public writes: one function. Limits: the wire constants. Channel: its component. Acting-as: `actingAs`. Consent and grants: the connections component. |
| Defaults and fallbacks | Closed public writes by default; an anonymous visitor gets no publish and no connect; no `bnbloc.data.json` means no opened collection; no connections component means `connect` answers a plain "not available". |
| External contracts | `appProxy` is ours and public. The connections component implements it and is tested against it in its repository, with the real gateway where it can be reached. |
| Load on users and agents | A member allows an app once, in one page. An agent learns the switch and nothing else; the skill's numbers go away because the page asks. |
| Release and compatibility | Additive routes and a file; pre-launch, so the default closes now and the built-in skill and starters change in the same release. Gateway and room service first, then the component with `optin`. |
| Observability | Refused public writes are counted per app; the channel component's own counters; consent grants, denials and calls are audit rows (`kind: connection`) naming the app and caller. |
| AI capability | `bnbloc.data.json` and the consent summary are drafted by the existing assist seam where a person edits them ("what this app can do with your wiki, in a sentence"); no model in any request path. |
| Content, component or platform | The Placement table above. Platform: one rule, one read, `appProxy`. Components: all connection behaviour and the channel. Content: the skill's guidance and the assists. Samples: example apps. |

**Pre-mortem.**

1. **"A visitor planted entries in the official list."** The rule closes it by
   default and has a test that fails on the old code.
2. **"An app read someone's wiki without them knowing."** Consent is per app and
   system, given on a page the app cannot draw, reads only by default, shown in
   the gallery, never for an anonymous visitor or a demo, and audited.
3. **"The channel became a spam or a leak path."** It is a component: its own
   limits, no storage, the attested sender, scoped to one app, replaceable;
   nothing reaches an agent.
4. **"Closing the default broke every shared wall."** The switch is one line in
   a file, the skill says so, and the first slice's tests include an app that
   needs it.

## 9. Not verified

That the public-write hole reproduces end to end (slice 1 is that check); that
`appProxy` can carry WebSocket upgrades through the gateway at the bounds a
channel needs; how a consent page
opens from a page in the desktop's site scheme and in a browser; and whether a
backend delegation (§6.2, last item) can reuse the signed caller header as
specified.
