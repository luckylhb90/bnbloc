# Sync engine tests failed intermittently at temp-dir cleanup

Status: fixed

## Repro

`go test ./services/bridge/sync` occasionally failed with
`TempDir RemoveAll cleanup: unlinkat …: directory not empty`
(seen in `TestSyncConflictPreservesBothVersions` during a full CI run; 20
isolated runs passed).

## Root cause

The tests started engines with `go e.Start(ctx, cfg)` and, on shutdown, only
cancelled the context and slept (80–300 ms) before the next phase or before
`t.TempDir` cleanup. `goSync.Start`'s deferred exit saves the sync index into
`cfg.Dir`, so under load the save could still be writing while the temp dir
was being removed (or while the next phase's engine started on the same dir).
Sleep-based "settle" waits were the defect, not the engine.

## Fix

`runSync` (test helper) runs Start under a child context, returns a channel
closed when Start has returned, and registers a cleanup that stops the engine
and waits for it before the temp dirs are removed; phase transitions wait on
those channels (`waitStopped`) instead of sleeping. All sleep-based shutdown
waits in the sync tests were replaced. Verified: 30 consecutive full-package
runs pass.
