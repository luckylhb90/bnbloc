# Layout regression (e2e)

`layout-regression.mjs` asserts the room layout's geometry across five viewports
in a real browser, so fixing one layout defect can't silently reopen another.
It is the guard against the "fix one, break another" churn this layout went
through: the shell dead-gap, the rail gap, and the wide-window feed dead-space
are each pinned here.

It checks, per viewport (375 / 744 / 1200 / 1440 / 1920):

- the stage resolves correctly (compact / medium / wide)
- no dead gap left of content (shell sidebar → main)
- no gap between the channel rail and the chat column
- no dead space right of the message feed (the wide-window regression)
- no horizontal page overflow
- under a coarse pointer, zero interactive controls below 44px

## Running it

It is intentionally NOT wired into the app's build or dependencies — it uses
`playwright-core` from a scratch install, so it never changes `web/package.json`
or the shipped bundle. Run it against a live stack:

```sh
# 1. bring up the backend (make up) and a web server:
cd web && npx vite --port 5173 --strictPort
# 2. in a scratch dir, once:
npm i playwright-core
# 3. point it at the web server and run:
BNBLOC_E2E_URL=http://localhost:5173/ node /path/to/web/e2e/layout-regression.mjs
```

It signs in as `maya`, creates its own fixture room via the API, enters it by
clicking the Plaza card (rooms are React state, not a URL route — the harness
polls for `[data-room-row]` after the click rather than assuming it landed),
and exits non-zero with a per-assertion failure list if any invariant breaks.

The test creates a plain room and message only; it depends on no specific agent
core, so it runs identically on the public and internal builds.

## Multi-account (`multi-account.mjs`)

The accounts of one app, in a real browser against a mocked gateway (the
browser answers `/v1/*` itself, so no backend is needed): the vault, a window
bound to one account, a second window for another, switching, removing,
a refused credential, the renewal lock across windows, the last view, and
signing in. Run it the same way, with a web server on `BNBLOC_E2E_URL`:

```sh
BNBLOC_E2E_URL=http://localhost:5173 node web/e2e/multi-account.mjs
```
