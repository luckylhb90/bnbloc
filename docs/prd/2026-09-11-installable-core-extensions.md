# Design — Installable core provider extensions

Status: historical — C (documented SPI) and A (manifest-driven core) shipped
then; distribution is now install-only through the Components API
(`bnbloc cores install` / Platform → Components), with nothing loading from a
dropped-in file. B (sidecar) landed later as the `sidecar` package runtime.
See `docs/prd/2026-09-30-no-builtin-cores.md` and
`docs/prd/2026-10-01-components-workbench-redesign-design.md`.

## Problem

Today a new local-agent core (the daemon bnbloc drives over HTTP)
is added by writing a ~200-line Go `core.Provider` and adding a blank
import, then recompiling. The user wants users to be able to ADD core support by
installing an extension, not by rebuilding bnbloc.

## Hard constraint (why the obvious answer is wrong)

"Install an extension = load native code at runtime" is not viable here. Go's
`plugin` package is Linux/macOS-only, requires the plugin and host to be built
with the identical Go toolchain + dependency versions, and is unsupported on
Windows — and Windows is a repository compatibility contract. So a downloaded
`.so` that adds a core is out.

## Key observation

A `core.Provider` is a stateless HTTP translator: it maps 6 neutral verbs
(CreateWorkspace, CreateSession, SendInput, ListTargets, SessionIdle,
LatestSession) onto some daemon's REST API. A compiled REST-daemon provider (~200
lines) is entirely: `do(method, pathTemplate, bodyTemplate)` + extract fields
from the JSON response by path. Every method is the same skeleton. That means a
whole class of cores can be expressed as DATA (a manifest), not code.

## Three options

### A. Manifest-driven core (RECOMMENDED)
Compile in ONE new provider, `manifestcore.Provider`, that reads a declarative
manifest describing, per verb: the HTTP method, a path template, a request-body
template, and JSON-path response extractions, plus discovery (fixed-port probe
or listener-file) and small rules (idle predicate, empty-target fallback).
Adding a REST core = drop a manifest file; zero Go, zero recompile, cross-
platform (pure Go interpreter), safe (data, no code execution). Registers
through the SAME `core.Provider` registry, so gateway/bridge are unchanged.

- Pros: real "install an extension" with no rebuild; Windows-safe; no code-exec
  attack surface; reuses the multi-core machinery already shipped.
- Cons: only expresses cores whose control path is request/response templating.
  A core needing multi-step handshakes, streaming control, or exotic auth can't
  be a pure manifest.
- Covers: plain REST daemons (the common case). A core with listener-file
  discovery and richer shapes stayed native at the time (later expressed as
  manifest data).

### B. Sidecar core (escape hatch, later)
An extension is an executable (any language) implementing a tiny local
HTTP/stdio contract = the same 6 verbs. bnbloc launches/supervises it like the
existing bridge sidecar (`apps/desktop/src-tauri/src/bridge.rs`). Expresses
arbitrarily complex cores, any language, cross-platform.
- Cons: must define process lifecycle, discovery, package format, signing/trust,
  and a stable wire contract. Largest surface. Deferred until a real core needs
  more than a manifest.

### C. Documented internal SPI only (baseline, always do)
Keep the compile-time registry; write the durable "how to add a core" doc +
scaffold. New core = a provider + one blank import. Zero runtime risk.
- This is the floor. A and B build on top of it; the SPI doc is a prerequisite
  either way.

## Recommendation

Ship C (doc the SPI) as the foundation, then A (manifest core) as the actual
"installable extension" mechanism — it delivers no-rebuild core installation for
the REST-daemon common case with the least new surface, reusing the multi-core
config + discovery already built. Keep B as a documented escape hatch to add
only when a concrete core exceeds manifest expressiveness. This avoids the Go
plugin trap and the premature cost of a full sidecar protocol + catalog.

## Manifest schema (draft, option A)

```jsonc
{
  "name": "myrest",                       // core name (registry key, target prefix)
  "discover": {                            // optional; else config-only
    "probeUrl": "http://host.docker.internal:8088",
    "probePath": "/v1/info"               // 200 => found; token from config
  },
  "createWorkspace": {
    "method": "POST", "path": "/v1/projects",
    "body": { "name": "bnbloc-{shortid}" },
    "extract": { "workspaceId": "id" }     // JSON path in response
  },
  "createSession": {
    "method": "POST", "path": "/v1/sessions",
    "body": { "agent_id": "{targetId|firstTarget}", "project_id": "{workspaceId}",
              "prompt": "{prompt}" },
    "extract": { "sessionId": "id", "turnId": "active_response_id",
                 "phase": "status" }
  },
  "sendInput": { "method": "POST", "path": "/v1/sessions/{sessionId}/events",
                 "body": { "text": "{text}" } },
  "listTargets": { "method": "GET", "path": "/v1/agents",
                   "targets": { "list": "data", "id": "id", "provider": "harness" } },
  "sessionIdle": { "method": "GET", "path": "/v1/sessions/{sessionId}",
                   "idleWhen": { "field": "status", "notIn": ["running"] } },
  "latestSession": { "method": "GET", "path": "/v1/sessions",
                     "targets": { "list": "data", "id": "id" }, "pick": "first" }
}
```

Template vars: `{workspaceId} {sessionId} {targetId} {prompt} {text} {shortid}`
and the `{targetId|firstTarget}` fallback form. Extraction is dotted JSON path
(support `a.b`, `a[].c` for lists). Anything the schema can't express => the core
is a native provider or a sidecar (option B), not a manifest.

## Install entry (chosen: desktop path/URL entry + drop-in dir)

- Manifests load from a scanned directory (e.g. `<state>/cores/*.json`) at
  startup AND from paths added in Desktop settings (BridgePanel already lists
  cores; extend a core row to optionally point at a manifest path/URL).
- `manifestcore` registers each loaded manifest under its `name` via the
  existing `core.Register`, so `BNBLOC_CORES` and the aggregated targets treat
  it identically to a built-in core.

## Robustness (needed for user-entered cores)

- coreset.FromEnv currently FAILS FAST on an unknown core name
  (`internal/coreset/coreset.go:58-61`), which would crash-loop the bridge if a
  user types a not-yet-loaded name. Change to: skip unknown cores with a loud
  warning, start with the known ones.
- Expose `GET /v1/cores` (core.Names() + which resolved an endpoint) so the
  desktop UI validates the name field instead of free text.

## Non-goals (for the first cut)

- Native code plugins (Go plugin) — permanently out (Windows).
- CDN catalog + signing (option-C-of-install) — later; drop-in + desktop entry
  first.
- Sidecar protocol (option B) — documented escape hatch, built only on demand.

## Verification plan

- manifestcore unit tests: a fake REST daemon (httptest) + a manifest reproduce
  a compiled REST provider's 6 verbs byte-for-byte (same method/path/body, correct extraction).
- discovery: manifest probe found/not-found.
- coreset: unknown core is skipped (not fatal); known cores still load.
- gateway aggregation already shows manifest cores' targets core-labeled.
- Windows: pure Go templating + net/http; manifest paths use filepath; no
  plugin, no shell.
