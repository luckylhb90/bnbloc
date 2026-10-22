# Connections: agents act as the person who asked, in the systems that person connected (2026-10-22)

Status: proposed. Owner: the gateway (the connection store, the proxy, the
consent bookkeeping, the audit), the connector and connection-provider
contracts (`core/pkgmeta`), the bridge's forwarding proxy and the Sandbox
Protocol's host proxy capability, connector samples (their own
repository).

A member connects the systems their agents should reach in their name: the
company wiki and issue tracker through the organization's single sign-on, a
calendar or a drive through the provider's OAuth, a service through an API
key. Their own agent then reads and works in those systems **as them**:
what the member can see there is what the agent can see. The credential is
never shown to the agent, never written into its workspace or sandbox, and
never typed into a room. The agent only says what it wants done. The
platform makes the call with the member's credential, within what the
member allowed, and records it.

## 1. Why

- QA 2026-10-21, a release room (member `maya`, own agent, sandbox mode
  `all`). The release CLIs needed CI tokens, so maya pasted two personal
  tokens into the room. The agent stored them in the CLIs' config, where
  every later command, and anyone reading the workspace, could use them.
  When the agent moved into her sandbox, the tokens and the CLIs stayed
  behind.
- A host-substituted secret (the sandbox's `secrets`) was checked live and
  set aside (docs/prd/2026-10-21-durable-sandbox-homes.md §2.2). It covers
  a token sent as-is in a header and fails on everything else: a local
  format check, a token exchange whose result lands in the sandbox,
  encoded or signed credentials.
- What exists, checked against the code:
  - MCP tool components get settings (platform, organization or member
    values; secrets sealed), and an endpoint tool may sign each member in
    with OAuth on their desktop (`mcp.http.auth: oauth`, tokens 0600 in
    `<member>/mcp-sign-ins/`). Both bind a credential to **one tool**, both
    keep member values on one desktop, and neither is used on a drive the
    member did not ask for.
  - The room relay lets a stdio tool reach a short list of gateway routes
    as the requester of the turn being driven (`mcp.credentials: "room"`,
    `wire.RoomRelayRoutes`). The gateway session never leaves the bridge.
  - Sign-in through the organization's SSO is an auth-provider endpoint
    component. Nothing obtains a member's access to another system.
- Two designs we studied:
  - **An internal assistant service** reaches about thirty internal
    systems as the chatting user. A bot sends the user a consent card for
    each system's SSO app, and an HTTPS interceptor injected into each
    skill's Node process turns the consent into the system's session
    cookies. It works, and it proves the consent path. But the session
    cookies and the bot's own key sit in the sandbox the agent's commands
    run in; only Node gets credentials (Python needed a patch, curl and
    git get none); consent is triggered by a deliberately failing command;
    the token comes back as a chat message recognized by its shape; the
    agent holds the system's whole web session, with no read-only and no
    record of its calls.
  - **An open-source desktop assistant** connects about 120 SaaS toolkits
    through a managed connector platform. The agent calls tools and sees
    results, never credentials. The boundary is right, but the tokens live
    with a third party, which an organization will not accept for its own
    systems, and internal systems are not reachable at all.

## 2. What it is (the end state)

Three concerns, three owners:

| Concern | Owner | Shape |
|---|---|---|
| Which system, where, what may be done | **Connector** (a package) | its hosts, how one connects, the default policy, the skill that explains its API |
| How a member's access is obtained and kept fresh | **Connection provider** | OAuth 2.0 and API keys built into the platform; anything else (an organization's SSO app consent) is an endpoint component |
| Where the credential lives and how it is used | **The platform** | the connection store (sealed, per member), the proxy, the policy, the audit |

### 2.1 Connectors

- A component kind `bnbloc.connector.package/v1`, declarative (no
  runtime). It is published, rolled out and scoped to organizations like
  any component.

  ```json
  {
    "kind": "bnbloc.connector.package/v1",
    "name": "acme-wiki",
    "displayName": "Acme Wiki",
    "description": "Search, read and write wiki pages as yourself.",
    "hosts": ["wiki.acme.example"],
    "connect": { "provider": "oauth2", "scopes": ["read", "write"] },
    "policy": {
      "read":  [{"methods": ["GET", "HEAD"]}, {"methods": ["POST"], "paths": ["/rest/api/search"]}],
      "write": [{"methods": ["POST", "PUT", "PATCH", "DELETE"]}]
    },
    "requires": { "skills": ["acme-wiki-api@^1"] },
    "settings": [ {"key": "clientId", "scope": "platform", "type": "text"},
                  {"key": "clientSecret", "scope": "platform", "type": "secret"} ]
  }
  ```

