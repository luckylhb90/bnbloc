# Desktop update and browser handoff Implementation Plan

Status: implemented (merged in 1be3204); task checkboxes below were not maintained.


**Goal:** Ship the signed internal bnbloc desktop 0.1.3 update and guide browser users to that desktop only at a room's own-agent entry when their account has no live bridge.

**Architecture:** Keep Tauri's existing session-authenticated, stable-channel update manifest. Upload the signed archive once through the existing super-admin artifact API and reuse its gateway blob URL for the updater manifest and Release Hub. Add a browser-only, room-empty-state card using `/v1/my/agents` as the liveness authority; the browser cannot detect installed-app state and uses the existing `bnbloc://open` link.

**Tech Stack:** Tauri 2, Rust, React/TypeScript, Go gateway/update-config and realtime blobstore APIs, Node 24 built-in tests.

---

## File map

- `apps/desktop/src-tauri/Cargo.toml`, `tauri.conf.json`, `apps/desktop/package.json`: lockstep client version.
- `apps/desktop/src-tauri/src/updater_client.rs`: retain bearer header through update archive download; updater/plugin receives the same headers for both manifest and archive requests.
- `web/src/lib/desktopHandoff.ts`: pure platform, phone, live-roster display policy.
- `web/src/lib/releaseDownload.ts`: attach bearer only to the same-gateway artifact API; reauthenticate once; download the response as a browser blob.
- `web/src/lib/api.ts`: expose token/re-auth-backed `downloadReleaseFile` through shared API facade.
- `web/src/lib/useDesktopDownload.ts`: expose the configured platform release candidate without any hardcoded host.
- `web/src/views/DesktopHandoffCard.tsx`: compact download/open/re-check card.
- `web/src/views/RoomView.tsx`, `web/src/views/room/MessageFeed.tsx`: fetch own roster and render the card only in the room empty feed; preserve borrowed-agent room roster.
- `web/scripts/desktop-handoff.test.mjs`: deterministic `node:test` regressions for unknown/live roster, shell/mobile gates, and credential routing.
- `docs/prd/2026-10-05-desktop-update-and-browser-handoff.md`, `docs/desktop-update-release-runbook.md`: reflect the authenticated artifact URL distinction and 0.1.3 procedure.

## Task 1 — Regression helpers

- [ ] Test pending/null roster is not treated as confirmed empty; test empty roster shows only on desktop browser, while app shell, phone UA, own-online roster, or an online own room agent suppresses the card.
- [ ] Test credential routing: a same-gateway `/v1/artifacts/...` URL receives `Authorization: Bearer …`; an external HTTPS URL and an unrelated same-origin path never receive that header.
- [ ] Run `node --test web/scripts/desktop-handoff.test.mjs` and confirm it fails on the missing helpers before implementation.

## Task 2 — Desktop update archive authorization and release version

- [ ] Add the failing assertion to `apps/desktop/src-tauri/src/updater_client.rs`: the install path must not remove `Authorization` before the Tauri `Update::download` archive request.
- [ ] Run `cargo test --features internal updater_client::tests` and observe the targeted regression failure.
- [ ] Remove only the `update.headers.remove("Authorization")` line. Confirm the pinned Tauri plugin copies `Update.headers` into `download()`; do not add a second URL or separate S3 code path.
- [ ] Bump Cargo, Tauri, and desktop npm versions to `0.1.3`.
- [ ] Re-run the focused Rust test and check all version fields.

## Task 3 — Browser roster policy and handoff card

- [ ] Implement and pass the pure-helper tests from Task 1.
- [ ] Add `DesktopHandoffCard` with three explicit actions: current instance's configured macOS/Windows release artifact download, `bnbloc://open`, and caller-supplied roster re-check. Missing release metadata displays admin guidance, not a fallback host.
- [ ] Keep card text compact and inline, accessible, and explicit that it helps run the user's own agent while chat/borrow still work.
- [ ] In `RoomView`, initialize own-roster state as unknown, call `/v1/my/agents` after room/session changes and on card re-check, and keep unknown/error distinct from a successful empty roster.
- [ ] Render only in the approved active-room empty feed for non-shell desktop browser with no live own bridge. Do not add a modal, persistent app banner, MyRooms toolbar CTA, direct loopback probe, or install detection.
- [ ] Confirm `bnbloc://open` is already handled by Tauri to show/focus the desktop window.
- [ ] Run the direct helper regression and the web production build. Exercise own-roster empty/live/error, iPhone/Android phone UA, tablet UA, and desktop-shell cases through the deterministic test inputs.

## Task 4 — One signed artifact release and docs

- [ ] Build exactly one internal macOS Apple Silicon updater `.app.tar.gz` and its paired `.sig` after tests pass; do not use the unsigned `.dmg` as an updater artifact.
- [ ] As super-admin, `POST /v1/platform/artifacts` with the signed archive bytes. Record returned `key`, `sha256`, and `size`; GET the returned authenticated `/v1/artifacts/<key>` with the production session and compare SHA-256 before publishing anything.
- [ ] Update gateway `/v1/platform/update-config` to version 0.1.3 / channel stable / existing policy unchanged / macOS Apple Silicon URL and exact `.sig`. Add a stable `0.1.3` Release Hub row pointing at the same artifact URL and including byte size + digest. Preserve other configured targets/configuration.
- [ ] If the production session, realtime S3-backed store, or super-admin permission is unavailable, stop at the precise missing credential/capability; do not print tokens, guess a bucket, change remote S3 configuration, or claim upload.
- [ ] Record actual URL, sha256, signature pairing, tests, app version, and any production blocker in the PRD and updater runbook.

## Task 5 — Required verification

- [ ] Run `node --test web/scripts/desktop-handoff.test.mjs` and `pnpm --dir web build`.
- [ ] Run `cargo test --features internal updater_client::tests`, `cargo test --features internal internal_ext::sidecar_`, and `cargo check --features internal`.
- [ ] Build the signed updater archive and verify its paired minisign signature/public key and actual app bundle version.
- [ ] Launch the installed 0.1.3 app and confirm the `bnbloc://open` handoff focuses it; check the authenticated stable updater manifest from client 0.1.2 returns 0.1.3 only after the admin config is committed, then confirm 0.1.3 returns 204.
- [ ] Upload/authenticated-download round trip verifies exactly the same SHA-256 as the local signed archive; browser Release Hub/handoff downloads it using session-authenticated fetch.
- [ ] Real browser smoke confirms: own bridge absent -> room-only card; open link -> desktop activation; re-check after bridge heartbeat -> card disappears; chat and borrow remain usable.
