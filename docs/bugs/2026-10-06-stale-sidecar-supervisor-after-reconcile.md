# Reconcile leaves stale sidecar supervisor alive after artifact loss

Date: 2026-10-06
Status: fixed and verified (2026-10-07)

## Reproduction

1. Start the desktop bridge with a runnable installed sidecar and wait until its supervisor is claimed.
2. Reconcile the same discovered component after its selected-platform artifact is unavailable, so it remains discovered but produces no runnable `SidecarSpec`.
3. Observe that reconciliation clears `spec` and process state while the sidecar generation remains unchanged; an in-flight supervisor may still pass its generation check and launch or restart the prior binary.

## Root cause

`apply_specs_to_inner` invalidates a sidecar's generation when the component is removed or replaced, but the discovered-yet-not-runnable branch clears `spec` and child state without incrementing `generation`. `supervise` and `spawn_sidecar` use that generation as the cancellation boundary.

## Fix
Increment the sidecar generation before clearing an unrunnable discovered component, invalidating any in-flight supervisor before clearing its spec and process state.

## Verification

The regression test now seeds generation 4 with an active supervisor and expects generation 5 after artifact-loss reconciliation. Three focused `cargo test` attempts did not finish: two timed out while compiling Tauri dependencies (1200s each), and an earlier `--features custom-protocol` attempt failed because the extension's optional `internal` dependencies were not enabled. The pre-fix source path leaves generation unchanged; the assertion is intended to fail before the fix. Web typecheck/build and web tests do not exercise this Rust race.


Verified 2026-10-07: `cargo test --lib internal_ext` passes 18/18 (including `reconcile_specs_clears_process_state_when_active_component_loses_artifact`, generation 4 → 5) and the full desktop lib suite passes. The earlier `--features custom-protocol` failure came from a mis-merged `Cargo.toml` that made the extension dependencies optional; see `2026-10-07-internal-merge-omissions.md`.