# Desktop update refresh and browser-to-desktop handoff

Date: 2026-10-05
Status: implemented locally; signed bundle built, production publication and installed-client smoke pending

## Goal

Publish the 0.1.3 internal desktop build and guide browser users to install/open the desktop only when their own local agent is needed and their bridge is not online.

## Approved release shape

- Bump Cargo, Tauri and desktop npm versions in lockstep to 0.1.3.
- Keep the existing signed internal updater and `stable` channel; preserve update policy settings.
- Build one macOS Apple Silicon `.app.tar.gz` and matching `.sig`.
- Components' authenticated `/v1/platform/artifacts` upload streams through gateway to the production realtime blobstore configured for S3. It returns a content-addressed key and SHA-256. The gateway download `/v1/artifacts/<key>` requires a session bearer. The Tauri updater retains its configured bearer on both manifest and archive GET; Release Hub/browser downloads fetch this URL with the browser's session bearer and save the body as a blob. Never publish a private object URL or invent S3 configuration.
- Reuse the uploaded archive URL for the updater config (`darwin-aarch64`) and Release Hub file entry; the signature must be the paired Tauri signature. Enable/update stable policy only after upload bytes are verified.

## Implemented browser behavior

- `web/src/views/room/useDesktopHandoff.ts` checks `/v1/my/agents` and `/v1/rooms/{roomId}/agents`; it leaves roster unknown on failure and only presents a card after successful responses show no own online agent. Desktop-shell and phone browser gates remain in the pure predicate.
- The empty room feed uses a compact `DesktopHandoffCard` with admin-configured desktop download, `bnbloc://open`, and roster re-check. Normal chat and agent borrowing stay usable. Release Hub, Run Agent, and Agents-page desktop downloads use the shared authenticated download helper for same-gateway artifact URLs.
- Tauri 0.1.3 keeps its session bearer on both update manifest and archive GET. The pinned updater plugin clones the configured headers into `Update`, then supplies them on `Update::download`.
## Administrator-local release publishing

- The super-admin's **Platform → Updates** screen accepts the signed updater `.app.tar.gz`, matching Tauri `.sig`, version, and notes. The `.dmg` is manual installation only; the publisher rejects it.
- After file/signature preflight, the client uploads to `/v1/platform/artifacts`, checks content key/SHA-256/size, then reloads current update config and Hub history. It upserts one stable `darwin-aarch64` Hub entry preserving other target files and finally saves update config using the same `/v1/artifacts/<key>` URL and signature.
- Publishing sets stable updater channel, enables checks, and preserves auto-install, cadence, minimum supported version, and non-targets. With auto-install off, clients get the normal prompt. The admin UI does not offer `internal` because current desktop clients request stable only.
- A different-channel row with the same version is rejected before Hub replacement. Failure after Hub save explicitly identifies that row as saved while update rollout remains inactive.

### Administrator-local release wizard

- Platform → Updates guides the super-admin through four steps: select and verify the `.app.tar.gz` plus its paired `.sig`, enter semver and notes, review the stable channel/gateway/policy, then publish and show the artifact digest. After success, the wizard advances to Result.
- Native file inputs retain the browser selection and display the same `File` that the uploader uses. Invalid/missing files, signatures, and versions stop before upload. Failures preserve selection and current step for correction/retry.
- Upload, Release Hub save, and updater activation report explicit current stage. Release Hub saves before updater configuration; a partial failure identifies the completed stage and states rollout is inactive.
- Focused local tests pass: 16 across preflight, release sequencing and artifact upload; `pnpm --dir web build` passes. The browser smoke uploaded a real signed 16,290,389-byte Tauri archive to the local gateway, verified the Release Hub and updater config records, then reset both local records. Production publication has not been performed.
## Verification and publication gate

- Browser policy/download unit regressions: 4 passed, covering unknown vs empty roster, browser shell/mobile/online-own-agent conditions, and same-gateway-only bearer routing.
- `pnpm --dir web build`: passed; file-length check and TypeScript completed; Vite emitted the large-chunk advisory. Signed internal Tauri 0.1.3 Apple Silicon app bundle, `.app.tar.gz`, and paired `.sig` built; bundle version is 0.1.3. Verified the detached signature against the public key embedded in `tauri.conf.json` using the cached Minisign implementation.
- Local authenticated `/v1/platform/artifacts` upload returned key `sha256/e493e433831fdb6c72ec6c4ac289bccef43782cf5510dfdda8f87196acbf062e`, matching SHA-256, size 16,287,625 bytes; authenticated `/v1/artifacts/<key>` download returned the same hash and size. The local gateway's configured backend is `realtime-blob`; production S3 upload is not established.
- Local browser smoke used the repository source checkout against a disposable local room: empty feed showed the handoff when both own-agent and room roster returned empty; a human-only chat message still appeared in the feed. A QA tab remained signed in with existing history and did not meet the empty-feed condition; phone-UA emulation and installed desktop activation were not verified.
- Production settings were intentionally not changed. No production deployment approval/session/S3 identity was available; the local `realtime-blob` capability does not prove production S3. Thus updater manifest, Release Hub 0.1.3 row, and installed-client self-update remain pending deployment by an authorized super-admin.

## Revision 2026-10-07: guidance visible everywhere, not only in empty rooms

The first version showed the handoff card only in an empty room feed, so members in
rooms with history never saw it. Now an app-wide `DesktopPresenceProvider`
(`web/src/lib/desktopPresence.tsx`) polls `GET /v1/my/agents` (every 60s and on
focus/visibility) in desktop-capable browsers (not the desktop shell, not phones):

- Sidebar footer: a persistent "Desktop app not connected" entry while no own agent
  is online; it opens the open/install/check guidance in a dialog.
- Room: a dismissible banner (remembered per member) above the channel row with
  "Open desktop app" and "Download"; hidden while the empty-room card is on screen.
- "Open desktop app" launches `bnbloc://open` and re-checks after 3/8/15s; if the
  bridge still isn't online the guidance says the app is probably not installed and
  promotes Download.
- Everything disappears as soon as an own agent reports online.

Verified in headless Chromium against the built bundle with a mocked API (sidebar
entry, banner, dialog, disappearance after the agent comes online).
