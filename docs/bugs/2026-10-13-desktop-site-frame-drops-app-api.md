# The desktop's site frame drops an app's writes and change feed (2026-10-13)

Status: **landed** (2026-10-13, with interactive apps step 6). Found while
landing interactive apps step 5 (the web); the fix is the desktop shell's.

## Symptom

An interactive app (a published site using `~app/sdk.js`) opened from its
deliverable card in the **desktop app** loads and reads its data, but every
write (`insert`, `put`, `patch`, `remove`) fails, and `subscribe` never
delivers a change. The same card in a **browser** works.

## Root cause

In the browser, `SiteViewer` frames the gateway's member site URL
(`/v1/sites/<ticket>/`), and the SDK's relative `~app/` calls reach the
gateway directly (the frame entries answer CORS for any origin).

In the desktop shell, `siteFrameUrl` routes a member site through the
`bnbloc-site` URI scheme (`apps/desktop/src-tauri/src/site_scheme.rs`), so
the app CSP's `frame-src` never has to admit the gateway origin. The scheme
was built for static site files:

- it forwards only `GET` and `HEAD` (anything else is 405), so app writes
  and their CORS preflights are refused;
- it forwards no request header (no `Content-Type`, `If-Match`,
  `If-None-Match`, `Accept`), so conditional writes would lose their
  condition even once allowed;
- it buffers the whole response before answering (a custom scheme
  responder takes one body), so the change feed, an endless SSE stream,
  never answers and holds a proxy task until the read times out.

Reads (`find`, `get`, `me`) work: they are plain GETs.

## Fix (proposed)

Decide in the desktop shell, with step 6 (`BNBLOC_SITES_ORIGIN`) in view:

- frame an app from the gateway's (or the sites origin's) own URL and admit
  that origin in `frame-src`, so the SDK reaches the gateway directly as in
  the browser; or
- let the scheme forward the app API (`/v1/sites/<ticket>/~app/…`: every
  method, the four request headers, bounded bodies) and answer the change
  feed's absence clearly (the SDK reconnects with backoff; the page then
  relies on re-reads).

The first keeps one path for browser and desktop and is the likely choice
once sites have their own origin.

## Fix (landed)

The second, made whole: the scheme stays the desktop's one path, with or
without a sites origin, so the app CSP still admits only the scheme in
`frame-src` and no gateway or sites origin has to be written into the
shell's static CSP (Tauri's CSP is fixed at build time; the gateway and its
sites origin are runtime settings).

- **Every method of the app API.** A path under `~app/` takes GET, HEAD,
  POST, PUT, PATCH, DELETE and OPTIONS (a site file still only GET and
  HEAD), with its body (at most `wire.MaxAppRequestBytes`, else 413) and
  the request headers the API reads: `Content-Type`, `If-Match`,
  `If-None-Match`, `Accept`, `X-Bnbloc-Visitor`, and a preflight's
  `Access-Control-Request-*`. Never a cookie or `Authorization`. The CORS
  answer headers pass back.
- **The change feed.** A Tauri scheme responder takes one body, so a
  stream cannot pass through it. The shell holds the feed's upstream SSE
  stream itself (one per feed URL and visitor) and answers each
  `GET ~app/events` with the events that arrived since the previous
  request, waiting up to 20 s for one (an empty batch is a keep-alive). The
  SDK reads the batch, sees the end and reconnects 1 s later; the held
  stream keeps every event meanwhile, so nothing is lost while the page
  polls. A feed nobody polls for 45 s is closed; a refusal (a dead ticket)
  is answered as the gateway answered it.
- **The sites origin.** The scheme forwards to wherever the gateway serves
  sites: `GET /v1/public/client-config` names the sites origin
  (`BNBLOC_SITES_ORIGIN`) when there is one, else the gateway itself
  (cached 5 minutes per gateway). The web hands the scheme the site URL's
  path (`siteFrameUrl`), whatever origin the URL is on.

Tests: `apps/desktop/src-tauri/src/site_scheme_tests.rs` (writes with
their body and conditions, the preflight, the held feed delivering every
event across polls over one upstream stream, a refused and an unpolled
feed, the sites origin rule).

## Landing

Landed 2026-10-13 with interactive apps delivery step 6
(`docs/prd/2026-10-12-interactive-apps.md`).
