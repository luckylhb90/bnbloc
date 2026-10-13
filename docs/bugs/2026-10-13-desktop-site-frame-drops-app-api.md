# The desktop's site frame drops an app's writes and change feed (2026-10-13)

Status: **open**. Found while landing interactive apps step 5 (the web);
the fix belongs to the desktop shell and is not part of that step.

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

## Landing

Not landed.
