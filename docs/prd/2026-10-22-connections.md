# Connections: agents act as the person who asked, in the systems that person connected (2026-10-22)

Status: proposed, ready to build. Owner: the **connections component**
(`bnbloc-connections`, its own repository: connectors, connecting,
grants, policy, activity) and its provider components (an organization's
SSO in the internal repository). The platform adds only generic primitives
that any component may use: an attested turn, egress through a component,
room facts from a component, component cards, component pages, a sealed
per-member store, component audit (§2.4), and the Sandbox Protocol's
`hostProxy`.

A member connects the systems their agents should reach in their name: the
company wiki and issue tracker through the organization's single sign-on, a
calendar or a drive through the provider's OAuth, a service through an API
key. Their own agent then works in those systems **as them**: what the
member can see there is what the agent can see. The agent gets nothing new.
It calls the system the way it calls anything, with `curl`, a script or a
CLI, and the call is authenticated on its way out, where the agent cannot
see it. The credential is never in the agent's context, workspace,
sandbox or room. Reads are allowed; every change is the member's click
unless they allowed changes for that system; every call is recorded.

## 1. Why

- QA 2026-10-21, a release room (member `maya`, own agent, sandbox mode
  `all`). The release CLIs needed CI tokens, so maya pasted two personal
  tokens into the room. The agent stored them in the CLIs' config, where
  every later command, and anyone reading the workspace, could use them.
  When the agent moved into her sandbox, the tokens and the CLIs stayed
  behind.
- A host-substituted sandbox secret was checked live and set aside
  (docs/prd/2026-10-21-durable-sandbox-homes.md §2.2). It covers a token
  sent as-is in a header and fails on local format checks, token exchanges
  (the token they return lands in the sandbox), and encoded or signed
  credentials.
- Two designs we studied:
  - **An internal assistant service** reaches about thirty internal
    systems as the chatting user. A bot sends a consent card for each
    system's SSO app, and an HTTPS interceptor injected into each skill's
    Node process turns the consent into the system's session cookies. It
    proves the consent path. But the session cookies and the bot's key sit
    in the sandbox the agent's commands run in; only Node gets credentials;
    consent is triggered by a deliberately failing command; the token comes
    back as a chat message recognized by its shape; the agent holds the
    system's whole web session, with no read-only mode and no record of its
    calls.
  - **An open-source desktop assistant** connects about 120 SaaS toolkits
    through a managed connector platform. The agent calls tools and never
    sees credentials. The boundary is right, but the tokens live with a
    third party, internal systems are out of reach, and each system is a
    tool the agent must learn.
- What bnbloc has, checked against the code: MCP tool settings and the
  desktop's MCP OAuth bind a credential to one tool and one desktop, and
  host-run MCP tools (docs/prd/2026-10-22-host-run-mcp-tools.md) keep a
  tool's own sign-in outside the sandbox; neither gives an agent's own HTTP
  a member's access. `jobGrant` (services/gateway/content_requester.go) is
  the one admission of "this call acts for the requester of the turn being
  driven". Endpoint components hold one sealed credential and sign their
  calls. Share targets serve a component's page to a member through a
  ticket. Nothing lets a component act for a turn.

## 2. What it is (the end state)

### 2.1 The parts

