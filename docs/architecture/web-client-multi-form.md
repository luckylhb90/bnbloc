# Web Client Multi-Form Contract

The `web/` React SPA ships as **one build** that runs in three forms. This
document is the authoritative contract for how the client addresses the
backend, how it is embedded, and what any change to routing, addressing, or
packaging must preserve. It is distilled from the code that implements each
form; every rule cites its source.

## The three forms

| Form | Loads from | Gateway addressing | Server SPA fallback |
| --- | --- | --- | --- |
| **Gateway-embedded** | the gateway's own origin (`//go:embed` of the built SPA) | same-origin: `apiBase = ""`, relative paths | yes — gateway rewrites unknown paths to `index.html` |
| **Plain browser** | a deployed gateway origin (identical case to embedded) | same-origin, relative | yes (same gateway) |
| **Tauri desktop shell** | bundled `index.html` over the `tauri://` custom protocol | absolute: the shell injects `window.__BNBLOC_GATEWAY__` before the SPA boots | no — the custom protocol serves bundled files only |

Sources: `web/vite.config.ts:6-14`, `services/gateway/webui/webui.go:1-58`,
`web/src/lib/config.ts:1-56`, `apps/desktop/src-tauri/src/lib.rs:196-257`.

## Addressing rules (do not break these)

1. **Gateway URLs come only from `gatewayBase()` / `apiUrl()`**
   (`web/src/lib/config.ts:30-46`). Never build an API URL from
   `window.location` — in the desktop form the page origin is `tauri://`, not
   the gateway.

2. **`apiBase` is read once at module init** from `window.__BNBLOC_GATEWAY__`
   (`config.ts:26-28`). The desktop shell injects that global via an
   initialization script that runs *before* any SPA script
   (`lib.rs:246-256`). Do not move the read earlier than the shell's injection,
   and do not assume it is set in the browser forms (there it is empty →
   same-origin).

3. **Same-origin forms use relative paths, no CORS.** The gateway serves the
   SPA and the API from one origin; `apiUrl("/v1/x")` returns `/v1/x`
   (`config.ts:44-46`). A standalone web tier behind a single domain preserves
   this by reverse-proxying `/v1`, `/api`, `/sso` to the gateway (see
   [Production Deployment](../production-deployment.md)).

4. **The relay is not the gateway.** File-sync reaches the relay under the
   top-level `/relay/` prefix, proxied directly (never through the gateway's
   auth). The desktop bridge dials it via `BNBLOC_RELAY_BASE_URL`. See the
   deployment guide's reverse-proxy section; the relay entry **must be TLS**
   because the sync token rides the stream in cleartext.

## Base path and asset resolution

- **Vite `base: './'`** (`vite.config.ts:14`) — asset URLs are *relative*, so
  the same bundle works whether served from the gateway root or a bundled
  `index.html`. This is a hard constraint: a real path-segment URL such as
  `/room/abc` would make `./assets/...` resolve against `/room/`, breaking asset
  loads on reload.
- **Consequence: client routing MUST be hash-based.** A hash (`#/room/abc`)
  keeps the document path at root, so relative assets always resolve. Do not
  introduce `BrowserRouter` or real path routes.

## Routing contract

- The **only** cross-window route is the preview hash:
  `index.html#/preview/<roomId>/<artifactId>`. The Rust shell builds this
  literal string when popping out a preview window (`lib.rs:200`), and the web
  parses it (`web/src/views/StandalonePreview.tsx`). **This shape is a
  cross-language contract** — changing it breaks the shell.
- The **SSO callback** parses `window.location.hash` as URL-encoded params
  (`sessionToken`, `subject`) then clears it (`web/src/lib/auth.tsx:78-86`). Any
  hash router must not consume or collide with the SSO-callback hash; route
  parsing runs after the callback is handled and cleared.
- **Preview windows boot fresh** and depend on a session handoff seeded before
  mount (`web/src/main.tsx`, `web/src/lib/desktop.ts` `seedPreviewSession`).
  Routing must run after that seed.

## API client contract

- `web/src/lib/api.ts` exposes a single `api` object of promise-returning
  methods over a shared `req<T>` helper. Auth is a module-level bearer token
  (`setToken`) plus an `X-Bnbloc-Acting-Tenant` header for super-admins.
- **Built-in resilience to preserve:** a single silent re-auth + retry on
  401/403 via a pluggable `onReauth` (`api.ts` `setReauthHandler`). Any caching
  or query layer added on top must call through `api`/`req`, not replace it, so
  this retry survives.
- **SSE is separate.** `web/src/lib/useRoomStream.ts` + `sse.ts` are a live
  event stream with their own reconnect/backoff/re-auth loop. They are **not**
  request/response and must never be wrapped in a request cache.

## Change checklist

Before merging a change that touches routing, addressing, packaging, or the
reverse proxy, confirm:

- [ ] Gateway URLs still flow only through `gatewayBase()`/`apiUrl()`.
- [ ] All routing is hash-based; no `base: '/'`, no `BrowserRouter`.
- [ ] `#/preview/<room>/<artifact>` is unchanged (shell contract).
- [ ] SSO-callback hash handling is intact.
- [ ] SSE is untouched by any request/cache layer.
- [ ] All three forms boot and reach the gateway (browser same-origin; desktop
      via injected `__BNBLOC_GATEWAY__`).
