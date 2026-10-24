# Deliverable backends and a complete showcase (2026-10-24)

Status: **proposed**. The product owner's decisions of 2026-10-24 are in §1.
Nothing is built. Builds on `docs/prd/2026-10-12-interactive-apps.md` (the app
base), `docs/prd/2026-10-13-app-gallery.md` and
`docs/prd/2026-10-13-works-gallery-and-make-similar.md` (the gallery),
`docs/architecture/service-components.md` (signed calls, attested identity),
`docs/prd/2026-10-22-connections.md` and
`docs/architecture/sandbox-protocol.md`.

Owner: the room service (backend registry, usage), the gateway (the app's
backend path, policy admission), the bridge (project snapshots, the member's
channel), `internal/contentpkg` and `internal/tarbundle` (works, media), the
public contract packages (`host/protocol`, the host package kind in
`core/pkgmeta`), and the host **components**, which live in their own
repositories.

## 0. Summary

An agent can already deliver a page with its own data. This record lets it
deliver a **project**: a whole folder, with a backend if the folder has one,
and lets other people see all of it and run it.

Two halves:

1. **Showcase.** What a member sees in the gallery is the whole work: cover,
   screenshots, a recorded walkthrough, the full project (file tree, README,
   what it needs, whether it was seen to start), and Try demo where the work
   can run without a backend.
2. **Backends.** A project with a backend runs somewhere, as a **backend of
   an app**. Where it runs is decided by installable **host components**
   behind one contract, `bnbloc.host.contract/v1`. The platform owns what a
   host cannot be trusted with: identity of the caller, policy, quota,
   metering, audit, secrets and the kill switch. The member's own sandbox is
   one host; a cloud one is another; an organization may install several or
   write its own.

## 1. Decisions (product owner, 2026-10-24)

- Design from the end state; milestones are slices of it, never stopgaps.
- A host is a **component** behind a protocol and a contract. The platform
  names no provider, no cloud and no runtime.
- Backends may run in the member's own sandbox and, through components, in a
  cloud. Both are hosts of one contract.
- A live Try demo of a backend runs in **the viewer's own sandbox**, through
  the same host contract, once the sandbox host exists as a component (§10).
  A cloud demo stays reserved.
- Recorded walkthroughs and screenshots are supported from the start.
- Who may host in the cloud and who pays: proposed in §8, for the owner to
  confirm.

## 2. Why

Checked against the code on 2026-10-24:

| Finding | Where |
|---|---|
| A static site is already a many-file deliverable (2000 files, 64 MiB). Anything else is published one file at a time, 24 files per pass. | `services/bridge/sites.go` (`tarbundle.SiteLimits`), `services/bridge/artifacts.go` (`maxPublishFiles`) |
| A work that is not a site holds exactly one file, a cover and the recipe's references. A project folder cannot be shared whole. | `internal/contentpkg/work.go` (`validateWorkFiles`) |
| Dependency directories and every dot path are never published; a `.env.example` or a `vendor/` tree is lost with them. | `services/bridge/artifacts.go` (`dependencyDirs`, `dependencyPath`) |
| A site over its caps, a source root with no build, an invalid path: **skipped with one log line**. Nobody in the room is told, and the agent says it is done. | `services/bridge/sites.go` (`publishSite`) |
| A cover is one image of at most 1 MiB. There is no place for screenshots or a recording. | `internal/tarbundle/cover.go` |
| Try demo runs the page with the SDK in demo mode: an in-memory data space. It cannot run a backend. | `services/gateway/app_demo.go` |
| A sandbox space is deleted after **every** sandboxed turn so no guest process outlives its turn. A server an agent starts dies with the turn. | `services/bridge/sandbox_fence.go` (`quiesce`) |
| The protocol's `ports` capability exists, and the bridge never consumes it (no reference to `Ports` outside the protocol). Reaching a guest port is not built. | `sandbox/protocol/types.go`, `services/bridge` |
| The sandbox is a desktop sidecar. The default mode is `borrowed`: an own agent runs on the host, with no VM. A web-only member has no sandbox. | `services/bridge/sandbox.go` (`ParseSandboxMode`) |
| Deprovisioning removes tenant memberships and sign-ins and keeps room memberships as history. Nothing stops what a departed member started. | `internal/orgsync/orgsync.go` (`Deprovision`) |

Two consequences shape the design:

- Running a backend is not "reuse the sandbox's `ports`". It needs a new
  lifecycle (a service that outlives a turn), a new ingress, and a place to
  run that exists for members without a desktop sandbox.
- The sandbox protocol is the wrong contract for hosting. It describes a
  machine with exec, spaces and shared mounts, for running an agent's
  commands. A hosting provider (a platform on a cluster, a container
  service, an internal platform) offers workloads, not machines to exec in.
  Forcing those providers to implement spaces and shares would bend both
  contracts. Hosting gets its own, small contract (§6). A provider built on
  a sandbox endpoint remains possible as an adapter.

## 3. What it is (the end state)

### 3.1 The parts

| Part | Where | Owns |
|---|---|---|
| **Media and project works** | platform (works, `tarbundle`) | what a work may hold and show |
| **Run spec** | the project's own file, validated by the platform | what a project says it needs to run |
| **App backend registry and ingress** | platform (room service, gateway) | which backend belongs to which app, the caller's attested identity, policy and quota admission, usage, audit |
| **Host contract** `bnbloc.host.contract/v1` | public package `host/protocol`, package kind `bnbloc.host.package/v1` | the one interface every host implements; a conformance suite |
| **Host components** | their own repositories | running the workload: the member's sandbox, a cluster, a hosted service |
| **Samples** | their own repositories | a reference host and example projects, built only with the public capabilities |

### 3.1a Placement: content, component or platform

The core rule (AGENTS.md "the platform provides only what it must"), answered
for every part. A row marked platform names its allowed reason.

