# Service components

A **service** (`bnbloc.service.package/v1`, contract
`bnbloc.service.contract/v1`) is an endpoint component beside the gateway
that acts for members and for the turns their agents run, through
primitives it declares in its package's `capabilities`. The platform
attests who a call is for and carries it; what the call may do is the
service's decision. The first service is the connections component
(docs/prd/2026-10-22-connections.md); nothing here names it.

The contract's shapes and rules are the public `component/` package
(`service.go`, `signature.go`): a service outside this repository builds
against them.

## The primitives

| Capability | What the service gets | Where |
|---|---|---|
| `turnProxy` | A bridge's calls for a job it drives, the turn attested | `GET\|POST /v1/turn-proxy/{service}/{path}?room=&job=` → service `/turn/{path}` |
| `egress` (needs `turnProxy`) | The HTTPS the turn's commands send to the hosts it claims | `/turn/egress/hosts`, `/turn/egress/request` |
| `roomFacts` (needs `turnProxy`) | A line in the session guidance and files under `.room/{service}/` | `/turn/facts` |
| `pages` | Pages a member opens, the member attested | `GET /pages`; `/v1/component-pages/{ticket}/{path}` → service `/pages/{page}/{path}` |
| `callbacks` | A stable address another system redirects a browser to | `/v1/component-callbacks/{service}/{path}` → service `/callbacks/{path}` |
| `store` | Values sealed with the platform key | `GET\|PUT\|DELETE /v1/component-store/{scope}/{key}` |
| `assist` | Its own AI tasks run on the organization's assist model | `POST /v1/component-assist/{task}` |

A service gets none it does not declare (`pkgmeta.validateServicePackage`;
`servicecomp.Declares` at every route). Each primitive reaches only its own
prefix of the service: a turn's call never reaches a page, a callback never
reaches a turn's route.

A service is the organization's infrastructure: it is distributed to an
organization by its rollout (`extpipe.TenantDistribution`), with no
per-member install. Its settings are asked like any endpoint component's
(`GET /v1/component-settings`).

## Signing and identity

Every call in either direction is signed with the service's credential
(`component.Sign`) over `component.SignedRequest`: the method, the path
with its query, the identity header's value and the body. No part of one
call can be replayed in another.

| Header | On | Carries |
|---|---|---|
| `X-Bnbloc-Turn` | a turn call | `component.Turn`: org, room, channel, job, requester, agent owner, drive (`own`, `borrowed`, `platform`), the public origin |
| `X-Bnbloc-Member` | a page call | `component.Member`: subject, org, role and whether they manage the organization, page, the page's own address, the public origin |
| `X-Bnbloc-Callback` | a callback | the public origin and the callback's address |
| `X-Bnbloc-Audit` | the service's answer to any forwarded call | one `component.Audit` row the gateway records in the organization's audit log, attributed to the service, for the attested person |
| `X-Bnbloc-Component` | a service's own call (the store, its AI tasks) | `service/<name>` |

Identities are base64url JSON (`component.EncodeHeader`).

- **Turn**: admitted by `jobGrant` (services/gateway/content_requester.go):
  the job is live and leased by the calling bridge, and its requester may
  still post in the room. `drive` is `own` when the requester owns the
  agent, `platform` for a platform agent, `borrowed` otherwise. The
  service decides what a drive may do.
- **Member**: `POST /v1/my/component-pages/{service}/{page}` signs a ticket
  (`auth.KindComponentPage`, one hour) naming the member, their
  organization and role, the service and the page. The page's address
  carries it; every call under it is the member's. An admin page
  (`audience: admin`) is listed only to an organization admin; the service
  still checks `Member.Admin`.

Answers pass on with their status and a few headers
(`servicecomp.passedHeaders`); a cookie never does. An HTML answer is
sandboxed (`Content-Security-Policy: sandbox allow-scripts allow-forms
allow-modals allow-popups allow-popups-to-escape-sandbox`, an opaque
origin) and never cached. Redirects are passed to the browser, never
followed by the gateway. Bodies are at most `servicecomp.MaxBody`.

## Egress

The bridge's room tools server is also the session's HTTPS proxy
(`services/bridge/roomtools/egress.go`).

- A session on a computer whose organization has an `egress` service gets
  `HTTPS_PROXY` with its room tools bearer, `NO_PROXY` for loopback, and
  the local CA in `NODE_EXTRA_CA_CERTS`, and a bundle of the computer's
  roots (on macOS its system and administrator keychains, so an
  organization's inspecting proxy stays trusted) with it in `SSL_CERT_FILE`, `REQUESTS_CA_BUNDLE`, `CURL_CA_BUNDLE`,
  `GIT_SSL_CAINFO` (`Server.EgressEnv`). The services' `env` (placeholders a
  CLI needs before it runs) joins it.
- At every turn the bridge asks each egress service which hosts it handles
  for the job (`EgressHosts`: exact names, `component.ValidEgressHost`). A
  host two services claim stays with the first by name. A service that
  cannot be read keeps the hosts it claimed before.
- A `CONNECT` to a claimed host on 443 is answered with a leaf certificate
  from the **local CA** and each request is sent to the service as an
  `EgressRequest` through the turn proxy for the bound job; the service's
  `EgressResponse` is the command's answer. Between turns a claimed host
  answers 503 and nothing is sent. Every other `CONNECT` is tunnelled,
  never decrypted.