- `hosts` are exact names: the only places a request through the
  connector can go. `connect.expiredWhen` says how the system answers a
  credential it no longer accepts when that is not a 401 (a 302 to its
  login page, a 200 carrying its login form):
  `[{"status": 302, "locationPrefix": "/login"}, {"status": 200, "bodyContains": "id=\"login-link\""}]`. `policy` sorts requests into **read** and **write** (a
  search that is a POST is a read). Anything matching neither is refused.
- What the API looks like lives in a **skill pack** the connector
  requires: ordinary content the member's agent reads when the connector is
  in effect. A connector never holds prompt text.
- The provider's own values (an OAuth client id and secret, an SSO app id)
  are the connector's **settings**, set where every component's settings
  are set (Components → Settings). The platform holds no provider names or
  endpoints in code.

### 2.2 Connection providers

- **`oauth2`** (platform): the authorization code flow with PKCE, the
  gateway as the client. Endpoints come from the connector's issuer
  metadata (RFC 8414, else OIDC discovery) or its settings. The redirect
  goes to the gateway (`/v1/connections/oauth/callback`), with `state`
  bound to the member and the connector, RFC 9207 `iss` checked, and the
  scopes the member picked. Tokens are refreshed before expiry, rotating
  the refresh token.
- **`api-key`** (platform): the member types the key once in the connect
  dialog. The connector says how a request carries it (`bearer`,
  `header:<name>`, `query:<name>`, `basic:<user>`). The proxy builds the
  header itself, so encoding is never the agent's problem.
- **A provider component** (`bnbloc.connection-provider.package/v1`,
  runtime `endpoint`, one sealed credential like every endpoint component)
  for everything else. The organization's SSO, where a bot asks the member
  to consent to each system's app, is one. The contract is three signed
  calls from the gateway:

  | Call | Answer |
  |---|---|
  | `POST /v1/connect` `{member, connector, settings}` | `{url}` the member opens, or `{card: true}` (the component reached the member itself), or a refusal with a reason |
  | `POST /v1/authorize` `{member, connector, settings, grant}` | `{headers, cookies, expiresAtMs, grant?}`: what one request carries until `expiresAtMs`, and an updated grant to store |
  | `POST /v1/revoke` `{member, connector, grant}` | 2xx |

  A consent the component receives (a token its bot was sent) reaches the
  gateway through a signed `POST /v1/connections/grants`. The component
  stores nothing: the **grant** is the platform's, sealed, handed to the
  component only for the call that needs it.

### 2.3 The connection store

