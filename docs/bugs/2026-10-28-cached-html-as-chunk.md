# A deploy left a browser unable to open rooms until its site data was cleared (2026-10-28)

Status: fixed (see Fix).

## Repro

QA was deployed while a browser had bnbloc open (the E2E room's dedicated
profile). Afterwards every room failed with "bnbloc was updated… Reload to
continue" and `Failed to fetch dynamically imported module`; Reload, the
"New version — reload" notice and opening the room from the home page all
failed the same way. The server served every chunk of the live build
correctly (checked with curl).

## Root cause

Two rules that are each fine met during the rollout:

1. The gateway answers any unknown path with index.html (the SPA fallback),
   status 200, `text/html`, including a missing `/assets/*.js`. During a
   rolling deploy, the new index.html names chunks an old pod does not have;
   a request for one that lands on an old pod gets index.html.
2. The service worker caches `assets/*` cache-first (hashed names never
   change) and stored any `ok` same-origin answer, so it kept that HTML under
   the chunk's name. From then on the browser imported HTML as a module and
   failed, the worker answered from its cache before the network, and no
   reload could replace it.

The worker's profile cache on the E2E machine held index.html under eleven
chunk names of the live build (`Check.es-…js`, `Organization-…js`, …).

## Fix

- The gateway never answers a missing `/assets/*` with index.html: it
  answers 404 (`no-store`), so a missing chunk is an error, not a page.
- The worker caches an asset only when it is not HTML, and serves a cached
  asset only when it is not HTML (a copy stored before this fix is dropped
  and fetched again). `SHELL_VERSION` moves to `v2`, so activate deletes the
  old cache on every installed worker.
- Tests: `TestMissingAssetIsNotThePage` (gateway), the worker's
  asset-caching test (`web/scripts/pwa.test.mjs`).

## Why it was not caught

Lens 11 (release: old and new side by side during a rollout) and lens 8
(silent fallbacks): the SPA fallback answered every path, the worker trusted
every 200, and neither was tested against a mixed deploy. The E2E room, run
by an agent in a long-lived browser profile, met it on its first run after a
deploy.
