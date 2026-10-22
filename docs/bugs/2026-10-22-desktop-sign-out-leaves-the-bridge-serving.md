# Desktop sign-out leaves the bridge serving the signed-out member (2026-10-22)

Status: fixed in code (2026-10-22), found by reading the code while designing
several accounts (`docs/prd/2026-10-22-multi-account.md`). Not reproduced on a
running desktop app (not run here); the two causes below are in the code and
each has a test.

## What the code does

Web `logout()` clears the web session and revokes the web's own refresh family
(`web/src/lib/auth.tsx:179-195`). It never stops the bridge or clears the
owner, tenant and gateway the shell keeps in `settings.json`
(`apps/desktop/src-tauri/src/bridge.rs`, `settings.rs`). The bridge holds its
own forked refresh family (`services/bridge/bridge.go:2917-2983`), which the
web logout does not revoke (`services/gateway/session.go:440-460`), so it
keeps claiming the signed-out member's jobs and driving their agents until it
is restarted. The empty adapter session restarts the member-scoped sidecars
and, a few seconds later, the bridge without a state dir or a refresh token;
on a gateway that offers dev login, the bridge then signs in again as the
stored owner.

## Fix

- Gateway (root cause): `handleLogout` revoked only the presented family;
  `handleRevokeMySession` already revoked the family and the sessions forked
  from it. Both now end a sign-in through one function, `revokeFamilyAndForks`
  (`services/gateway/session.go`), so a logout also ends the bridge's fork.
  Test: `TestLogoutRevokesTheForkedBridgeToo` (fails with the forks left,
  another sign-in's bridge untouched).
- Shell: the web's sign-out (an empty session handed to
  `internal_adapter_session`) now stops the bridge and forgets the owner and
  tenant it ran for (`Settings::forget_member`; test
  `signing_out_forgets_the_member_and_nothing_else`). A session bearer the
  bridge already holds lives at most one session TTL, so stopping it at
  once matters even with the fork revoked.

## Still to confirm on a desktop app

Sign in, send an agent message, sign out, send a message to that member's agent
from another account in a shared room: the first account's bridge must not run
the turn.

## Why it was not caught

Lens 2 (time and lifecycle): the question "what is still running as this
identity after it ends, and who stops it?" was never asked of the bridge's
forked session. Added to `docs/design-review.md` lens 2 with this fix.
