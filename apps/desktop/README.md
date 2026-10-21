# bnbloc Desktop

The bnbloc desktop client: the shared web UI (`web/`) packaged with the
`bnbloc-bridge` connector as a supervised Tauri sidecar. One installable app
that upgrades UI + bridge together.

Design: `docs/prd/2026-09-10-desktop-client-phase1-design.md`.
Bridge supervision + config: `docs/bridge.md` → "Desktop client (embedded sidecar)".

## Layout

```
apps/desktop/
  package.json                 dev/build scripts
  scripts/
    build-bridge-sidecar.mjs   cross-compiles the Go bridge per target triple
  src-tauri/
    tauri.conf.json            window, CSP, bundle, externalBin (sidecar); version <- ../package.json
    Entitlements.plist         hardened-runtime entitlements (intentionally empty; see the file)
    capabilities/default.json  core:default only (plugins are driven from Rust)
    src/
      main.rs                  thin entry -> lib::run()
      lib.rs                   app setup, IPC commands, exit handling
      bridge.rs                bridge supervisor (spawn/state/restart/stop)
      artifact_scheme.rs       bnbloc-artifact:// scheme: the live-artifact runner's own origin
      child_process.rs         shared child spawn + graceful stop + backoff
      child_process/windows_job.rs  per-child kill-on-close Job Object (Windows)
      shell_env.rs             the member's login-shell environment, for every child
      internal_ext/            component sidecar manager (catalog, download, supervise)
      settings.rs              persisted connection settings (owner-only JSON file)
    icons/                     app icons (placeholder; real icons are a release task)
  (frontend served from ../../services/gateway/webui/static — the web/ build output)
```

## Prerequisites

- Rust (stable) + `cargo`.
- `cargo tauri` CLI: `cargo install tauri-cli --version "^2.0"`.
- Go toolchain (for the bridge sidecar).
- `pnpm` (for the web build).

## Develop

```sh
# from apps/desktop
pnpm dev        # cargo tauri dev: builds web, runs vite dev, launches the shell
```

## Build

```sh
BNBLOC_DEFAULT_GATEWAY=https://<your-gateway> pnpm build   # cargo tauri build: web + bridge sidecar + bundle
```

`BNBLOC_DEFAULT_GATEWAY` is the deployment's gateway, baked in as the
first-launch default. The source carries no fallback (deployment data is never
hardcoded): a build without it starts with no gateway and the member picks one
on the login screen (Cmd/Ctrl+Shift+G). Release builds warn when it is unset.

The version lives in `package.json` only for Tauri (`tauri.conf.json` reads it);
`src-tauri/Cargo.toml` must carry the same value — a unit test enforces it.

`pnpm build:sidecar` builds only the bridge binary into
`src-tauri/binaries/bnbloc-bridge-<triple>` (the name Tauri's `externalBin`
resolves).

`cargo tauri build` produces, under `src-tauri/target/release/bundle/`:

- `macos/bnbloc.app` — the app bundle, with `bnbloc-bridge` bundled beside
  the shell binary in `Contents/MacOS/` (the supervised sidecar).
- `dmg/bnbloc_<version>_<arch>.dmg` — the drag-to-install disk image.

### Icons

App icons are generated from `src-tauri/icon-source.svg`:

```sh
sips -s format png src-tauri/icon-source.svg --out /tmp/icon1024.png
cargo tauri icon /tmp/icon1024.png   # writes src-tauri/icons/* (icns, ico, png, platform sets)
```

### Signing & notarization

`tauri.conf.json` → `bundle.macOS` sets `hardenedRuntime: true`,
`entitlements: ./Entitlements.plist`, and `signingIdentity: "-"`: a build with
no `APPLE_*` env is **ad-hoc signed** by Tauri with the hardened runtime and
the entitlements (app and bundled bridge alike), so the release configuration
is exercised on every local build. Gatekeeper still blocks an ad-hoc app on
other Macs. Developer ID signing + notarization is pure configuration — set
the `APPLE_*` env vars at build time; see
`docs/desktop-update-release-runbook.md` → "macOS signing and notarization".

- Windows: sign the `.msi`/`.exe` with a code-signing certificate.

> **Running the compiled binary directly.** A plain `cargo build` debug binary
> loads `devUrl` (the vite dev server on :5173) and shows a blank window unless
> that server is running. To run a standalone binary that serves the _embedded_
> frontend, build with the custom protocol:
> `cargo build --features custom-protocol`. `cargo tauri dev` (vite) and
> `cargo tauri build` (auto-enables the feature) both handle this for you.

## How the shell talks to the bridge

The shell contributes **process supervision and thin config/status IPC only** —
no business logic. IPC commands (Rust ↔ web):

- `bridge_status() -> { state, pid?, lastExitCode?, logsTail }`
- `bridge_start()` (starts, or restarts a running bridge) / `bridge_stop()`
- `get_settings()` / `set_settings(Settings)`
- `component_settings()` / `component_settings_save(kind, name, values,
  clear)`: the member's own values of a plugin, MCP tool or the sandbox
  (Components → a component → Settings)
