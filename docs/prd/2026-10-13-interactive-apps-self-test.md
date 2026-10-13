# Interactive apps test themselves before delivery (2026-10-13)

Status: approved by the product owner (2026-10-13); landed: recorded in 2886020, built in bc5ad64.

## Problem

On QA an agent delivered a site with HTML and CSS and no script at all:
every view was hidden, so the page looked blank, and the agent still ended
its turn as done. The built-in `interactive-apps` skill told it to take a
cover screenshot with a headless browser, but nothing made it check that
the page works, and it could not have checked the data: `~app/sdk.js` does
not exist in its workspace, so every SDK call failed in a local test.

## Decision

- **The skill requires a self-test before the agent finishes** (and the
  `app-builder` expert's steps say so): load the page in a headless
  browser and check that (1) there is no console error and no uncaught
  exception, (2) the main view renders visible content, (3) one write and
  read through the SDK works, by doing the page's main action and seeing
  what it wrote shown back. A failed check is fixed and tested again, or
  reported; the agent never says the app is done or works when a check
  failed or could not run. A complete harness (a static server, the checks,
  the cover screenshot in the same run) is in the skill's reference file.
- **The SDK runs locally in demo mode.** bnbloc writes the app SDK in demo
  mode (`appsdk.DemoJS`: the one SDK file with its mode argument `"demo"`,
  every call answered in the page's memory, nothing sent) into the
  workspace's room records as `app-sdk/sdk.js`, rewritten only when it
  changes. The harness serves it as the page's `~app/sdk.js`; it is never
  published (it is under `.room/`).
- Budgets stay green (`TestAppBuiltinsAlwaysOnBudget`), the SDK drift test
  covers the reference file, and both built-ins' versions are bumped.

## Acceptance

- The skill states the three checks and the never-claim-done rule; the
  expert's steps test before explaining.
- A pull workspace holds `.room/app-sdk/sdk.js`, the demo-mode SDK, written
  once per workspace and again only when the SDK changes.