| Part | Placed as | Why | What an organization would swap |
|---|---|---|---|
| Recording and screenshots made by the agent; the run-file and listing guidance | **Content**: the built-in app skill pack's update, then a `backend-apps` skill pack and a "Backend builder" expert in a content repository | people and agents already use skills; nothing here needs code | edit or replace the pack and the expert |
| Drafting and reviewing a run spec, drafting a listing | **Content**: assist packages in the content repository | the assist seam exists; tasks are content | their own prompts, per organization |
| Example projects (a form with a backend, a bot) | **Content / sample**: their own repository | a use case is a sample, never platform | copy and change |
| Media typing and serving (bytes sniffed, size caps, `nosniff`, sandbox CSP) | **Platform** | isolation: untrusted bytes must never be served on a guess | nothing; the caps are wire constants |
| The project bundle (a deterministic snapshot of a folder) and its work type | **Platform** | the works kind and its blob store are the platform's; a contract, not a policy | nothing |
| The project's file-tree viewer | **Component**: a previewer (`bnbloc.previewer.package/v1`) matching the project bundle type | viewers are components; the platform serves the bundle and the archive | another viewer |
| The publish gate (ask checks, fail closed, a person's acknowledgement, which paths) | **Platform** | consent and the events only the platform sees; a gate is a mechanism | policy: whether checks are required |
| The checkers (credentials, danger, licences, banned words) | **Component**: service components declaring `publishCheck` | the formats and rules change without a release and differ by organization | install, add or write another |
| Notices for a publish that did not happen | **Platform** | events only the platform sees | nothing |
| The run spec's shape | **Platform** (a contract) | one validated shape for every host | nothing; its fields' meaning is the host's |
| Backend registry, ingress, caller identity, sealed environment, policy and quota admission, metering, audit, kill switch | **Platform** | identity and the attested caller, isolation, budgets and quotas, audit | policy values (who, how much) |
| Page-and-backend release pairing | **Platform** | only the platform decides which version a link serves | when to release |
| Owner's open and call counts | **Platform** | events only the gateway sees | nothing |
| The member channel (a member's sidecar reachable from the gateway) | **Platform** | the gateway cannot dial a laptop; a generic primitive | its transport is decided in slice 6 |
| Host contract, conformance suite, package kind | **Platform** | the contract itself | nothing; hosts implement it |
| Every host: the member's sandbox, a cluster, a hosted service | **Component** (each its own repository) | the provider is the organization's choice | choose, install, write one |
| The reference host | **Sample** component | proves the contract in CI | replace |
| Live demo on the viewer's sandbox | the sandbox host component's `ephemeral` capability; the demo ticket routing is **platform** | running code is a host's job; routing a demo to the viewer's own host is the attested proxy | which host serves demos |

### 3.2 What a member does

1. Asks an agent in a room for something with a backend. The agent writes the
   project, runs it in its own sandbox to test it, and records what it did
   (§4.3). The deliverable card shows a project: a file tree, the README,
   screenshots and the recording, and what it needs.
2. Presses **Run**. The card offers the hosts the organization's policy
   allows: *My sandbox*, and any cloud host. The first run asks for the
   values of the environment variables the spec names, and shows what the
   spec asks to reach (§4.2). The member decides.
3. The app's page, and only the app's page and its owners, call the backend.
   The card shows the state: starting, running, idle, failed; where it runs;
   its logs.
4. Publishes the work to the gallery. Others see all of it (§3.3). *Use this*
   copies the files, never an instance, data or secrets; the member then runs
   their own copy on their own host and quota.

### 3.3 What a viewer sees in the gallery

Cover, up to eight screenshots, one recording, the README, the file tree with
each file viewable (a previewer component) and the whole project downloadable
as one archive, the run
spec as "What this needs" (hosts it reaches, connections it asks for,
variables, storage, size), the record that it was seen to start (§4.4), the
files left out and why, and Try demo where §10 allows it.

## 4. The showcase

### 4.1 Media

- A work bundle may hold a `media/` folder: screenshots
  (`media/*.png|webp|jpg|jpeg`) and one recording (`media/demo.webm` or
  `media/demo.mp4`). The cover stays `cover.png` as today.
- Every file is **typed by its bytes**, never by its name (a PNG, WebP, JPEG,
  WebM or MP4 header; an SVG is never media), with size caps held as wire
  constants beside `MaxWorkCoverBytes` (proposed: 2 MiB per screenshot, at
  most eight, 32 MiB for the recording; to be confirmed against real
  recordings when built). Order is by file name.
- Served by the same route family as the cover: `nosniff`, a sandbox CSP, no
  inline HTML. The detail page shows a carousel and a native video player.
- **Who makes them.** The agent, in its own sandbox, in the self-test it
  already runs (`docs/prd/2026-10-13-interactive-apps-self-test.md`): the
  headless browser that loads the page also takes the screenshots and records
  one pass through the main action. This is **content**: a change to the
  existing `interactive-apps` skill pack (a version bump and its golden and
  budget tests), not a new tool, rule or platform code. That built-in pack is
  itself a candidate to move to the content repository; this record adds
  nothing else to the built-ins. The platform never synthesizes media. The
  same change places the SDK's types (`sdk.d.ts`, which the gateway already
  serves at `~app/sdk.d.ts`) beside the demo-mode `sdk.js` in the workspace's
  room records, so the agent's self-test and editor see a wrong SDK call
  before the page does.
- A work without media still publishes. The card then draws its generated
  cover and the detail page says there is no recording; nothing is invented.

### 4.2 Projects

- **A project is a folder published whole.** New work type `project`
  (`wire.WorkTypes`), new artifact kind. Its snapshot is the folder's files as
  a deterministic bundle, so an unchanged project re-publishes as a no-op,
  like a site.
- **What goes in is decided by the project's own files, not by a list in the
  platform.** Everything the folder lists except: the dependency directories
  the bridge already never publishes (`dependencyDirs`, **not extended** by
  this record), the paths the project's own `.gitignore` excludes (positive
  evidence, read once, by one owner), the run spec's `exclude`, protected
  workspace paths (`core.ProtectedWorkspacePath`), and every hidden path unless
  the run spec's `include` names it (so a `.env.example` ships when its author
  says so and a `.env` never does by default). Anything a required publish
  check blocks (§4.5) is out. **Dependencies are not shipped.** A project ships
  its manifest and lockfile; the host installs. The platform names no ecosystem:
  which files mean what is the project's and the host's business.
- **Left out is always said.** The card and the detail page list the files
  left out and the reason (dependency directory, ignored, hidden, excluded,
  blocked by a check, over a cap). A rule may only skip what it can name as
  noise.
