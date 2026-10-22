# Desktop sign-out leaves the bridge serving the signed-out member (2026-10-22)

Status: open, found by reading the code while designing several accounts
(`docs/prd/2026-10-22-multi-account.md`); not reproduced yet. Fix: slice 2 of
that record.

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

## To confirm (repro)

Sign in on the desktop app, send an agent message, sign out, send a message
to that member's agent from another account in a shared room: does the first
account's bridge still run the turn?

## Fix

Sign-out stops the bridge, clears its owner, tenant and gateway token from the
settings slot, and revokes the bridge's forked family; covered by a shell test
and a bridge test.

## Why it was not caught

Lens 2 (time and lifecycle: what happens to the long-running thing bound to
an identity when the identity ends). The question to add to
`docs/design-review.md` with the fix: "What is still running as this identity
after sign-out or a switch, and who stops it?"
