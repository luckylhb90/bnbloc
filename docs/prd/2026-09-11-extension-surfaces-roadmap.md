# Roadmap — Two extension surfaces

Status: **historical roadmap** — Surface 1 evolved into signed, installable Components packages
(`docs/prd/2026-10-01-components-workbench-redesign-design.md`); Surface 2's event-hook slice landed as typed
plugin sidecar packages; UI contribution points remain unbuilt.

bnbloc has TWO distinct, orthogonal extension surfaces. They are NOT two
designs for one thing; conflating them produces wrong architecture.

## Surface 1 — Local-agent aggregation layer (BUILT)

- What it extends: which agent BACKENDS bnbloc can drive.
- Analogy: a data-source adapter / language server, not a product plugin.
- Mechanism: `core.Provider` (6 outbound verbs) + registry; tier-1 JSON manifest
  (`core/manifestcore`) for REST daemons + runtime install (`POST
  /v1/cores/install`); tier-2 (design, not built) = pure-Go WASM via wazero for
  cores the manifest can't express (multi-step auth, HMAC, streaming control).
  See `docs/prd/2026-09-11-installable-core-extensions.md` and
  `docs/architecture/core-providers.md`.
- Status: shipped, but usability is in question (see below). Priority is making
  Surface 1 genuinely usable before anything else.

## Surface 2 — Product plugin platform (NOT BUILT; roadmap)

- What it extends: what the bnbloc collaboration space can DO — nav pages, room
  side panels, custom message renderers, composer actions, event hooks.
- Analogy: VS Code extensions (contribution points + activation + extension API).
- Feasibility (audited): every product-extensibility primitive is ABSENT today.
  Frontend is a closed SPA — `Shell.tsx` hardcodes `type View`, `App.tsx`
  hardcodes the view ternary, no contribution/slot registry, no client event
  bus, no command registry, no scoped plugin API/tokens. Backend has a partial
  event bus (`internal/wire.Event` + realtime hub) but no webhook/plugin hooks.
  Verdict: this is a BUILD-FROM-SCRATCH platform, not an extension of existing
  scaffolding.
- Reusable blueprint: the Surface-1 lifecycle (declarative manifest -> disk ->
  thread-safe registry -> live reload -> management UI) is a good template for a
  future plugin registry, but its schema is REST-verb-specific and cannot express
  UI/product extensibility.
- Smallest credible first slice WHEN pursued: invert `Shell.tsx`/`App.tsx` to a
  frontend contribution-point registry (nav item + view), reuse the cores
  install pattern for `POST /v1/plugins/install`, sandbox contributed views
  (iframe/ESM + postMessage bridge). Later slices: room side-panel slots,
  message-renderer registry, scoped gateway tokens + client pub-sub bus.
- Hard open decision before building: how third-party UI code runs safely
  (iframe sandbox vs ESM vs micro-frontend) — each has deep security/UX
  consequences. Do not start until product intent + the sandbox model are fixed.

## Current priority

Surface 1 usability first. Surface 2 stays roadmap until Surface 1 is genuinely
usable and there is a concrete product need with a decided sandbox model.
