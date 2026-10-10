# Bug: presence roster churned join/leave

Status: done
Commits: 940ca3c

## Repro

Watch a room's roster while a client reconnects (or the bridge is subscribed):
members rapidly flip join/leave (observed jack join/leave many times per
second).

## Root cause

Two issues:

1. Presence was pure refcount with no grace: a dropped SSE connection immediately
   announced a leave, then the reconnect announced a rejoin — churn on any flaky
   connection.
2. The bridge subscribes to the room SSE stream to drive agents and was counted
   as a present human, so its own reconnects churned the roster too.

## Fix

- Defer a leave by a short grace window (5s); a re-`Enter` within it cancels the
  pending leave and does not re-announce a join. Injectable timer for
  deterministic tests.
- Non-presence subscribers pass `?presence=0`; realtime skips `Presence`
  Enter/Leave for them, and the bridge subscribes with it — driving agents no
  longer inflates or churns presence.

## Verification

Unit tests: deferred finalize, reconnect cancels leave without re-join, refcount
defers only on the last reference.