- **A person or the agent publishes a folder as a project; the platform does
  not guess.** A folder holding the run spec is a project by the platform's
  own contract (`bnbloc.run.json`); any other folder is one when a person, or
  the agent at their request, publishes it as one. Spotting a candidate in a
  workspace is content: the skill pack and the assist of §11 propose, and a
  person decides. There is no marker list of package managers or languages in
  the platform. A folder with a non-build `index.html` stays a site; a project
  that contains a site is published **beside** it, not instead: the site is what
  members open, the project is what is shared and run.
- **Caps** follow the promise one layer up: a project may be as large as a
  work may be (`WorkLimits`, raised for projects by a project cap held beside
  the site caps), and a project over a cap is not published silently (§4.6).
- **The run spec.** `bnbloc.run.json` at the project root, optional, validated
  by the platform for shape and caps only:

  | Field | Meaning |
  |---|---|
  | `profile` or `image` | the runtime the host offers (names from its discovery document), or an OCI reference when the host advertises `ociImage` |
  | `install`, `start` | commands; their meaning is the host's |
  | `port`, `health.path` | where the backend listens, how to tell it is up |
  | `env[]` | `{name, description, required, secret}`: names only, never values |
  | `volumes[]` | `{path, purpose}`: paths that must survive a restart |
  | `egress[]` | `{host, port, why}`: what the backend asks to reach |
  | `connections[]` | `{connector, why}`: what it asks to use (§5.5) |
  | `resources` | requested CPU, memory, disk; the host and policy decide |
  | `include`, `exclude` | path patterns: hidden files to ship, files to leave out (§4.2) |
  | `demo` | optional `{enabled, env}`: the author's opt-in to a live Try demo (§10) and the non-secret values it runs with |

  The platform names no language, framework or build system. `egress` and
  `connections` are **requests**, never grants.
- **No file, no problem.** Without a run spec, an assist (§11) drafts one from
  the files, the author edits it, and the draft is saved into the project as
  the file. A person decides.

### 4.3 The run record

When a project is run on any host and its health check answers, the room
service records `{specDigest, bundleDigest, host, at}` on that version. The
detail page says "Seen to start on <date> on <host kind>". It is a fact about
one run, not a safety claim, and it never carries logs or data.

### 4.4 What is not in a work

Data, instances, secrets, connection grants and logs are never part of a
work, as for apps today. `Use this` creates a copy with an empty data space.

### 4.5 Publish checks: a gate in the platform, the checking in components

A project and a site go outward through three paths: the gallery, a link
anyone can open, and an organization publication. Checking what leaves is not
the platform's business to implement (which credential formats exist, which
licences matter, which words an organization forbids all change without a
platform release and differ by organization), so the platform ships only the
**gate** and a contract:

- **The gate (platform).** Before any outward publish, the platform asks every
  check component the organization has installed and enabled, bounded and
  fail-closed, and applies what comes back: a `high` finding blocks the gallery
  and an anyone-link (in an organization-only share it warns); `medium` and
  `low` need the author's acknowledgement; **a check that cannot finish blocks
  with a retryable error and never publishes on silence.** The organization's
  policy says whether outward publishing requires at least one check component
  to be installed.
- **An acknowledgement is a person's act, never an agent's**: a room tool or
  the CLI an agent uses refuses to publish past a finding and shows the person
  what was found; no flag or `--yes` substitutes. The same applies to the run
  spec's requests (`egress`, `connections`, `env`): a publish to the gallery or
  an anyone-link shows them and needs a fresh acknowledgement whenever they
  changed since the last one the person gave.
- **The contract.** A **service component** (`bnbloc.service.package/v1`) that
  declares a `publishCheck` capability answers `POST /checks/publish` with the
  work's file list and, within a cap, the text of its files, and returns
  findings `{severity, file, line, kind, message}` and whether it saw
  everything. A finding never contains a secret's value. The platform adds the
  capability to the service contract; it names no scanner.
- **The checkers (components, content).** A first-party credential-and-danger
  check is a service component in its own repository, with its pattern table and
  its tests; an organization installs it, adds its own, or writes another
  (licence, banned words, a malware service). It is a tripwire, not a
  boundary: a host's policy and sandbox are the boundary. Guidance on what to do
  with a finding is content.

### 4.6 A publish that does not happen says so

A site or project not published because of a cap, a skipped source root or an
invalid path posts a **notice** in the room to the turn's requester, naming
the cause and the limit (`bnbloc.bridge.site_too_large` and its siblings are
today only log lines). The agent learns it through the room records it already
reads each turn (which file carries it is decided at build time), so it does
not report a missing deliverable as delivered. One owner decides "published,
unchanged, skipped (reason)" for sites and projects. This part stands alone
and ships first (§12, slice 1).

### 4.7 Signals: is it worth trying

A viewer decides from what others did, an owner from whether anyone uses the
work. Both are counts, kept without any person's identity:

- **Owner.** Opens and distinct openers of the app, and, for a backend, its
  calls and errors, over days. Counted at the gateway, where a page is opened
  and a backend called, once per (app, day, participant pseudonym); pruned by
  retention. The owner sees them on the app's card. This is the one part that
  is the platform's: only the gateway sees an open or a call.
- **Gallery.** The existing favorites and the existing "used by N"
  (`content_component_uses`) stay. **Public stars and a popularity sort are not
  proposed**: nobody has asked, and a ranking is a product opinion. If wanted,
  it is its own record on the content system.

## 5. The app's backend

### 5.1 Where it is reached

A backend belongs to one **app** (the site artifact's id, the same `app_id`
as the data space). The page calls it under its own capability:

```
~app/backend/{path...}     any method; WebSocket and server-sent events pass
```

- Scope comes only from the capability (member ticket, share ticket), exactly
  as `~app/c/…` does: no route takes an app, room or tenant from the request.
  `TestAppDataScopeFromCapabilityOnly`'s rule extends to this route.
- The request is forwarded to the host's ingress. **The upstream is never
  derived from the request**: it is the registered workload, so a path or a
  header cannot reach another address (no SSRF by construction).
- No cookie and no session header travel in either direction. Hop-by-hop
  headers are removed. Redirects are not followed. Request and answer bodies
  are capped (a wire constant; streams count against an idle bound, not a
  clock). Each route is a route-table row with its own rate budget, writes
  stricter than reads.
- Sites keep their opaque origin and the sites origin (`BNBLOC_SITES_ORIGIN`);
  the backend path is served only there, once it is set.

### 5.2 The caller's identity: a signed fact the backend can verify

