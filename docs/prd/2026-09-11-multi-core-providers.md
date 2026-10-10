# Design — Multiple concurrent core providers

Status: **implemented; built-in core auto-discovery superseded** by
`docs/prd/2026-09-30-no-builtin-cores.md` (cores now arrive only by install). Extend the local-agent core from a single active
provider (`BNBLOC_CORE_PROVIDER`, default nimbus) to N providers running at
once, discovered automatically, with every agent target labeled by its core so
the web + desktop surfaces show all of them grouped by core. Verified live:
gateway resolved both nimbus (listener discovery) + a second REST core (:6767 probe) and
aggregated core-qualified targets; a core with no agents degrades gracefully.

This is the "per-room / multi provider" item the pluggable-core design
(2026-09-10) deferred as a non-goal. The `core.Provider` interface already
allows it; this design adds the composition, discovery, namespacing, and UI.

## Vocabulary (the codebase overloads "provider")

- **Core** = the daemon backend implementing `core.Provider`: `nimbus`,
  `acme` (an example REST-daemon core). Selected/instantiated at startup.
- **Harness** = the agent kind inside a core, carried in `core.Target.Provider`:
  `codex`, `claude`. (UI historically calls this "provider" too.)

To end the overload in new code, the wire/UI field for the core is `core` and
the harness stays `provider`.

## Problem: three single-core assumptions (blast radius verified)

1. Gateway holds one `Core core.Provider`; `handleLocalAgentTargets/run` query
   only it (`services/gateway/service.go:49`, `localagent.go:128,137,170`).
2. Bridge holds one `b.core` + one endpoint; keys sessions `room\x00harness`
   (`services/bridge/bridge.go:98,498,559,589`), so two cores exposing `codex`
   collide.
3. Target ids do not encode the core (`local:codex` vs an `acme` UUID), so
   nothing downstream can route a harness back to the right daemon.

## Decisions

### D1 — Core-qualified target id
A target id becomes `"<core>/<rawid>"`, e.g. `nimbus/local:codex`,
`acme/agent_abc123`. The gateway/bridge split on the FIRST `/` to recover
`(core, rawid)`; `rawid` is passed to that core's provider unchanged. This is
the single routing key that flows through targets → chat recipient → bridge.
Chat `recipient` gains an optional `core` field alongside `provider` (harness);
absent `core` = legacy single-core behavior (default core), so old clients keep
working during rollout, then the web always sends it.

### D2 — Cores config list + auto-discovery
Replace the single `BNBLOC_CORE_PROVIDER` with a list. Each configured core is
`{name, url?, token?}`. Empty url/token are filled by per-core discovery.
Env form (gateway + bridge): `BNBLOC_CORES="nimbus,acme"` names which cores
to run; per-core overrides `BNBLOC_CORE_<NAME>_URL` / `_TOKEN`. When
`BNBLOC_CORES` is unset, fall back to `BNBLOC_CORE_PROVIDER` (default nimbus) —
byte-compatible with today.

### D3 — Discovery behind the core seam (per-core mechanism)
nimbus discovers via its listener file (a descriptor file in its data dir,
random port+token); acme has no file — it uses a fixed default port `:6767`
and an optional configured token. So discovery is per-core, not one gateway
hack. Add an optional capability interface:

```go
// Discoverer is implemented by cores that can locate their local daemon
// without explicit config. Optional: cores that cannot stay config-only.
type Discoverer interface {
    Discover(ctx context.Context) (Endpoint, bool)
}
```

nimbus's Discover reads the listener descriptor (path injected via a constructor
field, keeping os specifics out of core); acme's Discover probes
`http://host.docker.internal:6767/v1/info` (+ configured token) and reports
found on 200. The gateway's dev-only discovery gating
(`BNBLOC_DEV_DISCOVERY`) still guards returning any token to the browser.

### D4 — Gateway aggregates
`Service.Core core.Provider` → `Service.Cores map[string]core.Provider` plus a
resolver `Service.CoreEndpoints` that yields each core's discovered/configured
`Endpoint`. `handleLocalAgentTargets` iterates all cores, prefixes each returned
target id with `<core>/`, and adds a `core` field to each target JSON.
`handleLocalAgentRun` and the discover endpoint split the incoming target id /
accept a `core` param to pick the core. Response target shape becomes
`{id, provider, core}` (id already core-qualified).

### D5 — Bridge drives N cores
Bridge holds `cores map[string]coreEntry{provider, endpoint}`. Chat claim reads
`recipient.core` (falling back to the sole/default core when absent), routes to
that core's provider+endpoint, and keys sessions `room\x00core\x00harness` so
two cores' `codex` no longer collide. `target := "local:"+harness` becomes the
core's raw target id derived from `recipient`.

### D6 — Web shows all cores grouped
`AgentTarget` gains `core: string`. `useLocalAgent` fetches the aggregated
targets (still one call — the gateway aggregates). RunAgentDialog + Composer
AgentPicker group options by core with a header/badge (`nimbus` / `acme`).
`roomAgents` derivation keys by `(core, harness)` so a borrowed/own agent shows
its core. Chat send includes `recipient.core`.

### D7 — Desktop multi-core settings
`DesktopSettings.coreProvider: string` → `cores: {name, url?, token?}[]`.
BridgePanel renders a repeatable core row (name select from the known set + url +
token, url/token optional when discoverable). Rust `settings.rs` +
`bridge.rs` persist and pass the list (as `BNBLOC_CORES` + per-core env) to the
sidecar. Migrate an existing single `coreProvider` value into a one-element list
on load (no data loss).

## Rollout / compatibility

- `recipient.core` optional; bridge + gateway default to the sole core when
  absent. Once web always sends it, single-core deploys still work (one entry).
- `BNBLOC_CORE_PROVIDER` still honored as the fallback when `BNBLOC_CORES`
  unset. compose keeps working; we set `BNBLOC_CORES=nimbus` explicitly.
- No DB changes.

## Non-goals

- Inbound activity abstraction (stays per-core glue).
- Cross-core session migration.

## Verification

- gateway: aggregated `/v1/local-agent/targets` returns targets from ≥2 fake
  cores, each core-qualified + labeled; run routes by core prefix.
- bridge: two fake cores; a chat to `core=acme` drives the acme fake,
  `core=nimbus` the nimbus fake; sessions keyed distinctly (no collision).
- discovery: nimbus Discover parses a listener file; acme Discover reports
  found on a 200 `/v1/info` probe, not-found otherwise.
- web build green; AgentPicker groups by core.
- Windows: discovery/paths stay platform-neutral (listener path injected, no
  hardcoded separators); probe uses net/http.
