# Browser "new version available" reload notice

Status: landed

## Intent

A browser tab opened before a deploy keeps running the old bundle, which then
calls routes that no longer exist (observed: the removed tenant webhook tab
answering "404 page not found" after the notifier cutover). The desktop app
has its own updater; the browser had nothing.

## Behaviour

- Browser only (the desktop shell embeds its bundle and uses the updater).
- The page knows its own entry script (`assets/index-<hash>.js`, a content
  hash). While visible it re-fetches the gateway's `index.html` (served
  `no-store`) every 5 minutes and on focus, and compares entry scripts. No
  version endpoint and no hardcoded version: the deployed `index.html` is the
  authority.
- A difference shows a quiet sidebar footer entry "New version — reload"
  (same slot as the desktop update notice). Clicking reloads. Nothing reloads
  on its own (a reload would drop a draft).

## Acceptance

- Unit test for entry-script extraction from built `index.html`.
- No notice in dev (no hashed entry) or in the desktop shell.
