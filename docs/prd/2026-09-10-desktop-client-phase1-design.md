# Design — Phase 1: bnbloc Desktop Client (Tauri shell + embedded bridge)

Status: implemented. Phase 1 of the client/frontend roadmap
(`docs/prd/2026-09-10-client-frontend-roadmap.md`): a native desktop app that
packages the shared web UI and embeds the `bridge` as a supervised local
connector, so a user installs/upgrades one app. See `apps/desktop` and the
"Desktop client (embedded sidecar)" section of `docs/bridge.md`.

## Goal

- One installable desktop app = shared web UI (Phase 0 `web/` bundle) + the
  `bridge` binary, bundled and upgraded together.
- The shell spawns and supervises the bridge as a child process, configures it,
  and surfaces its status. bridge stays a headless connector — no second
  business core.

## Stack decision

- **Tauri 2** (Rust shell + system WebView). Chosen over Electron for footprint
  and native packaging/signing. Verified locally: Rust 1.96, tauri-cli 2.11,
  Xcode CLT present (macOS arm64).
- The web UI is the existing `web/` build; the shell loads it, not a fork.

## Architecture

```
apps/desktop/
  src-tauri/            Rust shell
    tauri.conf.json     window, bundle, sidecar externalBin
    src/main.rs         app entry, spawn+supervise bridge, IPC commands
    binaries/           bundled bridge binary (built per target triple)
  (frontend served from ../../web/dist via beforeBuildCommand)
```

- **Frontend source of truth stays `web/`.** Tauri's `frontendDist` points at
  `../../web/dist`; `beforeBuildCommand` runs the web build. No UI is copied or
  forked — the same bundle the gateway embeds.
- **bridge as a Tauri sidecar** (`externalBin`): the `bnbloc-bridge` Go binary
  is compiled per target triple and named `bnbloc-bridge-<triple>` so Tauri
  bundles and resolves it. The shell launches it with the Tauri shell/process
  plugin.

## bridge supervision (no bridge code change)

bridge is already a headless env-configured daemon (`b.Run(ctx)`, SIGINT/
SIGTERM). The shell supervises it purely through process lifecycle — no HTTP
surface is added to bridge (keeps it a connector, not a core):

- **Spawn**: shell starts the sidecar with env from the app's settings
  (`BNBLOC_GATEWAY_BASE_URL`, `BNBLOC_BRIDGE_OWNER`, `BNBLOC_LOCAL_AGENT_*`,
  `BNBLOC_CORE_PROVIDER`, sync vars).
- **Status**: derived from process state — `starting` (spawned), `running`
  (alive past a short settle), `stopped` (clean exit), `failed` (non-zero exit).
  bridge's structured stdout/stderr logs are captured and tailed for surfacing.
- **Lifecycle**: shell starts bridge after the user is configured; stops it on
  app quit (graceful signal, then kill); restart on demand and on crash with
  bounded backoff.

## IPC surface (Rust <-> web)

Tauri commands the web UI calls (thin, additive; the web app degrades gracefully
when these are absent, i.e. when running in a plain browser):

- `bridge_status() -> { state, pid?, lastExitCode?, logsTail: string[] }`
- `bridge_start(config) -> ok` / `bridge_stop() -> ok` / `bridge_restart() -> ok`
- `get_settings() -> Settings` / `set_settings(Settings) -> ok`
  (persisted via the Tauri store; gateway URL, owner, local-core address/token,
  provider, sync secret)
- `app_info() -> { version, platform, inShell: true }`

The web app feature-detects Tauri (`window.__TAURI__`): in the desktop shell it
renders a Bridge panel (status, start/stop, settings) and reads `inShell:true`;
in a browser it hides that panel and behaves exactly as today.

## Runtime modes

- **Bundled** (default): shell loads the embedded web bundle offline; talks to a
  configured remote gateway URL; runs the local bridge.
- **Thin** (optional): shell points its WebView at a deployed gateway URL and
  still runs the local bridge for the local-core link.

## Build & packaging

- `apps/desktop/package.json` scripts: `dev` (tauri dev), `build` (tauri build).
- A prebuild step compiles the Go bridge for the host target triple into
  `src-tauri/binaries/bnbloc-bridge-<triple>` (script:
  `scripts/build-bridge-sidecar.mjs`, reusing the parameterized Go build).
- Web build runs via Tauri `beforeBuildCommand: "pnpm --dir ../../web build"`.
- Signing/notarization deferred to a release task; the Windows compatibility
  contract applies to sidecar path/suffix handling (`.exe`) and process spawn.

## Verification

- `pnpm --dir web build` + Go bridge sidecar build succeed.
- `tauri dev` (or a headless build) launches the shell, loads the UI, spawns the
  bridge sidecar, and `bridge_status` reports `running`; stopping the app stops
  the bridge. Where a full GUI launch is not possible in this environment, the
  Rust supervision logic is exercised by a smoke that spawns the built bridge
  binary and asserts the state machine, and the gap is stated explicitly.

## Windows impact

Sidecar binary name carries `.exe` on Windows; process spawn/signal differs
(no SIGTERM — use the platform kill). The Rust supervisor abstracts stop across
POSIX signal vs Windows terminate. Paths for the Tauri store and bundled binary
use Tauri's path APIs, not literals.

## Non-goals

- Auto-update server (later release task).
- Mobile shell (later).
- bnbloc-owned core (Phase 2).
- Adding an HTTP/business surface to bridge.