- One connection per (member, connector, organization): its state (`none`,
  `awaiting`, `connected`, `expiring`, `expired`, `refused`), the scopes
  granted, whether **changes** are allowed, when it was connected and last
  used. Its grant (a refresh token, an API key, a provider's grant) is
  sealed with the platform key (`internal/sealbox`). Derived request
  credentials (an access token, a session cookie) are held in memory until
  they expire, never stored.
- Disconnecting deletes the grant and asks the provider to revoke it. An
  organization admin can disconnect a connector for everyone. A member
  removed from the organization (org sync deprovisioning) loses every
  connection.

### 2.4 The proxy

- `POST /v1/connections/{connector}/request`
  `{method, path, query, headers, body}` answers
  `{status, headers, body, truncated}`. Only the bridge calls it, for the
  turn it drives (§2.5), with that job's grant (`?room=&job=`), so it acts
  as **the requester of the turn being driven** and only while the job
  runs.
- Whose connection: the requester's, and only on a drive where the
  requester's data stays with the requester. That means **their own agent**
  (the agent's owner is the requester) or a schedule they created. On a
  borrowed drive (Bob asks Alice's agent) the call is refused with a line
  saying why: Bob's systems' data would otherwise land in Alice's agent's
  workspace and transcripts on Alice's computer. A platform agent counts as
  borrowed.
- Checks, in order:
  1. the connector is in effect for the organization;
  2. the requester's connection is connected (else `not_connected`, below);
  3. the request's host is the connector's (paths only; no host, scheme or
     port override; redirects followed only within its hosts);
  4. the policy class is read, or write with changes allowed (else
     `needs_changes`, below);
  5. a rate per member and connector.

  The proxy then adds the credential (the provider's headers and cookies),
  strips any credential the agent sent, and drops `Set-Cookie` and the
  provider's auth headers from the answer.
- Bounds: request body 1 MiB, answer body 4 MiB (the bridge's proxy
  answers an over-cap body with a 502 naming the cap).
- **Every call is recorded**: who, which agent, room, job, connector,
  method, path (query values left out), policy class, status, bytes. The
  member sees their own (Connections → activity) and an organization admin
  sees the organization's (the audit log).

### 2.5 The agent's side: nothing new, its usual HTTP just works

The agent gets no tool and no rule. It calls a connected system the way it
calls anything: `curl`, a script's HTTP client, a CLI that takes an API
base. The platform authenticates the call on its way out, where the agent
cannot see it.

- **The bridge's forwarding proxy.** Each bridge runs one HTTP proxy on
  loopback. A turn of the member's own drive with connections in effect
  gets `HTTPS_PROXY` and `HTTP_PROXY` in its commands' environment (host
  commands, and the space's for a sandboxed turn), carrying a bearer bound
  to that turn's job, as the room tools bearer is. A `CONNECT` to a host of
  a connector in effect for the requester (connected or not, so a call to
  one not yet connected answers `not_connected` and the person is asked)
  is answered with a certificate from the
  bridge's own local CA (generated per member, its key 0600 in the member
  directory, never leaving the computer). The proxy reads the request and
  sends it as `POST /v1/connections/{connector}/request` through the
  bridge's own client, so the gateway applies the credential, the policy
  and the audit (§2.4). It then answers with the system's response. Every
  other `CONNECT` is tunnelled untouched and never decrypted.
- **Trusting the local CA, for the agent's commands only.** Its
  certificate is added to the bundle those commands read (`SSL_CERT_FILE`,
  `NODE_EXTRA_CA_CERTS`, `REQUESTS_CA_BUNDLE`, `GIT_SSL_CAINFO`). The
  member's own system trust store is never changed.
- **Sandboxed turns.** The Sandbox Protocol gains `hostProxy`: a space may
  reach one host loopback port the bridge names (the proxy), and nothing
  else on the host. The reference implementation maps it through the
  runtime's host alias, enforced outside the guest.
- **Turn-bound and own drives only.** The bearer works while its job runs
  and is revoked with the turn. A borrowed or platform drive gets no proxy
  environment, so its calls go out unauthenticated, as without connections.
- **What the agent knows** is data, not a rule. The session's room facts
  (`.room/README.md`) list the connectors in effect for the requester, one
  line each with its state: "calendar (www.googleapis.com): as maya,
  read; changes are asked first", or "wiki (wiki.acme.example): not
  connected". The connector's skill explains its API with ordinary `curl`
  examples.
- **A room can say what it uses.** A room's setup may name connectors
  (`requires.connectors`, beside `requires.skills`, in the room and in a
  room package). Each member who has not connected one sees it on the room's
  start card ("this room uses your calendar: Connect"). Installing a room
  package asks the same. Nothing connects for them, and nothing is shared:
  each member's agent uses that member's own connection.
- **Refusals are ordinary HTTP.** `not_connected` and `needs_changes`
  answer `403` with a JSON body naming the reason and saying that the
  person was asked in the room (the card, §2.6). A held write answers
  `202` with `{"held": true, "card": …}`. An agent reads these like any
  API error. It does not retry or ask for a token, since the body tells it
  the person decides.
- A CLI that refuses to run before its own login (it checks for a token
  locally) is given a placeholder through the connector's `cliEnv`
  (`{"GH_TOKEN": "connected"}`). The proxy replaces every credential on a
  connector host, so the placeholder is never sent anywhere.

### 2.6 AI first

- **The agent finds out and the member decides.** A call answered
  `not_connected` or `needs_changes` posts a card to the requester in the
  room (shown to them only, like a publish proposal). It names the system,
  what the agent tried ("read page 123", "create an issue") and the reach
  the member grants ("read as you", "make changes as you"). Connect or
  Allow runs the provider's flow; the card then offers **Ask again** (the
  same request to the same agent). Nothing is connected or widened without
  that click.
- **Writes are proposed.** A write the member did not mark "don't ask for
  this connector" is held: the card shows the method, the path and the
  body's summary, and the call runs on Allow. A schedule's run cannot ask,
  so its writes need changes allowed in advance; otherwise it ends with the
  card left for the member.
- **The agent drafts connectors.** Given a system's API description or
  docs, an agent drafts a connector package and its skill pack (the
  `components` room tool, kind `connector`, `try` validating it). An admin
  saves and publishes it: the agent proposes, a person publishes.
- **Explained activity.** Connections → activity has an assist that
  summarizes what agents did with a connection over a period (the assist
  seam, `connection-activity`), so a member can read "this week your agent
  read 40 wiki pages and created 2 issues" instead of a call list.
- With the model off, everything above still works by hand: connect from
  Components, allow changes in the connection's settings, read the raw
  activity.

### 2.7 One owner of a member's grants

A member's grants to other systems live in one place. An endpoint MCP
tool's OAuth (`mcp.http.auth: oauth`, today on the desktop) becomes a
connection to the tool's server. The tool names a connector, and the
bridge gets the access token for the member's own drives from the gateway.
The desktop's `mcp-sign-ins/` store and its flow go (pre-launch, no
migration; members sign in once more). Member-scoped secret settings stay
settings: they configure a tool, they are not a grant to a system.

## 3. Design review

| Lens | Answer |
|---|---|
| Actors | The member connects and decides changes; an organization admin enables connectors and can disconnect them; the platform admin publishes connectors and providers and sets their client values. Agents act only for the requester of their own drives; a borrowed or platform drive never uses the requester's connections (§2.4). |
| Time and lifecycle | A grant lives until disconnected, revoked by the provider, or the member deprovisioned. Derived credentials live in memory until expiry. A consent wait has no fixed bound; a turn does not pause for it (it ends with the card). A refresh refused mid-run ends that call with `not_connected` and a card. |
| Scale and cost | One store row per member and connector; an in-memory cache per live credential; a call costs one upstream request. A per-member, per-connector rate and the body caps bound a runaway agent. |
| Failure and recovery | Provider down: the call answers `provider_unavailable` (retryable by the agent); a grant the provider refuses: `expired`, with a reconnect card. An upstream 401, or an answer matching the connector's `expiredWhen`, triggers one re-authorize and one retry, then `expired`. Nothing fails silently: each refusal names the system and the fix. |
| Boundaries and privacy | Grants sealed with the platform key; never in an answer, a log, a workspace, a sandbox or a room. The proxy reaches only a connector's hosts. Answers flow to the requester's own agent only. Audit rows hold paths, not bodies or query values. |
| Abuse and untrusted input | A page the agent read cannot widen its reach: writes need the member's allow, per call by default. Requests cannot pick a host, scheme or port; the proxy strips agent-sent credentials; redirects outside the hosts stop. A system's answer is data the agent's command printed, like any `curl` output: what limits a hostile page is that its suggestions cannot change anything without the member's click. |
| One owner | The proxy and its checks are one route; the policy classes are one function in `core/pkgmeta`, used by the validator and the proxy; the grant store is one package; providers own only their protocol. §2.7 removes the second store of member grants. |
| Defaults and fallbacks | A new connection is read-only; changes are asked per call; no connector is in effect until an organization admin enables it; without a connector's client values it shows "needs setup" to admins and is hidden from members. |
| External contracts | OAuth 2.0 with PKCE, RFC 8414/OIDC discovery, RFC 9207, refresh rotation; each provider component's three calls with a conformance probe; each system's API as the connector's skill describes it. A provider's refusal ("this platform may not reach the host yet") is kept distinct from "you lack access". |
| Load on users and agents | One card when a system is not connected or a change is proposed; one dialog to connect. No tool and no rule for agents: their ordinary HTTP is authenticated on the way out; one fact line per connection in `.room/README.md`; per-system knowledge in skill packs, attached only for the requester's connected connectors. |
| Release and compatibility | New kinds, routes and tables (additive). §2.7 replaces the desktop's MCP sign-ins without migration (pre-launch). The bridge's proxy ships with the desktop; `hostProxy` is an optional capability (without it a sandboxed turn's calls are not authenticated and the facts say so); connector samples release on their own. |
| Observability | Every call is an audit row and a structured log event (connector, class, status, latency); providers' failures are logged with their reason; the member's activity view and its summary. |
| AI capability | The agent notices a missing connection or a needed change and proposes it; the agent drafts connectors; an assist explains activity (§2.6). No model sits in the request path: read or write is the connector's declared policy, and a change is the person's click, never a model's call. |
| Component or platform | Platform: the store, the proxy, the policy enforcement, consent bookkeeping, the cards, the audit (only the platform can hold members' secrets and enforce a boundary between them). Components: provider protocols other than OAuth 2.0 and API keys, every connector and its skill. The forwarding proxy is the bridge's (it holds the turn's grant, as the room tools do). Built-in providers are the two open standards, not a vendor. |

**Pre-mortem.**
1. *A fetched page talks the agent into deleting things.* Changes are
   proposed per call by default and run only on the member's click; the
   card shows the exact method, path and body summary.
2. *The proxy becomes an internal scanner.* A request names a path, never
   a host; the connector's hosts are exact names an admin published; a
   redirect elsewhere stops; the resolved address must not be loopback,
   link-local or metadata.
3. *Someone borrows an agent to act as its owner.* A connection is the
   requester's, never the agent owner's; and on a borrowed drive the
   requester's connections are refused too (§2.4).
4. *The store leaks.* Grants are sealed with a key the store does not
   hold; derived credentials are never written; disconnect and deprovision
   delete; a key rotation re-seals.
5. *The SSO path fails as "you have no access".* A provider maps its
   upstream reasons: the platform's bot not yet allowed to reach a host is
   an operator item shown to admins, not a member error.
6. *A schedule's write waits forever.* A schedule cannot be asked; without
   changes allowed in advance the run ends with the card and says so.
7. *The local CA is turned against the member.* Only the agent's command
   environment trusts it; the proxy mints leaf certificates only for the
   requester's connector hosts, for the turn; its key never leaves the
   member directory (0600), and a new one is made when the member signs
   out.
8. *The agent stops using the system because it does not know it can.*
   The facts line names each connection and its reach; the agent task
   suite checks that an agent asked for a wiki page reads it through a
   plain `curl` with no tool and no prompt rule.

## 4. Build order (slices of §2)

1. **The platform and the open standards.** The connector kind and
   validator (policy classes), the store, the proxy and its checks, the
   audit, `GET /v1/my/connections` (job-grant admitted, for `list`),
   Connections in Components (connect, allow changes, disconnect,
   activity), the `oauth2` and `api-key` providers. The bridge's forwarding
   proxy, its local CA and the facts line; the Sandbox Protocol's
   `hostProxy` with its conformance step and the reference implementation. The cards for not connected, needs changes and held
   writes, with Ask again, plus the agent's connector drafting
   (`components` kind `connector`) and the activity assist. First
   connectors as samples in their own repository: a public code host (API
   key and OAuth) and a calendar (OAuth).
2. **Provider components.** The `bnbloc.connection-provider.package/v1`
   contract with its probe, then the organization's SSO app-consent
   provider (internal; operator provisioning per system starts in
   parallel) and the internal wiki and issue tracker connectors.
3. **One owner of grants** (§2.7): endpoint MCP tools' OAuth through
   connections; the desktop's MCP sign-ins go.

Not in this round: non-HTTP protocols (a database wire protocol, SSH);
a connector for one waits for a system that has no HTTP API.

## 5. Open questions

- The organization's SSO: the consent bot's type and permissions, the
  per-system gateway allowlist for the platform's bot, the token's
  lifetime and refresh (operator items, recorded in the internal addendum).
- Which external OAuth clients the organization will register, and which
  sensitive scopes need provider review.
