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

## Share links and interactive apps

- `#/share/<token>` (`web/src/main.tsx`, inside `AuthProvider`) renders
  before sign-in. A link anyone can open is read through the share routes
  alone (`publicShareApi`, `lib/sharesApi.ts`), without a session. A link
  for the room's organization (the default,
  `docs/prd/2026-10-13-share-link-sign-in.md`) answers the anonymous read
  401: without a session the page remembers itself (`lib/loginReturn.ts`,
  the one login return path, web boundary rule A2: sessionStorage, 15
  minutes, a share hash only) and sends the person to the app's sign-in;
  `App` takes the return once signed in, whichever way, and goes back.
  With a session the page mints a share ticket (`sharesApi.ticket`, the
  only call carrying the session) and opens the link with the ticket in
  the token's place; a 403 is "This link is only for members of <org>".
- The share dialog (`views/workbench/ShareDialog.tsx`) picks who may open
  a link (`shareAccessOptions`, default `DEFAULT_SHARE_ACCESS` = `org`;
  "anyone" is unavailable while the organization disables such links) and
  lists each link's access, which its creator can change.
- A link to a web app (the share answers a `siteUrl`) is the **app view**
  (`views/share/SharedApp.tsx`): the site frame full-screen inside the
  safe areas (`index.html`'s `viewport-fit=cover`), no file-viewer header,
  version line or download, and no page scroll. The frame keeps the site
  sandbox (`SITE_SANDBOX`, never `allow-same-origin`). Any other file keeps
  the file page (`SharedFile`).
- On a link anyone can open, the app view holds the link's **visitor
  token** for the app's data API (on an org link the frame's URL carries
  the member's share ticket, which makes them the participant, and no
  visitor token exists):
  `lib/appVisitor.ts` is its one owner (web boundary rule A1). It mints the
  token on the frame's first handshake request
  (`POST /v1/public/shares/{token}/visitor`), keeps it per share in
  `localStorage` (`StorageKey.appVisitor` plus a hash of the share token,
  never the token itself), renews it an hour before it expires (same
  visitor) and pushes the renewed grant to the frame. It answers only a
  request whose `event.source` is its own frame's window; a repeated
  request for the token the frame already holds renews it first (the
  gateway refused it, e.g. after a signing-key change).
- A member's site viewer (`SiteViewer`, `lib/siteFrame.ts`) answers no
  visitor request: the member's ticket is the capability, and the SDK
  only waits for a visitor token after a refusal a ticket never gets. In
  the desktop shell the `bnbloc-site` scheme carries the site's path and
  forwards every app API method and its change feed
  (`apps/desktop/src-tauri/src/site_scheme.rs`).
- **Where a site loads from is the gateway's answer.** `siteUrl` is a path
  on the gateway, or an absolute URL on the deployment's sites origin
  (`BNBLOC_SITES_ORIGIN`); `siteGatewayUrl` uses an absolute one as is and
  the web never picks a host. The web client sets no `frame-src` of its own
  (the gateway serves it with `frame-ancestors 'self'` only), so a sites
  origin needs no web configuration.
- An app's owners see its **data line** on the deliverable card and in the
  artifact viewer (`views/workbench/AppDataLine.tsx`, rules in
  `lib/appData.ts`): the count per collection from the owners' stats route
  (a 403 there means "not an owner": no line), Export (JSON of every
  collection, CSV of one: the server's audited export route, streamed; the
  web only names the file) and Clear data behind `ConfirmDialog`. The room
  stream's `app_changed` refreshes the counts.

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
