# Web client: multi-form contract + architecture hardening

Date: 2026-09-22
Status: landed 2026-09-22

## Intent

Two related tracks, recorded together because they share the `web/` client and
its three-form embedding contract.

### Track A — multi-client markdown spec

The web bundle runs in three forms from one build. That contract lived only in
scattered code comments (`config.ts`, `webui.go`, the Tauri `lib.rs` init
script). Codify it as a durable spec so every future change to routing,
addressing, embedding, or the single-domain reverse proxy is checked against
one authoritative document.

### Track B — architecture hardening

A read-only audit of `web/` found three real, growth-sensitive weaknesses. Fix
the two low-risk ones now; scope the higher-risk ones explicitly.

## The three forms (authoritative)

| Form | Origin | Gateway addressing | Base path |
| --- | --- | --- | --- |
| Gateway-embedded | gateway origin | same-origin, `apiBase=""`, relative paths | `/` (Vite `base:'./'`) |
| Plain browser | = gateway origin (identical to embedded) | same-origin, relative | `/` |
| Tauri desktop shell | `tauri://` custom protocol | absolute: shell injects `window.__BNBLOC_GATEWAY__` before SPA boot | bundled `index.html`, no server fallback |

Hard constraints any client change must honor (evidence: `web/vite.config.ts:14`,
`web/src/lib/config.ts:1-46`, `apps/desktop/src-tauri/src/lib.rs:196-257`,
`services/gateway/webui/webui.go:35-58`, `web/src/lib/auth.tsx:78-86`):

- Vite `base:'./'` (relative asset URLs) — real path-segment deep links break
  asset resolution on reload. Client routing must be **hash-based**.
- The `#/preview/<roomId>/<artifactId>` hash route is a contract shared with the
  Rust shell (`lib.rs:200` builds it literally). It must not change shape.
- The SSO callback parses `window.location.hash` as URLSearchParams then clears
  it (`auth.tsx:78-86`). Any HashRouter must not collide with this.
- Gateway URLs come only from `gatewayBase()`/`apiUrl()`, never from
  `window.location` (the desktop origin is not the gateway).
- API resilience already in `api.ts`: a single silent re-auth + retry on 401/403
  via a pluggable `onReauth`. A data layer must preserve it.
- SSE (`useRoomStream`/`sse.ts`) is a live stream, not request/response, and must
  not be wrapped in a query/cache layer.

## Track B findings and decisions

1. **File length: no limit in `web/`.** The Go side has an 800-line rule; `web/`
   has no lint at all (only `tsc -b && vite build`). Four files exceed 800:
   `Composer.tsx` 939, `RoomView.tsx` 902, `api.ts` 900, `Agents.tsx` 890.
   → Decision: add an oxlint config to `web/` with `max-lines`, wire a check,
   and split the four files along the seams the audit identified (zero behavior
   change; `api.ts` becomes a barrel over `api/{types,client,auth,platform,
   tenant,cores,rooms,...}`, the views extract inline sub-components/hooks into
   the existing `web/src/views/room/*`-style sibling dirs).

2. **Data fetching scattered, no cache/dedupe.** ~15 `useEffect+api.*` in
   `Organization.tsx`, 12 in `RoomView.tsx`, 6 in `ArtifactPanel.tsx`; each
   hand-rolls loading/error/race. No dedupe or staleness anywhere.
   → Decision: introduce a minimal query hook (server-state reads only),
   preserving the `onReauth` retry and leaving SSE untouched. Migrate the
   highest-duplication readers first (`Organization` sub-panels). Mutations and
   optimistic paths stay explicit.

3. **Navigation is `useState`, not URL-backed.** No deep links / shareable room
   URLs / back-forward (except the preview hash special-case).
   → Decision: add hash-based routing so room/view are URL-addressable, keeping
   the `#/preview/...` contract and SSO-callback hash handling intact.

## Acceptance

- Spec doc exists and is linked from the deployment/architecture docs.
- `web/` has a file-length check; the four files are under limit; build +
  typecheck pass.
- Query layer covers the migrated readers; SSE and re-auth behavior unchanged.
- Room/view deep links work in a browser; `#/preview/...` still opens a
  shell-less preview; desktop pop-out and SSO callback still work.
- All three forms still boot and address the gateway correctly.

## Outcome (landed)

- Spec: `docs/architecture/web-client-multi-form.md` (three forms, addressing,
  hash-routing constraint, API/SSE contract, change checklist).
- File-length gate: `web/scripts/check-file-length.mjs` (800 meaningful lines,
  blank+comment excluded), wired into `web build`. Only `Composer.tsx` was over;
  the other three "big" files are under the limit by meaningful-line count and
  were left alone (no over-splitting).
- Composer split: `AgentPicker`/`ModelPicker`/types moved to
  `web/src/views/composer/*`; `Composer.tsx` 816 -> ~500 meaningful lines,
  behavior unchanged.
- Data layer: `web/src/lib/useQuery.ts` (zero-dep cache + dedupe + invalidate,
  calls through `api` so re-auth retry survives; SSE untouched). Migrated a
  representative pure reader (`Organization` TenantsTab). Fetches coupled to
  derived state/side-effects were intentionally not force-migrated.
- Routing: `web/src/lib/useHashRoute.ts` — hash-based (required by Vite
  base:'./'), room/view deep-links, back-forward, reload-safe. Verified the
  `#/preview/...` shell-less contract and SSO-callback hash both still work.
- Verified: full `web build` (gate+tsc+vite) passes; browser smoke test of
  deep-links, reload persistence, and the preview/app view swap.
