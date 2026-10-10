# Extension market — from a core store to a multi-kind platform

**Date:** 2026-09-20
**Status:** shell landed + the first second kind (`plugin`) landed. The
`auth-provider` kind has since landed as `bnbloc.auth-provider.package/v1`; the `webhook`
kind remains design-only. This doc now
records the AS-BUILT contract; see "What actually landed" for the deltas from
the original design.

## The mistake this corrects

The market shipped in `docs/prd/2026-09-20-plugin-market.md` (historical: its
routes are gone, superseded by the Components API) could only install one
thing: an agent core. The package format hardcodes
`protocol.ProtocolRange` (a Core Protocol v1 concept) and install ended in one
call — `core.Reregister(...)` producing a `core.Provider`. So despite the name
"plugin market", it was an **agent-core store**.

That was scope tunnel-vision: the driving problem was an installable core, so the
market became core-shaped. A real extension market must host more than cores —
authentication providers (enterprise SSO is one), event/webhook subscribers, and
whatever comes next. Saying "SSO can't use the market" was wrong: SSO can't use
*this* market because the format had no way to describe anything but a core.

## What the industry actually teaches here (and the trap to avoid)

The research already gathered (Terraform, Kubernetes, Envoy, Vault) is
consistent, and it cuts against a naive "one universal package" fix:

> **Different extension points have different contracts.** Kubernetes did not
> build one plugin protocol; it built CRI, CSI, CNI, device plugins and
> admission webhooks — separate contracts, each fitted to its job. Envoy layers
> xDS / Lua / WASM / ext-proc rather than forcing one mechanism.

So the goal is **not** a single package schema that can express any extension —
that road leads back to the manifest-DSL mistake, a format growing a field per
extension type. The goal is a **shared market shell** (distribution, signing,
super-admin install, console, registry) over a **small set of typed extension
contracts**, each plugging into a seam that already exists in this codebase.

## The shape (as landed)

```
Market shell  (landed — shared by every kind)
  ├─ signed package upload + ed25519 verification against trusted keys
  │    (POST /v1/platform/components; keys under /v1/platform/component-keys)
  ├─ rollout policy (auto | optin | off) + per-tenant enablement, member opt-in
  │    (component_installs), adoption counts, audit (extension_audit)
  ├─ platform-settings registry, rehydrated at boot
  └─ Components Workbench console UI (admin + member surfaces)
         │
         ▼  package.kind selects the contract
  kind: "bnbloc.core.package/v1"    → Core Protocol v1 → core.Register     (landed)
  kind: "bnbloc.plugin.package/v1"  → plugin sidecar   → realtime hooks    (landed)
  kind: "auth-provider"              → an auth seam     → the gateway's SSO hook   (design)
  kind: "webhook"                   → an event seam    → realtime Hub.Publish     (design)
```

`kind` is an explicit, required field on the package envelope
(`core/pkgmeta`). Each kind carries only the fields its contract needs — a
core carries `protocol` + `capabilities` + `runtime`
(`endpoint|manifest|sidecar`); a plugin carries `contract` + `hooks` +
`artifacts` + `sidecar`. `extpipe.Install` dispatches on `kind` to a
per-kind installer registered in the pipeline (`NewCorePipeline` registers
`core` and `plugin`; realtime's `NewPluginPipeline` registers `plugin` for
its host).

## Seams

- **core** — `core.Register` + `coreset.FromEnv`. Landed and wired; see
  `docs/architecture/core-providers.md` for the full contract.
- **plugin** — landed. `internal/pluginruntime` defines the typed installer;
  realtime's `PluginHookDispatcher` selects, per event, the member streams
  entitled to it (`GET /v1/my/plugin-events`); each member's desktop posts
  the versioned envelope to its supervised sidecar's `POST /events` and acks
  the outcome, which realtime audits. The hook registry is code-owned:
  `chat_message.v1` and `agent_messages.v1`
  (`core/pkgmeta/package_validation.go`); a plugin declaring anything else
  is rejected before persistence. Gateway and realtime never import or
  execute plugin code and never launch processes — the desktop sidecar
  manager supervises plugin processes (member-scoped, sha256-verified).
- **auth-provider** — `services/gateway/service.go` holds `SSO any`, a
  type-erased hook whose public build reports "not configured". That hook is
  the seam an `auth-provider` package would populate. Still design-only.
- **webhook** — `internal/realtime` hub publish is the choke point every room
  event already flows through. Still design-only; the landed plugin hooks
  (chat_message.v1, agent_messages.v1) already cover the subscribe-to-room-
  events case for supervised sidecars, so a separate `webhook` kind is now a
  question of outbound HTTP delivery to remote endpoints, not local sidecars.