| Part | Where | What it owns |
|---|---|---|
| **Connections** component | `bnbloc-connections` (an endpoint component in its own repository) | connectors, connecting (OAuth 2.0, API keys, provider components), the grants, the policy, held changes, activity, its pages, its facts |
| **Provider** components | their own repositories (the organization's SSO: the internal repository) | one protocol each for obtaining and refreshing a member's access, under the connections component's provider contract |
| **Connectors** | content the connections component holds (samples in their own repository) | one system: its hosts, how one connects, read/write policy, its guide |
| **Platform primitives** | the main repository | generic, for any component (§2.4); no connection nouns |

### 2.2 What a member does

1. An admin adds a connector (from a sample, or drafted by an agent, §2.6)
   and sets its client values on the connections component's admin page.
   Each organization enables the ones it wants.
2. The member connects a system when they need it. Either from the
   connections page (Components → Connections → Open), or from the card
   that appears the first time their agent tries to use it. Connecting runs
   the system's sign-in: the provider's OAuth page, the SSO consent card,
   or a field for an API key. There the member picks the reach: read, or
   read and changes.
3. In any room, their own agent uses it. A read just works. A change the
   member did not allow for that system shows a card first: "your agent
   wants to create an event on Friday 15:00 in your calendar: Allow". On
   Allow, the change is made and the card shows its result. Each member's
   agent uses that member's connections. Someone borrowing an agent never
   uses the owner's.
4. The connections page shows each connection's state and its activity,
   and disconnects it. Disconnecting revokes the access at the provider.

### 2.3 What the agent sees: nothing new

- **Its ordinary HTTP works.** A turn of the member's own drive with
  connections in effect runs its commands with `HTTPS_PROXY` set to the
  bridge's egress proxy (§2.4.2). A request to a connector's host goes out
  through the connections component with the member's access. Every other
  request goes out as before, never decrypted.
- **What it knows is one line and some files.** The room facts
  (`.room/README.md`) carry one line from the component, whatever the
  number of connections: "Systems you can reach as maya: calendar, drive,
  wiki, issues (see .room/connections/)". The details are files the agent
  reads when a task needs them, as it reads the room's history:
  `.room/connections/INDEX.md` (each system: its hosts, what it is for,
  connected or not, changes asked or allowed) and
  `.room/connections/<system>.md` (the connector's guide, with `curl`
  examples). They are rewritten each turn, so connecting mid-conversation
  never changes the session's content and never starts a new session.
- **Refusals are ordinary HTTP.** A request to a system not yet connected
  answers `403` with a JSON body: not connected, and the person was asked
  in the room. A change awaiting the member answers `202` with a JSON
  body: held, and the person was asked. The agent reports either like any
  API error and does not ask for a token, since the body says the person
  decides.
- A CLI that will not run before its own login gets a placeholder from the
  connector (`cliEnv`, e.g. `{"GH_TOKEN": "connected"}`). Every credential
  sent to a connector host is replaced, so the placeholder goes nowhere.

### 2.4 The platform primitives

Each is generic, named by what it does and not by connections, and usable
by other components. A component gets one only by declaring it in its
package.

#### 2.4.1 An attested turn (`turnProxy`)

- An endpoint component that declares `turnProxy` gets
  `ANY /v1/turn-proxy/{component}/{path...}`. Only a bridge calls it, for a
  job it drives, with `?room=&job=`. Admission is `jobGrant`: the job is
  live, leased by the caller's bridge, and its requester may still post in
  the room.
- The gateway forwards the request to the component, held to its
  `permissions.network`, signed like every call to an endpoint component.
  It adds `X-Bnbloc-Turn`: `{org, room, channel, job, requester,
  agentOwner, drive}`. `drive` is `own` when the requester owns the agent,
  `borrowed` when they do not, and `platform` for a platform agent. The
  component decides what that permits; the platform only attests it.
- Bounds: request and answer bodies 8 MiB; no redirects followed.

#### 2.4.2 Egress through a component (`egress`)

- A component that declares `egress` answers `GET /egress/hosts` through
  its turn proxy: the hosts it handles for this turn (none for a drive it
  will not serve).
- The **bridge's egress proxy** (one per bridge, on loopback) is set as
  `HTTPS_PROXY`/`HTTP_PROXY` in the commands of a turn whose components
  claim hosts. It authenticates with a bearer bound to that job and
  revoked with the turn, like the room tools bearer. A `CONNECT` to a
  claimed host is answered with a certificate from the member's local CA.
  The proxy reads the request, sends it to the component's turn proxy
  (`POST /egress/request`, the HTTP request as an envelope), and answers
  with the component's response. Every other `CONNECT` is tunnelled.
- **The local CA**: one per member. Its key is 0600 in the member
  directory, never leaves the computer and is replaced on sign-out. It
  issues leaf certificates only for claimed hosts. Only the turn's
  commands trust it: `SSL_CERT_FILE`, `NODE_EXTRA_CA_CERTS`,
  `REQUESTS_CA_BUNDLE` and `GIT_SSL_CAINFO` point at a bundle that adds it.
  The member's own system trust store is never touched.
- **Sandboxed turns**: the Sandbox Protocol gains `hostProxy`. A space may
  reach one host loopback port the bridge names, and nothing else on the
  host, enforced outside the guest. The reference implementation maps it
  through the runtime's host alias. Without the capability a sandboxed
  turn gets no egress proxy, and the component's facts say so.

#### 2.4.3 Room facts from a component (`roomFacts`)

- A component that declares `roomFacts` answers `GET /facts` through its
  turn proxy: `{line, files}`. `line` is at most 200 characters and is
  added to `.room/README.md`. `files` (at most 64 KiB in all) are written
  under `.room/<component>/`. The bridge asks once per turn, with the
  `.room/` refresh. When the component cannot be read, it writes no files
  and the line says so.
- The session prefix budget test covers the line. The files are read by
  the agent when it needs them, and are not part of the prompt.

#### 2.4.4 Component cards

- A signed `POST /v1/component-cards` from an endpoint component:
  `{turn, title, text, actions}`, where `turn` is the `X-Bnbloc-Turn` it
  was given. The card is a notice in that room, shown **only to the turn's
  requester** (like a publish proposal). Actions are `open` (one of the
  component's pages, §2.4.5) and `ask-again` (the web sends the turn's
  request again to the same agent).
- Text is plain; the platform renders it and never puts component HTML in
  the feed.

#### 2.4.5 Component pages and callbacks

- `POST /v1/my/component-pages/{component}/{page}` (a member route)
  issues a ticket naming the member, their organization and the page. The
  page is served from the component at the ticket's address, sandboxed by
  CSP, the way a share target's page is today. Share targets move onto
  this one mechanism: their `/v1/share-targets/{name}/pages/…` routes go
  (pre-launch).
- `GET|POST /v1/component-callbacks/{component}/{path...}` is open and
  forwarded signed to the component, so an OAuth provider can redirect to a
  stable URL. The component checks its own `state`.

#### 2.4.6 A sealed store per member and component

- Signed `GET|PUT|DELETE /v1/component-store/{member}/{key}`. A value is
  at most 64 KiB, sealed with the platform key (`internal/sealbox`) and
  readable only by the component that wrote it. A member's values go when
  the member is deprovisioned; all of a component's values go when it is
  uninstalled.

#### 2.4.7 Component audit

- Signed `POST /v1/component-audit`: `{turn?, member, action, target,
  outcome}`. It becomes a row in the organization's audit log, attributed
  to the component.

### 2.5 The connections component

- **Connectors** are its content: `{name, displayName, purpose, hosts,
  connect: {provider, scopes, expiredWhen}, policy: {read, write},
  cliEnv, guide}`.
  - `hosts` are exact names.
  - `policy` sorts each request into read or write (a search that is a
    POST can be a read); anything matching neither is refused.
  - `expiredWhen` says how the system answers an access it no longer
    accepts when that is not a 401 (a 302 to its login page, a 200
    carrying its login form).

  Admins manage connectors on its admin page; each organization enables
  its own.
- **Connecting**:
  - `oauth2`: authorization code with PKCE, issuer discovery, `state`,
    RFC 9207 and refresh rotation, redirecting to its component callback;
  - `api-key`: how a request carries the key (bearer, a header, a query
    parameter, basic);
  - **provider components** for anything else, through the component's
    own provider contract: `connect`; `authorize`, which answers the
    headers and cookies one request carries until a given time; `revoke`.
- **Grants** go in the platform store (§2.4.6). Derived credentials (an
  access token, a session cookie) are held in memory until they expire.
- **Serving a request** (`POST /egress/request`):
  1. The turn must be `drive: own`: the member's own agent, or a schedule
     they created. A borrowed drive would put the requester's data on the
     agent owner's computer.
  2. The host must be an enabled connector's.
  3. The connection must be connected; otherwise `403` and a card.
  4. The policy class must be read or an allowed write; a write not
     allowed is held (`202` and a card).

  The component re-authorizes once on a 401 or an `expiredWhen` match.
  It strips every credential the agent sent and adds the member's, drops
  `Set-Cookie` from the answer, records an audit row, and enforces a rate
  per member and connector.
- **Held changes** run on the card's Allow (its page) with the member's
  grant; the card then shows the outcome. A schedule cannot be asked, so
  its change needs changes allowed in advance, or it is held for the
  member like any other.
- **Facts**: the one line and the `.room/connections/` files (§2.3),
  including `ACTIVITY.md`, the requester's recent calls through this
  room's agent.

### 2.6 AI first

The AI capability is how the component is used; the pages are its
fallback with the model off.

- **The agent finds the need; the member decides.** A request to a system
  not yet connected, or a change not yet allowed, puts a card in front of
  the member at that moment. The card names the system, what the agent
  tried and the reach asked for. After Connect or Allow, Ask again resends
  the request.
- **The agent drafts connectors.** Asked "connect us to <system>", the
  agent reads the system's API description and writes a connector draft
  and its guide. It posts the draft to the connections component's own API
  (a claimed host, through egress like any call). An admin sees it on the
  admin page with a card, and enables it or not.
- **The agent explains activity.** "What did my agents do in the wiki
  this week" is answered from `.room/connections/ACTIVITY.md`. No model
  runs inside the component.
- No model sits in the request path: read or write is the connector's
  declared policy, and a change is the person's click.

## 3. Design review

| Lens | Answer |
|---|---|
| Actors | The member connects and decides changes; an organization admin enables connectors; a platform admin adds them and sets client values. A connection serves only its owner's own drives and schedules (`drive: own`, attested by the platform). |
| Time and lifecycle | A grant lives until disconnected, revoked upstream, the member deprovisioned or the component uninstalled. Derived credentials live in memory until expiry. A turn never waits for a click: it ends with the card, and Ask again continues. A refresh refused mid-run answers that call `403` with a card. |
| Scale and cost | One sealed value per member and connector; one upstream call per request plus a gateway hop; the facts call once per turn; caps on bodies and a rate per member and connector. |
| Failure and recovery | The component down: claimed hosts answer `502` naming it, the facts line says it could not be read, nothing falls back to an unauthenticated call. An upstream refusal is reported as the system's own error; an expired grant asks to reconnect. |
| Boundaries and privacy | Grants are sealed with the platform key and readable only by the component; never in an answer, a log, a workspace, a sandbox or a room. The local CA is trusted only by the turn's commands, and only claimed hosts are decrypted. Audit rows hold paths, never bodies or query values. |
| Abuse and untrusted input | A page the agent read cannot change anything without the member's click. A request names a path on a claimed host only; agent-sent credentials are stripped; the component refuses loopback, link-local and metadata addresses and redirects off its hosts. Card text is plain text the platform renders. |
| One owner | `jobGrant` admits every turn call; one egress proxy in the bridge; one page mechanism (share targets move onto it); one store; connections' policy is one function in its repository. |
| Defaults and fallbacks | No connector until an admin adds one; each is enabled per organization; a new connection is read-only; changes are asked per call; a component that does not declare a primitive gets none of it. |
| External contracts | OAuth 2.0 (PKCE, RFC 8414/OIDC discovery, RFC 9207, refresh rotation); the Sandbox Protocol's `hostProxy` with a conformance step; each primitive's wire types in `internal/wire` and `component/`; the connections provider contract with its probe. |
| Load on users and agents | Agents: no tool, no rule, one line of facts, files read on demand, no session restarts. Members: one card when something is needed, one page to manage. |
| Release and compatibility | All primitives are additive; share targets' page routes are replaced by the generic one (pre-launch, no compatibility). The component releases on its own; the bridge's proxy and facts ship with the desktop; `hostProxy` is optional. |
| Observability | An audit row for every call; the component logs each refusal with its reason; the bridge logs claimed-host routing per turn; the turn proxy logs status and latency per component. |
| AI capability | The agent notices a need and the member decides; the agent drafts connectors; the agent explains activity from a file (§2.6). |
| Component or platform | The platform adds what only it can: attesting a turn, routing an agent's egress, writing room facts, posting a card to one member, serving pages, a store sealed with its key, audit rows. Everything about connections, providers and systems is a component or its content. |

**Pre-mortem.**
1. *A fetched page talks the agent into deleting things.* Changes are
   held per call by default and run only on the member's click; the card
   shows the method, path and body summary.
2. *The egress path becomes an internal scanner.* Only claimed hosts are
   intercepted, and the component claims only its connectors' exact
   hosts. It refuses loopback, link-local and metadata addresses and
   redirects elsewhere.
3. *Someone borrows an agent to act as its owner.* The platform attests
   `drive`, and the component serves `own` only.
4. *The store leaks.* Values are sealed with a key the store does not
   hold and readable only by the component that wrote them; derived
   credentials are never written; disconnect, deprovision and uninstall
   delete.
5. *The local CA is turned against the member.* Only the turn's commands
   trust it; it issues certificates only for claimed hosts; its key never
   leaves the member directory.
6. *Twenty connections crowd the agent.* The session gets one line of
   names; details are in files; the budget test holds the session prefix
   with twenty connectors to the same budget plus that line.
7. *The agent does not use a system because it does not know it can.*
   The facts line names it and INDEX.md says what each one is for. The
   agent task suite checks that a plain `curl` to a connected wiki page
   works with no tool and no prompt rule.
8. *The SSO path fails as "you have no access".* A provider maps its
   upstream reasons: the platform's bot not yet allowed to reach a host is
   an admin item, not a member error.

## 4. Build order (slices of §2)

Each slice ships end to end. The platform primitives come first, then the
component that uses them.

1. **Platform primitives**: `turnProxy`, `egress` (the bridge's proxy and
   local CA), `roomFacts`, component cards, component pages and callbacks
   (share targets moved onto them), the component store, component audit;
   the Sandbox Protocol's `hostProxy` with its conformance step and the
   reference implementation.
2. **The connections component** (`bnbloc-connections`): connectors,
   `oauth2` and `api-key`, serving, held changes, the pages, the facts, and
   the AI paths of §2.6. Samples in their own repository: a public code
   host (API key and OAuth) and a calendar (OAuth).
3. **The organization's SSO provider** (internal repository) and the
   internal wiki and issue tracker connectors. Operator provisioning per
   system runs in parallel.

Not in this round:
- Non-HTTP protocols (a database protocol, SSH). Each waits for a system
  that has no HTTP API.
- Moving the desktop's MCP OAuth (`mcp.http.auth: oauth`) into
  connections. It needs its own design.

## 5. Open questions

- The organization's SSO: the consent bot's type and permissions, the
  per-system gateway allowlist for the platform's bot, and the token's
  lifetime and refresh (operator items, in the internal addendum).
- Which external OAuth clients the organization registers, and which
  sensitive scopes need the provider's review.