- **Sent from the member's computer**: a service may answer `Send`
  instead (a system its own network does not reach, an internal one the
  member's computer does): the bridge removes the headers `Send` names,
  sets its credentials for this one request and sends it itself, then asks
  the service again with a `Report` (status, Location, the body's first
  64 KiB). The service answers `Accept` (the system's answer is the
  command's), another `Send` (fresh credentials, at most two sends), or a
  refusal of its own with its card. Only that request's credentials reach
  the bridge, in its memory; never the agent.
- **The local CA** (`roomtools/egress_ca.go`): one per member directory
  (`egress-ca.pem`, its key `egress-ca.key` 0600), never sent anywhere,
  replaced 30 days before its end. Leaves last a day. Only the turn's
  commands trust it; the system's trust store is never touched. A tool
  that trusts only the system's roots (Go programs on macOS) fails its TLS
  check against a claimed host: its call never goes out unauthenticated.
- **Cards**: (the room's events carry a card to every member; the clients
  show it only to the requester, as a publish proposal) an `EgressResponse.Card` (`component.CheckCard`: plain text,
  at most three actions, `open` a page or `ask-again`) is posted by the
  bridge as a `component-card` notice under the job, shown only to the
  requester, once per job, service and title. The web renders it
  (`ComponentCardView`): Open opens the page as the member; Ask again puts
  the card in the composer.

### Sandboxed commands

A sandboxed session's commands run in the guest, where the host's loopback
is not reachable. With the Sandbox Protocol's `hostProxy`:

- The desktop picks a host port per sandbox launch, creates its machines
  with `hostPorts: [port]` and hands the bridge
  `BNBLOC_SANDBOX_HOST_PROXY=<hostAlias>:<port>`. The machine reaches that
  host port, and nothing else of the host, at `Discovery.HostAlias`
  (bnbloc-sandboxd: the runtime's host gateway, `allow_host` on that TCP
  port only), whatever its egress mode.
- The bridge's proxy also listens on `127.0.0.1:<port>` (proxy only).
- A sandboxed space gets the proxy in its default environment
  (`SpaceRequest.Env`, re-ensured every turn) with an **egress alias**: a
  bearer derived from the workspace, bound to the session when it is
  created; the CA files are copied into the workspace
  (`.room/egress/`), which the guest sees at the same path. The agent's own
  process on the host gets none of it.
- A tunnel for a sandboxed command reaches only public addresses (resolved
  and dialled by address), and a private address only for the member's
  private hosts (`BNBLOC_SANDBOX_PRIVATE_HOSTS`); never loopback,
  link-local or unspecified ones. Private hosts go through the proxy like
  any other (they are not in `NO_PROXY`), so a connector's host that is
  also private is still served by its service.
- A borrowed or platform drive's space gets the proxy too: each service
  decides by the attested drive (the connections component serves one only
  as a digital worker, never in the owner's or the requester's name).

## Room facts

At a session's creation the bridge asks each `roomFacts` service for the
job's `Facts`; the line (one line, at most 200 characters) joins the
records' list of the session guidance as `- {service}/: {line}` (an empty
line adds nothing; an unreadable service says it could not be read). At
every turn the files (at most 32, 64 KiB in all) are written under
`.room/{service}/`. A service named like one of the records' own
(`roomlog.ServiceDir`) gets no directory. The line rides the session
prefix; the files are read by the agent when it needs them.

## Store

A value is at most 64 KiB, sealed with the platform key bound to its
service, scope and key (`component.value/<service>/<scope>/<key>`), so a
row read by another service or moved to another key does not open.
Scopes: `members/<subject>`, `orgs/<org>`, `shared`. `GET` on a scope with
no key lists its keys. A service's values go with its uninstall; a
member's, in every service, with their deprovisioning
(`POST /v1/platform/org-sync/deprovision`). Table: `component_values`.

## AI tasks

docs/prd/2026-10-23-connections-ai.md §2. A service runs one of its own
JSON assist tasks for an organization with `component.ServiceAssistRequest`
`{org, member?, text}` (text at most 16,000 characters) and gets
`component.ServiceAssistReply` `{fields}`, the task's declared fields in their
shapes (`assist.Fields`). `component.ServiceClient.Assist` is the client.

- **Its own tasks only**: named `<service>-<task>`
  (`component.ServiceAssistTask`), never a name of the platform's
  (`wire.AssistContracts`): 403.
- **The organization's version, else the platform's**
  (`assist.ServiceTask`), never a member's own or a shared copy; a JSON
  task (a text task: 400). Not installed: 404. Tasks are content a
  service keeps in its own repository and publishes at platform scope.
- **Admitted** like any service call (signature, distributed to the org,
  declares `assist`), then the organization's assist model, else the
  platform's; assists off or no model: 409. 20 runs a minute per service
  and organization (429).
- **No context**: the model gets the task's instructions and the text,
  fenced as data (`assist.Messages`); the platform reads nothing for the
  service.
- **Audit**: an `assist` row, actor `service/<name>`, the task and its
  version, `for <member>` when the request names one (the service's
  word), the model and tokens, never the text.

## Code

- `component/service.go`, `component/signature.go`: the contract.
- `core/pkgmeta`: the kind, its capabilities and their needs.
- `internal/servicecomp`: the installer and `Forward`.
- `services/gateway/service_components.go`, `component_store.go`,
  `component_assist.go`: the routes.
- `services/bridge/turn_services.go`: the list, the facts, the egress hosts.
- `services/bridge/roomtools/egress.go`, `egress_ca.go`: the proxy and CA.
- `services/bridge/roomtools_contrib.go`: a session's egress environment,
  a sandboxed space's.
- `sandbox/protocol` `hostProxy`, `sandbox/conformance` step `hostProxy`.