The gateway adds `X-Bnbloc-Caller`: base64url JSON `{org, room, app, kind,
participant, account?, origin, iat}` where `kind` is `member`, `org-link` or
`anyone-link`, `participant` is the per-app pseudonym (`me().id`), and
`account` (login, email) is present only for a signed-in caller. It is signed
over the method, the path with its query, the header's value, a hash of the
body and the issue time, with a **per-backend verification key**
(`component.Sign`'s shape). Any `X-Bnbloc-*` header the client sent is removed
first. The backend gets the key as `BNBLOC_BACKEND_VERIFY_KEY`, so it can
refuse unsigned or replayed calls. An anonymous caller never has an account.

### 5.3 What the backend can call

The host gives the backend two environment values:
`BNBLOC_APP_API` and `BNBLOC_APP_TOKEN`, a per-backend token that acts as the
app's owner scope over **its own app's** data space, files, notify and AI,
through the same routes and the same owners as the page. It reaches nothing
else. The token is rotated on every deploy and revoked with the backend.
State a backend keeps is its choice: the app's data space (encrypted,
exportable, crypto-shreddable), or a declared volume.

### 5.4 The registry

A new room-owned table (`app_backends`; listed in `roomOwnedTables`, in
`migrations.lock`): app id, tenant, room, host (component and instance
reference), spec and bundle digests, state, **sponsor**, sealed verification
key, created and updated. States: `pending → starting → running ⇄ idle`,
`failed`, `suspended`, `deleting`, and **`unknown`**: a host that cannot be
read right now. `unknown` is never `failed` and never absent: no sweep acts on
it, nothing is deleted or restarted on it, and the card says the host could
not be reached. (A probe that could not run is not an answer.) Writes go
through one owner in the room
service; the gateway reads. Deleting an app or its room deletes its backend
(`DeleteWorkload`, purge) before its rows.

### 5.5 Connections

A backend that needs a connected system uses the connections component the
way the page does: through a platform primitive that attests the caller and
asks the component (the `appProxy` follow-up of
`docs/prd/2026-10-23-connections-ai.md`'s line of work, its own record). Until
that exists a backend reaches only the hosts its egress rules and the policy
allow. A backend never receives a member's connection credential.

### 5.6 Releases: the page and its backend move together

A page that calls a backend and the backend it calls must be the same
version. Today a link follows the latest site version
(`docs/prd/2026-10-12-interactive-apps.md`), which is right for a page and
wrong for a page with a backend: an agent's next edit would give every link
holder a page that calls an endpoint the running backend does not have.

- An app **with a backend** serves its page from the **released** version, the
  one its backend runs. Members in the room see the latest as a preview, in
  the room, as they do now. An app with no backend is unchanged.
- **Release** is an owner's act (or an agent's, when the owner asked it to):
  snapshot the backend's volumes if the host has `snapshots`, deploy the new
  version, wait for its health check, then move the page. A deploy that does
  not become healthy **rolls back**: the previous version runs again, the
  snapshot is restored if the new version touched the data, and the owner is
  told why. A host without `snapshots` says so before the release of a
  stateful backend, and the owner decides.
- A release is a row in the registry (versions, who, when, outcome) and an
  audit row, so a rollback to any recent release is one action. A rolled-back
  backend and its page return together.
- This is a mechanism, not a policy: the platform only keeps the pair
  consistent and recoverable. When to release is the owner's.

### 5.7 Waking

A request to an idle backend makes the gateway ask the host to start it. A
navigation gets a starting page; an API call gets `503 starting` with
`Retry-After`, which the SDK retries. How long starting may take is the
host's to report (progress, not a fixed deadline); a host that stops
reporting progress marks the backend `failed` and says why.

## 6. The host contract: `bnbloc.host.contract/v1`

Public package `host/protocol` (types, a Go client) and a conformance suite
beside it, as `sandbox/protocol` and `sandbox/conformance` are for sandboxes.
Package kind `bnbloc.host.package/v1`, signed, hash-pinned, installed by a
super admin, rolled out and tenant-scoped like every component, with
`permissions.network` naming the hosts it reaches.

### 6.1 Discovery

`GET /host/v1/discover` → name, version, contract, protocol range,
**capabilities**, **limits** and **profiles**. Nothing about a host is
hardcoded in the platform: the runtimes it offers, its limits and its
isolation class (`microvm`, `container`, `none`) are read from here.

| Capability | Covers |
|---|---|
| `sourceBundle` | accepts a project bundle and a run spec and runs it |
| `ociImage` | accepts an OCI reference |
| `volumes` | a declared path survives a restart |
| `snapshots` | volume (and optionally state) snapshot and restore |
| `scaleToZero` | an idle workload costs (almost) nothing and wakes on a request |
| `websocket` | the ingress carries upgrades and streams |
| `egressPolicy` | deny by default, allow list per workload |
| `logs`, `metrics` | log stream, usage counters |
| `ephemeral` | a short-lived instance from a bundle: no sponsor, no kept data, removed when its caller is done (§10). Built first by the sandbox host, for the local Try demo. |

Advertising a capability and failing it is non-conforming, as for sandboxes.

### 6.2 Workloads

| Method and path | Meaning |
|---|---|
| `PUT /host/v1/workloads/{id}` | converge to a spec: the bundle or image, the run spec, environment (secrets by reference), volumes, egress, resources, labels. Idempotent. |
| `GET /host/v1/workloads/{id}` | state, version, endpoint health, last error |
| `POST …/{id}/start\|stop\|restart` | lifecycle |
| `DELETE …/{id}[?purge=1]` | stop and remove; data only with `purge` |
| `ANY …/{id}/proxy/{path}` | the host's own reverse proxy: the gateway needs only the endpoint and a bearer, the host chooses its network topology |
| `GET …/{id}/logs` | log stream |
| `GET …/{id}/usage?since=` | monotonic counters: running seconds, CPU and memory time, storage, egress bytes |
| `POST …/{id}/snapshots`, `…/restore`, `…/export` | with `snapshots`, `volumes` |

Errors are `{"error":{"code","message","retryable"}}`; clients branch on the
code. A deploy, stop or delete that times out has an **unknown outcome**, not
a failed one: the host answers `outcome_unknown`, and the platform reads the
workload's state before it retries, so a retry never repeats a side effect
blindly. The platform treats an unreadable host as `unknown` (§5.4). The caller's signed identity and the platform's headers reach the
backend unchanged through `proxy`. A host never sees a member's session.

