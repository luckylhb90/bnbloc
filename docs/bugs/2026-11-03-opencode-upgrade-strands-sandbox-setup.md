# An opencode upgrade stranded every sandboxed opencode session (2026-11-03)

Status: fixed in bnbloc-acp (see Fix); the general cause is open (see "The
class it belongs to").

## Repro

1. A member runs opencode through the `acp` core with the agent sandbox on.
   The core prepared its opencode routing config directory
   (`<acp home>/sandbox/opencode-<hash of the vmexec path>`) while opencode
   1.18.35 was installed.
2. The member upgrades opencode to 1.19.5.
3. Every sandboxed opencode session fails before it starts:
   `runner_start_failed ... core_unavailable: opencode: prepare the opencode
   routing config (opencode models): <nil>: <the last model opencode
   listed>`. `bnbloc smoke --agent acp/opencode` on QA showed it.

## Root cause

The core trusts the directory only when its `package.json` pins the plugin
SDK to the installed opencode's version and that version is installed
(`opencodeConfigWarm`). On a mismatch it runs a confined `opencode models` to
set the directory up again. opencode writes `package.json` only when there is
none: it kept the 1.18.35 pin, the check failed again, and every session
failed the same way, with the last line of the model list as its "reason".
Checked by hand: an empty directory, or one without `package.json`,
`package-lock.json` and `node_modules`, is set up for 1.19.5; the old one as
it was stays at 1.18.35.

## Fix

bnbloc-acp: before a warm-up, the core removes opencode's own setup files
from its directory (`resetOpencodeSetup`: `package.json`,
`package-lock.json`, `node_modules`), keeping the routing plugin and the npm
cache. `TestOpencodeWarmAfterAnUpgrade` sets the directory up with a fake
opencode 1.18.35, then warms it with 1.19.5; with the reset removed it fails
with the error the member saw.

## Why it was not caught

Lens 11 (release and compatibility: "the upgrade itself, with existing
state, before and after"). The warm-up was tested from an empty directory
only, and every test stubbed it (`warmOpencodeFn`). Nobody asked what
happens to state the core prepared when the program it was prepared for
changes underneath it, without the core changing. Added to
`docs/design-review.md` lens 11.

## The class it belongs to

The core prepares state for things it does not own (an agent's version, an
npm package, the sandbox helper, an agent's config), and each piece has its
own idea of when it is still valid. The owner asked (2026-11-03) for one
mechanism instead of a fix per case; its inventory and design are in
`docs/prd/2026-11-03-prepared-state-self-check.md`.
