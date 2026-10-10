# Administrator Local Desktop Release Publishing Implementation Plan

Status: implemented (merged in 1be3204); task checkboxes below were not maintained.


**Goal:** Let a signed-in super-admin select a local Tauri updater archive and matching `.sig`, upload it to the current gateway's existing artifact endpoint, and publish the same artifact URL to Release Hub and stable auto-update policy.

**Architecture:** Reuse the current streaming super-admin artifact API and share its file-upload/auth-retry helper with the existing sidecar publisher. The Platform → Updates screen composes selected archive + signature with a fresh update config and existing same-version Release Hub row; it uploads, upserts the stable hub entry, and saves updater config last as the go-live action. Policy enables stable automatic checks but preserves auto-install, cadence, minimum-version, and all other target settings.

**Tech Stack:** React/TypeScript, existing gateway `POST /v1/platform/artifacts`, Tauri updater config API, Release Hub API, Node built-in test runner.

## Task 1 — Pure release mapping regression

- [ ] Add `web/scripts/admin-release.test.mjs` covering: output URL is the returned artifact URL; archive is mapped to `darwin-aarch64` and `macos/aarch64`; same-version existing Windows artifacts/notes survive while the previous macOS target row is replaced; enabled+stable+autoCheck are set; autoInstall, cadence, minimum version and other targets remain unchanged.
- [ ] Run the test and observe a named assertion failure because `prepareDesktopRelease` is not implemented.
- [ ] Implement `prepareDesktopRelease` in `web/src/lib/desktopRelease.ts`; reject empty version, non-`.app.tar.gz` archive, empty `.sig`, malformed digest, and invalid RFC3339 date.
- [ ] Re-run the test; all cases pass.

## Task 2 — Shared binary uploader

- [ ] Add `web/scripts/artifact-upload.test.mjs` that verifies upload is a raw `File` body, auth retry replaces the bearer exactly once, abort rejects as cancelled, and a 503 storage error is actionable.
- [ ] Extract the duplicate streaming uploader in `web/src/lib/sidecarPublish.ts` into `web/src/lib/artifactUpload.ts`, retaining the existing 5-minute timeout, 256 MiB server cap, bearer/acting-tenant headers, auth retry, and error messages.
- [ ] Make `sidecarPublish.ts` use the shared helper without changing its package installation flow.
- [ ] Expose `api.uploadArtifact(file, {signal,onProgress})` and `api.artifactUrl(key)`; neither accepts caller-supplied session credentials.
- [ ] Run artifact uploader tests red/green and existing `node --test web/scripts/desktop-handoff.test.mjs`.

## Task 3 — Admin upload-and-publish UI

- [ ] Extend `PlatformUpdateConfig` with local `.app.tar.gz` + `.sig` file inputs, version/notes from the existing Release section, byte limits, and upload/publish/cancel/status/error states.
- [ ] On publish: read fresh update config; upload archive; fetch existing Release Hub row; upsert stable Release Hub entry while preserving other platform rows and same-version non-target metadata; last, enable stable automatic checks and save only the matching update-config target. Preserve `autoInstall` and all non-target policy/artifact settings.
- [ ] Display separate stages/errors. If hub save succeeds and updater-config save fails, explicitly report hub saved but rollout inactive. Do not suggest update delivered.
- [ ] Keep existing manual per-target URL/signature fields for operators that do not use local upload. Do not upload/publish a DMG.
- [ ] Add durable admin copy/docs that publishing creates an update offer/prompt; actual installation follows the configured `autoInstall` choice.

## Task 4 — End-to-end verification

- [ ] `node --test web/scripts/admin-release.test.mjs web/scripts/artifact-upload.test.mjs web/scripts/desktop-handoff.test.mjs`, `pnpm --dir web exec tsc --noEmit --pretty false`, and `pnpm --dir web build`.
- [ ] Through the local admin UI, select the already-built 0.1.3 `.app.tar.gz` and paired `.sig`, publish, and verify the uploaded bytes match the return key/digest.
- [ ] Re-read local update-config and Release Hub; both reference the same `/v1/artifacts/<key>`, policy is enabled+stable+autoCheck, and existing autoInstall is unchanged.
- [ ] Request the authenticated `darwin-aarch64` manifest as a 0.1.2 client and assert 0.1.3 URL/signature; request as 0.1.3 and assert 204. Use the packaged client only if a real signed-in session is available.
- [ ] Restore pre-test local update config and Release Hub row after proving this local-only scenario. Never publish to production without explicit target and authenticated production session.
