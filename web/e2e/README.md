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

## Mocked gateways

Most scripts answer `/v1/*` in the browser instead of a backend. A mock
answers with the server's whole shape (`fixtures.mjs` builds rooms and
channels as `internal/wire` sends them): a field the client reads and the
mock leaves out crashes the view, and every check after it fails for the
wrong reason. `CHROME` names a local Chrome when playwright's own browser is
not installed:

```sh
CHROME="/Applications/Google Chrome.app/Contents/MacOS/Google Chrome" BNBLOC_E2E_URL=http://localhost:5173 node web/e2e/long-message.mjs
```

## Multi-account (`multi-account.mjs`)

The accounts of one app, in a real browser against a mocked gateway (the
browser answers `/v1/*` itself, so no backend is needed): the vault, a window
bound to one account, a second window for another, switching, removing,
a refused credential, the renewal lock across windows, the last view, and
signing in. Run it the same way, with a web server on `BNBLOC_E2E_URL`:

```sh
BNBLOC_E2E_URL=http://localhost:5173 node web/e2e/multi-account.mjs
```

## Suggestions in the composer (`suggest-use.mjs`)

The composer's "fits this request" row, in a real browser against a mocked
gateway: an expert is suggested and picked in one click, a request nothing
fits gets no row, a dismissal lasts across a reload, a skill pack is turned on
for the room and, when the room refuses, for the member's agents; the decision
model places a request in a few calls per draft (and its silence stands), and the
keyword matcher does without it, on a rate limit or when the member turned it off.

```sh
BNBLOC_E2E_URL=http://localhost:5173 node web/e2e/suggest-use.mjs
```

## A draft survives the room opening (`composer-draft.mjs`)

What is typed before a room's channel list arrives stays in the composer when it
does (the default channel resolving the first time is not another conversation,
`lib/composerKey.ts`), and another channel still gets its own, empty one.

```sh
BNBLOC_E2E_URL=http://localhost:5173 node web/e2e/composer-draft.mjs
```

## Channels set up and used as a member does (`channels.mjs`)

Channels slice 1 (docs/prd/2026-10-28-channels-and-threads.md) against a
mocked room that refuses what the room service refuses: the welcome page on a
first open, the rail grouped by section, the Rules sheet, a channel's settings
saving its default agent by key, the directory's search and join, archiving,
the rules agreement before a first post (the message is sent once agreed),
an admins-only channel's disabled composer, slow mode's wait, and a refused
menu action said on its row.

```sh
BNBLOC_E2E_URL=http://localhost:5173 node web/e2e/channels.mjs
```

## An agent's offer of a link (`share-proposal.mjs`)

The card an agent puts in the room (`room_setup` `share`) is seen only by the
person it was for, shows what is offered and that nothing is shared yet, creates
the default link on a click (the organization's members, a week), shows it with
Copy and opens the Share dialog from More options.

```sh
BNBLOC_E2E_URL=http://localhost:5173 node web/e2e/share-proposal.mjs
```