## The plugin contract as landed

`bnbloc.plugin.package/v1`, pinned to contract version
`bnbloc.plugin.contract/v1`:

- `runtime` must be `sidecar`; the sidecar runs out of process, supervised
  by the desktop's sidecar manager (never gateway/realtime).
- `contract` must equal `bnbloc.plugin.contract/v1` — this pins the delivery
  envelope shape so a future envelope change is a new contract, not a silent
  reinterpretation.
- `hooks` must be non-empty and every entry must exist in the code-owned
  registry (`chat_message.v1`, `agent_messages.v1`).
- `artifacts` (platform binary → URL + sha256) are required; URLs come from
  the gateway's own `POST /v1/platform/artifacts` staging route so the signed
  envelope pins gateway-produced blobs.
- `sidecar.localUrl` must be a loopback http URL — the member's desktop
  delivers events only to `POST /events` on its own machine (the server never
  dials a sidecar; see `docs/bugs/2026-10-08-plugin-hook-delivery-unreachable.md`).
- Plugin capabilities: `room-events` only.

Delivery envelope (realtime → member stream → desktop → sidecar,
best-effort, per member, audited on the desktop's ack):

```json
{
  "event": "chat_message",
  "version": "1",
  "tenantId": "…",
  "roomId": "…",
  "payload": { "…": "existing wire payload" }
}
```

Delivery is scoped by rollout, tenant overlay, and (for optin plugins)
whether an event subject has the plugin installed and is a room member.
Realtime never blocks on a plugin and never lets a plugin mutate the source
event.

## What this does NOT become

- **A universal package that can express any extension.** Rejected for the same
  reason the manifest DSL was frozen: a format that grows a field per extension
  type is a programming language with worse tooling. Each kind is its own typed
  contract.
- **A source-plugin / in-process code loader.** Still out (Go `plugin.Open`,
  WASM as primary mechanism, etc., per the earlier design). Extensions remain
  out-of-process daemons, endpoints, or declarative config — never linked code.
  The landed plugin kind is a supervised sidecar, not in-process code.
- **A hosted registry or auto-update.** Still explicit non-goals; upload-a-file
  stays the primitive.

## Migration (as it actually happened)

The clean cutover in
`docs/prd/2026-10-01-components-workbench-redesign-design.md` (plan:
`docs/plans/2026-10-01-components-workbench-redesign.md`)
made the shell generic in one change rather than adding a `kind` shim:

| Old (pre-2026-10-01) | New (landed) |
| --- | --- |
| `POST/GET/DELETE /v1/platform/core-packages` | `POST/GET/DELETE /v1/platform/components` |
| `PUT /v1/platform/core-packages/{name}/rollout` | `PUT /v1/platform/components/{name}/rollout` (+ `PUT .../tenants`) |
| `GET/POST/DELETE /v1/platform/core-package-keys` | `GET/POST/DELETE /v1/platform/component-keys` |
| one kind: core | two kinds: core (`bnbloc.core.package/v1`) + plugin (`bnbloc.plugin.package/v1`) |
| no rollout policy | rollout `auto\|optin\|off` + member `component_installs` opt-in |
| Platform console "Core packages" paste form | Components Workbench (upload flow, validation timeline, review, rollout) |
| `web/src/views/PlatformCorePackages.tsx` et al. | `PlatformComponents.tsx` / `MemberComponents.tsx` (old files deleted) |

Legacy `/v1/platform/core-packages*` routes now 404 (pinned by test). The
dev stores are disposable; no data migration was written — dev installs were
re-published through the new routes.

## Acceptance

- A package declares its `kind`; install dispatches to a per-kind installer.
- The existing core packages keep installing unchanged.
- Adding a new kind requires defining a contract + an installer, but **no change
  to the shell** (upload, signing, registry, console).
- Public/internal split holds: an internal-only kind (e.g. an internal SSO package)
  is distributed on the private remote, never the public mirror.

## Landing (when approved, per kind)

- ~~Shell: `core/pkgmeta/` gains `kind`~~ — landed, including the generic
  `RegisterKindValidator` dispatch and the clean-cutover rename off the old
  core-package routes.
- ~~`plugin` kind~~ — landed (`internal/pluginruntime`, realtime hook
  dispatch, desktop supervision).
- `auth-provider` kind: its own PRD before code.
- `webhook` kind: its own PRD before code.