### 6.3 Where a host runs

- `runtime: "endpoint"`: a remote service the gateway calls; its host is in
  `permissions.network`.
- `runtime: "sidecar"`: a process beside a member's bridge. The gateway cannot
  dial a laptop, so the platform adds one generic primitive: a **member
  channel**, an outbound connection the bridge opens to the gateway that
  carries signed host calls to that member's sidecar components and nothing
  else (the bridge is outbound-only today, as
  `docs/prd/2026-09-11-remote-deployment-adaptation.md` records). It carries
  only calls for backends the member sponsors. The transport (a new channel,
  or the relay service's streams) is decided in slice 5's design review; the
  contract does not depend on it.

### 6.4 What the platform does around a host

| The platform | A host does not |
|---|---|
| attests the caller and signs the call | decide who a caller is |
| admits a deploy against policy and quota, before the host sees it | enforce policy alone |
| seals environment values and passes them by reference | store a value the platform did not give it |
| records every deploy, start, stop, transfer, suspend and delete in the audit log | |
| verifies usage against its own counters and stops an over-quota workload | meter for itself |
| deletes a backend with its room or app, and on suspension | |

## 7. The hosts

### 7.1 The member's own sandbox

A host component of runtime `sidecar`, in its own repository, built on the
public sandbox protocol client. What it adds over a turn's space:

- a **service space** keyed by (app, owner), separate from a turn's space, so
  the bridge's `quiesce` never deletes it;
- supervision: health checks from the run spec, restart on a crash with
  backoff (a loop that keeps failing becomes `failed` and says so, not a
  silent retry every turn), stop when the backend has been idle for the
  policy's idle period, and **stop when the member signs out or the account
  switches**: an identity's end ends what it opened
  (`docs/bugs/2026-10-22-desktop-sign-out-leaves-the-bridge-serving.md`);
- state in the space's durable home (`durableHomes`), so it outlives the
  sandbox machine; nothing outlives `purge`;
- environment values as the sandbox's host-substituted secrets where they are
  tied to hosts, as plain environment otherwise;
- the sandbox's egress policy, narrowed by the spec's `egress` requests and
  the member's allowed hosts (`docs/prd/2026-10-21-sandbox-private-hosts.md`).

It is the personal and development host. Its limits are honest and shown: the
backend is up only while the computer and the desktop are; no backup; others
use it only while it is up. The card says "runs on <member>'s computer,
offline" instead of failing. **A host is never chosen silently**: if the
sandbox is absent (a web-only member, mode `off`, an unsupported platform),
Run offers the cloud hosts the policy allows or says there is none. Running a
shared project natively on the host is never offered as a fallback.

### 7.2 Cloud and other hosts

Components of runtime `endpoint`, each its own repository: a cluster
platform, a container service, a sandbox-endpoint adapter, an organization's
own platform. The platform ships none. A **reference host** (single node,
no scale-to-zero) ships as a sample in its own repository so the conformance
suite runs in CI and on QA, and so the contract is proven by a real
implementation, not a fake that accepts what production refuses.

An organization installs one or more, sets which members may use which
(§8), and may write its own against the public contract.

## 8. Policy, quota and cost

Proposed for the owner to confirm. The platform does not bill. It **meters
and enforces quotas**, as it does for assist budgets.

### 8.1 Who may host

- The member's own sandbox: every member, with no organization cost.
- A cloud host: **off until an organization installs a host and an admin turns
  it on.** Per host, the policy names who may use it (all members, roles or
  groups) and whether a launch needs approval (`none` or `required`, decided
  by one owner function like `contentshare.NeedsApproval`, reusing the
  publication approval rail where it fits).
- Several hosts at once: the policy names the default and the allowed set. A
  host reports its isolation class; an admin decides which class may run
  which code.

### 8.2 Quotas

Set by the admin in the organization's settings at three levels: per member,
per room, per organization (backends, CPU, memory, storage, egress). **No
default number lives in code**: with no quota a cloud host admits nothing. A
host's discovery limits bound what the policy can grant. Exceeding a quota
refuses a new deploy with the reason in words; a running workload over its
quota is stopped by the platform, and the owner is told.

Bounds are **activity and caps, never a clock**: an idle backend scales down
(host capability) or stops (policy's idle period, a setting). There is no
maximum run time.

### 8.3 Who pays

- Each backend has a **sponsor**, by default the member who started the
  hosting, who can transfer it to the room's owner or an admin.
- Usage is recorded per backend and attributed to the sponsor and the room;
  the sponsor and admins see it.
- A gallery work carries **no instance**. *Use this* creates a backend under
  the user's own quota. An author never pays for others' use.

### 8.4 When the sponsor leaves

Deprovisioning removes tenant membership and sessions and keeps room
memberships as history (`orgsync.Deprovision`). So the backend's admission
checks, on positive evidence, that its sponsor still belongs to the tenant,
and a sweep suspends the sponsor's backends when they do not. A suspended
backend is stopped, keeps its data, tells the room's admins and can be
transferred. Deletion happens only by an admin or after a retention the
organization's policy sets; with none set it is kept. Nothing is deleted on
an absence.

### 8.5 Visibility

A backend app follows the link rule the platform already has: organization
sign-in by default; an anonymous link is the creator's per-link choice and the
organization can disable it (`docs/prd/2026-10-13-share-link-sign-in.md`). An
anonymous visitor's traffic to running code is never the default.

## 9. Security

- **Isolation.** A host's isolation class is advertised and shown to the
  admin and the member. Gallery code runs only on a host the policy admits
  for it; the member's sandbox is a microVM, and no fallback to the host.
- **Identity.** §5.2. The backend verifies; the platform strips look-alike
  headers.
- **Scope.** The backend token reaches only its app (§5.3); the registered
  upstream is the only address (§5.1).
- **Egress.** Deny by default on the host; requests in the spec are not
  grants; connections only through the platform's primitive. An
  organization's own egress controls apply to the host's network, and a
  cloud host component names the hosts it needs in its package.
- **Secrets.** Environment values are sealed by the platform
  (`internal/sealbox`, bound to the backend), passed to the host by reference
  and put in at start; never in the room, the agent's context, a work, a log
  or an audit row. A host component is the organization's installed, signed
  code with declared network permissions; it holds a value while the workload
  runs.
- **Data.** The app's data space keeps its encryption and crypto-shred.
  Volumes are the host's; a host without `volumes` or `snapshots` says so and
  the card warns before an upgrade of a stateful backend.
- **Abuse.** Per-app and per-caller rate limits on the path; quotas on
  compute; the kill switch (stop, suspend, delete) for an owner, a room admin
  and an organization admin; an audit row for every one.

## 10. Try demo

- **Pure front-end works**: unchanged (demo SDK, in-memory data).
- **Works with a backend, always**: the detail page shows the recording,
  screenshots (§4.1) and the project. This is the demo every viewer gets, on
  any client, at no cost.
- **Works with a backend, live: on the viewer's own sandbox.** The viewer
  presses Try demo and the work's backend starts in a sandbox **on their own
  computer**, through the sandbox host (§7.1) with the `ephemeral` capability.
  It costs the organization nothing, needs no cloud host and no hosting
  policy, and runs the author's code where the viewer's own isolation
  protects them. A cloud demo stays reserved under the same capability and is
  not built.

How a local live demo works:

- **Same page, different upstream.** The demo ticket names a work version, as
  today, and no room or app. A `~app/backend` call under it is not routed to a
  registry row: the gateway sends it over the **viewer's member channel** (§6.3)
  to the viewer's own sandbox host, which holds the instance. The caller kind
  is `demo`; the participant is the demo's own; no account.
- **Where the viewer is.** The viewer's bridge and sandbox must be online. A
  member in the web client whose desktop is running qualifies, through the
  channel. A viewer without a sandbox gets no Try demo button for a backend,
  and the page says why in one line and points at the recording. There is no
  fallback to running it on the host or in the cloud.
- **Consent, said plainly.** One click, with one line: "This runs the author's
  code in a sandbox on your computer." Never started by opening the page.
- **Confinement.**
  - A **dedicated demo machine**, not the member's own or the shared machine:
    the reference sandbox enforces egress per machine and offers no per-space
    egress (`docs/prd/2026-10-21-sandbox-private-hosts.md`; the bridge does
    not use `spaceEgress`), so a narrower policy needs its own machine.
  - Egress is an **allow list**: the package registries the host's profile
    names, for the install, and the spec's `egress` requests. Never a private,
    loopback or link-local range, whatever the viewer's own settings allow for
    their other agents. A sandbox that cannot enforce this offers no live demo;
    it is never widened silently.
  - No connections, no credentials and no host mounts. Environment values are
    only the spec's `demo.env`, which the author declares non-secret; a
    `secret` variable is unset, and a backend that needs one cannot opt in.
  - An ephemeral volume, deleted when the demo ends. Resource caps come from
    the sandbox's discovery limits. One demo at a time per viewer.
- **Data.** The demo gets no app token: the backend cannot reach the real
  data space, and the page's demo-mode SDK keeps its data in the page. A
  backend that keeps its own state in its ephemeral volume demos fully; one
  that depends on the app's data API cannot opt in, and the review says so.
- **When it ends.** When the viewer closes the page, signs out or switches
  account, when the ticket expires, or when it has been idle, whichever comes
  first; the instance and its volume are removed, and a demo left behind by a
  crash is swept the next time the sandbox starts.
- **Opt-in by the author** (`demo.enabled` in the spec), shown to the author
  in the review (§11) and in the publish acknowledgement (§4.5); the
  organization's policy can turn live demos off.
- **Start-up cost** (an image pull, an install) falls on the viewer, once;
  the page shows progress from the host and never a fixed countdown.

Not verified: whether an install can be confined to the registry list on the
reference sandbox as it is today, how long a cold demo takes, and how the web
client reaches a desktop that is on another network. The first decides
whether slice 9 needs a sandbox change; none changes the contract.

## 11. AI

AI proposes; a person decides; no model sits in a request path.

- **`draft-run-spec`**: from the project's file list and manifests, drafts the
  run spec; the author edits and saves it as the file.
- **`review-run-spec`**: before the first run, says in one sentence what the
  backend can do, what it asks to reach, and flags findings (an egress rule
  that is a wildcard, a secret variable named like a credential, a start
  command that fetches and runs a script). Shown beside Run; it never blocks.
- **`draft-listing`**: from the files and the recording's absence or presence,
  drafts the gallery description, category and tags.

All three are assist tasks over the one assist seam
(`docs/architecture/ai-assist.md`): **content**, ordinary assist packages kept
in the content repository (§3.1a) and published at platform scope, so an
organization edits or replaces them; the platform ships no prompt for them.
Its off switch is honoured. With assists off every step works by hand and says
why the AI part is gone. The room's agents learn no new tool and no new rule:
they build a project the way they build anything, guided by the existing app
skill pack's few new lines (the recording, the run file) and, where an
organization wants more, a `backend-apps` skill pack and a "Backend builder"
expert in the same content repository, with starters as samples.

## 12. Delivery slices

Each slice is part of the end state and stands on its own. Slices 1 to 3 need
no host at all.

| # | Slice | Acceptance, and the tests that would fail if wrong |
|---|---|---|
| 1 | **Publishing says what it did and what it left out, and the publish gate.** Notices for skipped sites; the left-out list; the gate (§4.5) and the `publishCheck` capability on gallery, anyone-links and organization publications. The first-party check component (its own repository) follows as a separate deliverable. | A site over the file cap, a source root and an invalid path each post a notice with the real cause and limit. With a check component that reports a planted credential the gate blocks the gallery and an anyone-link and only warns an organization share; a check that cannot finish blocks and says retry; with none installed the policy decides, and an agent cannot acknowledge a finding. Tests run the gate against a fake check, and the first-party check against a hostile 20 MiB file inside its own bound. |
| 2 | **Media.** Validators, caps, serving, the detail carousel and player; the skill pack update. | Files are typed by bytes (a renamed script is refused; an SVG is never media); an over-cap file names its cap; a work with none still publishes; the always-on skill budget test stays green. |
| 3 | **Project works.** Artifact kind and work type `project`; whole-folder snapshots decided by the project's own `.gitignore` and run spec; the bundle route and archive; run spec validation; the run record. The file-tree viewer is a previewer component in its own repository. | A project round-trips: published, listed with its left-out files, copied by *Use this* into a room with an empty data space and no secret; an unchanged project is a no-op; a project beside its site publishes both. |
| 4 | **Backend ingress, identity and registry.** `~app/backend`, `X-Bnbloc-Caller`, the verification key and app token, `app_backends`, policy and quota admission, audit. | Scope comes only from the capability (static scan plus a behavioural test: app A's capability never reaches app B's backend); a forged or replayed caller header is refused by a reference backend; a client-sent `X-Bnbloc-*` is stripped; a destroyed app deletes its backend. |
| 5 | **Host contract and conformance.** `host/protocol`, `bnbloc.host.package/v1` validation, the conformance suite, the reference host as a sample. | The suite passes against the reference host and fails against a host that ignores `purge`, `egress` or the bearer. |
| 6 | **The member's sandbox host and the member channel.** | A backend started by an agent in a room runs in its own service space and survives the turn; survives a desktop restart and a sandbox update with its durable data; stops on sign-out and on an account switch; a crash loop ends in `failed` with a message; a closed lid shows "offline", not an error. Verified on a real desktop, not only a test process. |
| 7 | **Policy, quota, metering and the sponsor.** Settings, usage rows, enforcement, deprovision handling, transfer. | A sponsor removed from the tenant suspends their backends and nothing is deleted; an over-quota workload is stopped and the owner told; no quota means no cloud admission. |
| 8 | **Releases and rollback** (§5.6). Versions in the registry; the page served from the released version; snapshot, deploy, health check, roll back. | A release whose new version never becomes healthy leaves the previous page and backend serving, restores the snapshot when the data was touched, and tells the owner why; a host without `snapshots` is said so before the release. Run across a real desktop restart. |
| 9 | **Local live demo** (§10): the `ephemeral` capability on the sandbox host, the demo ticket routed over the viewer's channel, the demo machine. | A demo cannot reach the real data space or any connection; its egress refuses a private address even where the viewer's own agents may reach it; it ends on page close, sign-out and idle, and a crash-left instance is swept at the next start; a viewer without a sandbox sees the recording and a one-line reason, never a silent fallback. |
| 10 | **Owner counts** (§4.7). | Counts carry no identity beyond the per-app pseudonym; counters are pruned by retention; an unreadable counter says so. |
| 11 | **Content**: the `backend-apps` skill pack, the Backend builder expert, the three assists and example projects, in a content repository. | Published through the Library at platform scope; the platform has no copy of any of them; the always-on skill budget test stays green; each assist works with the organization's own model and is absent, with a stated reason, when assists are off. |

Order of risk: slice 1 fixes a defect that exists today and is first. Slices
4 and 5 are provider-independent and come before any host.

## 13. Design review

| Lens | Answer |
|---|---|
| Actors | The sponsor, room admins, other members, a borrower, a visitor on a link, an organization admin, the host component, the departed member. A borrowed agent never starts a backend on the owner's host (only the owner's own drive does); a visitor reaches only what the capability names; the backend sees a caller's kind and never a session. |
| Time and lifecycle | A backend outlives a turn on purpose, so it is bound to the sponsor's membership, the room and the app, not a turn. It stops when its owner's sign-in ends, is suspended when the sponsor leaves, is deleted with the room. A restart of the desktop, the sandbox or a host keeps data in the durable home or volume and restarts supervised backends. No fixed time limit; idle and activity bound it. A result landing late (a failed start) is shown against the backend it names. |
| Scale and cost | Per-backend, per-member, per-room and per-organization quotas, set by the admin, none defaulted in code. A shared host cannot starve others: admission before deploy, stop on overshoot. A project is bounded by the work caps; media by its caps. Usage accrues per hour and is cheap to read. |
| Failure and recovery | Every cross-process call (host, ingress, tunnel) has a stated user-facing failure: starting, offline, failed with a cause. A deploy is idempotent, so a retry repeats no side effect; the platform's registry is the record of intent and a sweep converges hosts to it. A crash loop becomes `failed`, never a silent retry. A host that fails conformance is not admitted. A host that cannot be read is `unknown`, not `failed` and not absent: no sweep restarts, suspends or deletes on it. A timed-out deploy is `outcome_unknown`, read before any retry. A release that does not turn healthy rolls back. |
| Boundaries and privacy | Capability-only scope; registered upstream only; no cookie crosses; a backend token reaches its own app; secrets sealed and never in a log, work or room. The member channel carries only signed host calls for that member's sponsored backends. A copy from the gallery carries no data. |
| Abuse and untrusted input | Gallery code is untrusted: isolation class gates where it runs, egress denies by default, quotas cap compute, the publish gate and its check components stop credentials leaving, the kill switch exists at three levels. Spec text, README and file names are fenced as data in every assist; the backend's answers are the page's data, never rendered by the platform's own UI. A look-alike header is stripped. A local live demo runs the author's code on a stranger's computer: it starts only on a click, only in a dedicated demo machine with an allow-listed egress that never includes a private range, with no credential, connection, data or mount, and ends with the page. An agent cannot acknowledge a finding or a changed request on a person's behalf. |
| One owner | Identity: the gateway's caller header. Scope: the capability resolver. Backend state: the room service's registry. Policy and quota: one admission function. Usage: one meter. "Published, unchanged, skipped": one publisher. The publish gate: one function. Host behaviour: the contract and its suite. Architecture tests forbid a second copy of each. |
| Defaults and fallbacks | No host, no policy, no quota → no cloud hosting, and the card says so. No sandbox → no silent native run. No run spec → an assist drafts one, a person confirms. No media → a generated cover and a plain statement. An unknown host capability is not assumed. A sponsor's absence suspends, never deletes. |
| External contracts | The host contract is ours and public; the real reference host proves it in CI; the host's timing guarantees (a deploy acknowledged before the workload is healthy, the health check owning "running") are written in the contract and checked by the suite. The sandbox protocol is used unchanged by the sandbox host. |
| Load on users and agents | The member presses Run and fills named variables; an organization admin sets a policy once. Agents learn a few lines about recording and the run file and no tool. The platform does isolation, identity, ordering and metering so nobody is told to. |
| Release and compatibility | Additive everywhere (new kinds, tables, routes, events, a new package kind); pre-launch, so no compatibility code. Order: gateway and room service, then bridge and desktop, then web, then host components with `optin` rollout first. A desktop or sandbox update with a running backend is replayed (before and after); host components are `optin` before `auto`. |
| Observability | Backend state, host, spec and bundle digests, last health and last error are visible to the sponsor and admins; logs stream from the host; every lifecycle step is an audit row (`kind: backend`); usage counters per backend; a version identity on every host. An end-to-end scenario builds a backend in a room, runs it, calls it from the page, restarts the bridge and checks the data. The owner has the same view in the CLI (`bnbloc backends list|logs|stop|release`), as every owner action has. |
| AI capability | §11: three assists, each proposing, each with a manual path. |
| Content, component or platform | §3.1a is the answer, row by row. In one line: **platform** only for the registry, ingress, caller identity, policy and quota, metering, audit, the publish gate, notices, owner counts, the project and media bytes, the member channel, and the host and check contracts with their suites; **components** for every host, every checker and the project viewer; **content** for the recording and run-file guidance, the skill pack, the expert, the assists and the examples. The platform names no provider, runtime, language, ecosystem or scanner; an organization wanting its own host or check installs or writes a component and forks nothing. |

**Pre-mortem.** It shipped and a month later it failed badly:

1. **"A shared app went down every night."** It ran on its builder's laptop.
   The card says where it runs and that it is offline, the policy can require
   a cloud host for apps with more than one user, and the room is told when a
   shared backend's host goes offline.
2. **"The bill, or an incident, from code nobody read."** A gallery project
   ran on a cloud host, mined, or reached an internal address. Deny-by-default
   egress, isolation class gating, quotas with no default, the publish gate, the
   review before first run, the kill switch and the audit trail; a launch can
   require approval.
3. **"A backend trusted a forged caller, or one app reached another."**
   Signed header with a per-backend key and a stripped client copy; the token
   and the upstream scoped by the registry; the scope test is behavioural and
   static.
4. **"A departed member's services kept running, or were deleted with their
   data."** Admission checks membership on positive evidence; suspension keeps
   data; deletion is an explicit admin act or a configured retention.
5. **"The contract worked for the sample and nobody else."** The conformance
   suite is the contract; a second host (the sandbox one) is built on the same
   suite before the cloud host is called supported.
6. **"A viewer's computer ran something it should not have."** A local live
   demo is a click, a dedicated machine, an allow-listed egress, no
   credentials, data or mounts, and it ends with the page. A sandbox that
   cannot enforce the egress offers no demo rather than a weaker one.
7. **"The page and its backend drifted, and a release broke every link."** An
   app with a backend serves its released version; a release is snapshot,
   deploy, health check, roll back.

## 14. Not in this record

- Billing and prices. The platform meters and enforces quotas only.
- A build system, buildpacks or a language list. A host decides how to run a
  start command.
- Backends called from outside the app (inbound webhooks, public APIs). The
  caller identity and the capability assume the app's page; an external
  caller needs its own authentication design.
- A backend-only deliverable with no page (called by an agent or a schedule).
  Its entry point is a follow-up.
- A cloud live demo (§10 reserves the capability).
- Custom domains per app.
- The app base's own gaps and connections for apps: they are one companion
  record, `docs/prd/2026-10-24-app-data-rules-and-connections.md`, because
  they change the app base and the connections contract, not hosting.

## 15. Open items and what was not verified

For the owner to confirm: whether the first-party check component is installed
by default for every organization (the gate itself is the platform's; with no
check installed the policy decides whether outward publishing may proceed),
and §8's defaults (a cloud host off until enabled, the
sponsor, suspension without deletion, no default quota, an organization-only
link as the default), the proposed media caps, and the package kind name
`bnbloc.host.package/v1`. And one that **changes an earlier decision**: §5.6
makes an app with a backend serve its released version instead of following
the latest (the interactive-apps record chose "the link follows the latest
version so fixes reach everyone"). It stays true for an app with no backend.

Decide in the slice's own design review: the member channel's transport
(slice 6), whether the sandbox host is a component or native to the bridge
(slice 6; recommended default: a component, with the bridge giving it only
the channel and the sandbox endpoint), and which room record carries the
publishing notices (slice 1).

Not verified when this was written (2026-10-24): that the reference sandbox's
`ports` works end to end in a real VM; what a cloud host costs or how fast it
starts; whether the relay service can carry the member channel; the approval
rail's fit for launch approvals; and any internal platform an organization
could install as a host. None of them changes the contract; each changes a
slice's plan.

## 16. What a comparison with another hosted-page tool contributed

An internal comparison (2026-10-23) with a tool that hosts agent-written pages
behind a login and gives them a generic backend produced the list below. Each
row says where it went, so nothing from it is lost or silently dropped.

| Idea in that tool | Where it is |
|---|---|
| Publishing is gated on the server for every caller; a scan blocks high findings, lower ones need a person's acknowledgement, and an agent's flag never substitutes | §4.5, split by the core rule: the gate is platform, the scanners are `publishCheck` components (§3.1a); the changed-requests acknowledgement is in it too |
| A preview address of the latest and a published address frozen at a release | §5.6, applied where it matters: an app with a backend |
| Version snapshots as `file → hash`, rollback all-or-nothing with an undo | §5.6 uses the host's `snapshots` for data; the page's versions are already immutable blobs |
| Usage statistics | §4.7 (the owner's counts; only the gateway sees them). A public star and a popularity sort were dropped as a product opinion nobody asked for |
| A write that times out has an unknown outcome; re-read before retrying | §6.2 (`outcome_unknown`) |
| A probe that could not run is not "absent" | §5.4 (`unknown`) |
| Connections as the way a page reaches internal systems, bound to a page with the owner's acknowledgement | the companion record (connections for apps); the acknowledgement of changed requests is §4.5 |
| Page-readable limits and usage, so a page never hardcodes caps | the companion record |
| An ephemeral, unpersisted broadcast channel | the companion record |
| A CLI that covers everything the web admin does | §13 Observability (`bnbloc backends …`) |
| SDK type declarations for the coding agent | §4.1 |
| An owner-scoped record store, a SQL/Redis data source reached through a connection | not borrowed: the app base's data space is stronger (shared, per-author, concurrent); a data source is a connector's business |
| A same-origin page with the visitor's session, "the page runs as whoever opens it" | **not borrowed, on purpose**: the opposite of §5's capability-only scope |
| Declarative `actions` (http/sql/loop steps stored as data) | not borrowed: AI schedules and a backend cover it |
| Team spaces | not borrowed: rooms already are the space |