- `sandbox_status()` / `sandbox_restart_vm()` / `sandbox_private_hosts_apply()`
  (the sandbox's state on this computer, in its settings dialog)
- `internal_adapter_session(gatewayUrl, sessionToken)` (+ status/targets/login)
- `check_update(sessionToken)` / `install_update(sessionToken)`
- `open_url(url)` / `pop_out_preview(roomId, artifactId)`
- `app_info() -> { version, platform, inShell }`
- `shell_env_reload() -> { imported, error? }` (Agents → Local bridge →
  "Reload shell environment")

Process lifecycle: every child (bridge and component sidecars) gets a stdin
pipe the shell holds open; a stop closes it (supervised children exit on EOF),
SIGTERMs the child's process group on Unix, and force-kills the child's whole
tree after a grace period (process group on Unix, Job Object on Windows). Children stop only at app exit (quit, main window closed, updater
restart) — closing a preview pop-out never touches them. A restart waits for
the run it replaces to stop. The settings file holds credentials and is
written owner-only (0600) through a temp file unique to each write; an
unreadable settings file is moved aside (`settings.json.corrupt-<ms>`) and
reported in the bridge log, never reset and overwritten.

## The member's shell environment

An app started from Finder, the Dock or a desktop launcher never runs the
member's login shell, so what `~/.zshrc`/`~/.zprofile` export (API keys,
version-manager PATH entries) would reach no agent CLI. `src/shell_env.rs`
resolves it once per launch: `$SHELL` (`/bin/zsh` on macOS, `/bin/sh`
elsewhere when unset) runs as `-ilc` with stdin `/dev/null`, in its own
session, and prints `env -0` between a per-run random marker; rc output
outside the markers is ignored, and a shell that fails or takes longer than
4 s is killed with its process group (the children then get the app's own
environment). The first child spawn waits for it.

Every child (the bridge, every sidecar, and through them every agent CLI and
MCP stdio tool) gets it beneath what the desktop sets for that child, so
`BNBLOC_*`, tokens and ports always win, and a core's own per-agent config
applies later inside the core. `BNBLOC_*` and shell-session names (`PWD`,
`OLDPWD`, `SHLVL`, `_`, `TERM_SESSION_ID`, `SHELL_SESSION_*`) are never
imported; PATH is the shell's, then the app's entries it lacks. The log
shows the imported count (names in debug builds only), never a value.
Nothing the app sends over the network carries it. After exporting a new
variable, Agents → Local bridge → "Reload shell environment" resolves it
again and restarts the bridge and the sidecars. On Windows this is a no-op:
GUI apps inherit the user's variables there.

Settings map one-to-one to the bridge's environment (see `docs/bridge.md`). The
web bundle feature-detects the shell (`window.__TAURI__`) and renders a **Local
bridge** panel; in a plain browser that panel is hidden.

## Windows

The sidecar binary carries a `.exe` suffix on Windows and process stop differs
(no `SIGTERM`): the graceful signal is stdin EOF, then `TerminateJobObject`.
Every child is created suspended, put into its own Job Object with
`JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE`, then resumed, so nothing it spawns can
escape the job; when the app exits or crashes the OS closes the job handles
and the whole tree dies (`src/child_process/windows_job.rs` explains why one
job per child rather than one app-wide job). Settings and cache paths use
Tauri's path APIs, not literals.

Type-check from macOS (no Windows linker needed; `ring`'s C build is skipped
because `check` never links, `llvm-rc` comes from Homebrew `llvm`, and the
sidecar placeholder only satisfies `externalBin`):

```sh
rustup target add x86_64-pc-windows-msvc
touch src-tauri/binaries/bnbloc-bridge-x86_64-pc-windows-msvc.exe
cd src-tauri && PATH="$HOME/.rustup/toolchains/stable-aarch64-apple-darwin/bin:/opt/homebrew/opt/llvm/bin:$PATH" \
  cargo clippy --target x86_64-pc-windows-msvc --locked --all-targets \
  --config 'target.x86_64-pc-windows-msvc.ring_core_0_17_14_.rustc-link-lib=[]' -- -D warnings
```

## Live-artifact runner and CSP

Agent-made HTML artifacts run in a `sandbox="allow-scripts"` iframe (no
`allow-same-origin`) that loads `artifact-runner.html` by URL — never
`srcdoc`, which would inherit the app's CSP. The shell serves the runner from
its `bnbloc-artifact` URI scheme (`src/artifact_scheme.rs`: exactly two
compiled-in files from `web/public/`, with their own CSP header allowing
inline script); the app CSP therefore keeps `script-src 'self'` (plus the
hashes Tauri adds) and its `frame-src` admits only
`bnbloc-artifact://localhost` / `http://bnbloc-artifact.localhost` (Windows).
In a browser the gateway serves the same runner files from the web bundle.
Protocol and checks: `web/src/lib/artifactFrame.ts`.
