# Plugin hook delivery targets a loopback URL realtime cannot reach

Status: fixed (G1, batch 2 commit "feat: batch 2 — …")

## Repro

1. Install a plugin package (`bnbloc.plugin.package/v1`, `runtime: "sidecar"`)
   with `sidecar.localUrl: http://127.0.0.1:<port>` and a `chat_message.v1` hook.
2. The member's desktop sidecar manager downloads and launches the sidecar on
   the member's machine, listening on that loopback port.
3. Send a chat in a room of that tenant.

Expected: the sidecar receives `POST /events`.
Actual (any deployment where realtime is not on the member's machine): realtime
POSTs to `127.0.0.1:<port>` on its own host/container; the request fails and is
audited as `failed`. It only works in single-host development.

## Root cause

`internal/pluginruntime` resolves the delivery target from the package's
`sidecar.localUrl`, and `services/realtime/plugin_delivery.go` posts to it
from the server. But sidecar supervision is the desktop's job (AGENTS.md), so
the process lives on member machines that the server cannot dial. The design
has the event source (server) and the process (client) on different hosts with
no transport between them.

## Fix (decided: deliver over the member's own channel)

Realtime stays the hook authority and the auditor; it no longer dials
anything. The server-side POST path (`pluginruntime.Sidecar`/`NewSidecar`, the
NetGuard'd delivery client, `PluginHookDispatcher.post`) is deleted.

Transport: a per-member SSE stream held by the desktop that supervises the
sidecars (the desktop already owns the gateway session and the sidecars'
runtime URLs; the bridge knows neither plugins nor their ports).

```
chat/agent_messages publish (realtime)
  -> PluginHookDispatcher.Deliver: non-blocking hand-off to a bounded queue
  -> fan-out worker: records snapshot (rollout/tenant/hook) x connected
     member streams (session tenant scope, opt-in install, room membership)
  -> GET /v1/my/plugin-events (SSE, gateway -> realtime, member's session)
       event: plugin_event   data: {"id","plugin","envelope"}
  -> desktop internal_ext/plugin_events.rs: POST <loopback runtime url>/events
  -> POST /v1/my/plugin-events/{id}/ack {"status":"delivered"|"failed","detail"}
  -> extension_audit: kind=plugin action=deliver actor=<member> outcome
```

Semantics:

- **Per-member delivery.** Each member of the event's room whose desktop runs
  the plugin and holds the stream gets the event on their own sidecar; two
  members with the same plugin each get it (inherent to per-member
  sidecars). Every desktop stream of one member receives it (bounded at 8
  streams per member).
- **Entitlement** = the member-visibility gate: rollout not `off`; the
  record's tenant scope admits the event's tenant AND the member's session
  tenant (what their desktop's catalog lists); `optin` only for members with a
  `component_installs` row (per-stream cache, 5s); member of the event's room
  (membership probed per event per member, off the publish path).
- **Best-effort.** No offline queue: a member without a connected desktop
  misses the event. Bounded everywhere: publish queue 256 (overflow dropped,
  logged), per-stream buffer 64 and pending-ack table 4096 (overflow dropped
  and audited `failed`), unacked deliveries audited `failed` after 60s.
- **Desktop guard.** The desktop only posts to the plugin's running runtime
  URL and only if it is loopback http; a delivery for a plugin that is not
  installed/running there is acked `failed` with the reason. The stream is
  open only while the desktop has a plugin sidecar installed, and is
  reopened when the gateway session changes.

Routes: `GET /v1/my/plugin-events`, `POST /v1/my/plugin-events/{id}/ack`
(gateway routes both to realtime; `PermRead`). Wire: `internal/wire/plugins.go`.

Tests: `services/realtime/plugin_hooks_test.go` (SSE end-to-end + ack/audit,
per-member, opt-in/off, tenant scope, rehydrate, uninstall-elsewhere,
snapshot/fail-closed, full-buffer drop, no-stream no-queue, per-member stream
cap), `services/gateway/plugin_events_route_test.go`,
`apps/desktop/src-tauri/src/internal_ext/plugin_events.rs` (SSE parser,
loopback target, ack body, POST /events).

Member notifications use this seam for personal delivery only: the
`notification.v1` hook carries one member's notification to their own
desktop plugin (with a server-notifier fallback); the organization's
delivery stays a server-side `bnbloc.notifier.package/v1` endpoint
component (`docs/notifications.md`).
