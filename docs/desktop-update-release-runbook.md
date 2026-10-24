# Desktop auto-update: release runbook (internal build)

How to ship a desktop update to the internal bnbloc build. The internal client
updates through the **gateway** (a super admin configures the release in the DB;
clients poll the gateway), NOT the separate static download hosting used for
the public/portal path — keep the two separate.

Message path:

```
desktop client --GET /v1/app/update/{target}/{version} (Bearer session)--> gateway
gateway reads the super-admin update config --> returns a signed Tauri manifest (or 204)
client verifies the signature against the baked pubkey --> downloads --> installs --> restarts
```

## One-time / build-time (already in place)

These rarely change; listed so a new maintainer can verify them.

- **Signing key.** The updater signs artifacts with the minisign private key at
  `~/.bnbloc/updater.key` (outside the repo, chmod 600 — back it up; losing it
  means no client can verify a future update). Its public half is baked into the
  app at `apps/desktop/src-tauri/tauri.conf.json` → `plugins.updater.pubkey`.
  The two MUST be a pair; signing with a different key = silent verification
  failure on every client.
- **`createUpdaterArtifacts: true`** in `tauri.conf.json` makes the build emit
  the `.tar.gz` + its `.sig`.
- **Endpoint.** `tauri.conf.json` declares no endpoint; at runtime
  `apps/desktop/src-tauri/src/updater_client.rs` builds it as
  `{gateway_url}/v1/app/update/{{target}}-{{arch}}/{{current_version}}` and adds
  the user's session token. `gateway_url` is whatever the user set in the app's
  settings, else the build's `BNBLOC_DEFAULT_GATEWAY`. So there is no
  per-release client change — the download endpoint follows the gateway the
  client already talks to.
- **Default gateway (build input).** Set `BNBLOC_DEFAULT_GATEWAY` to the
  deployment's gateway URL when building. It is the first-launch default; the
  source has no fallback host (deployment data is never hardcoded), so a build
  without it starts with no gateway and the member must choose one on the
  login screen (Cmd/Ctrl+Shift+G). Release builds print a cargo warning when it
  is unset.
- **HTTPS only in release.** Release builds refuse a non-`https` update
  endpoint (no `dangerousInsecureTransportProtocol`). Debug builds still accept
  `http://localhost` for local testing.

## Per-release steps

### 1. Bump the version

`apps/desktop/package.json` is the bundle version: `tauri.conf.json` reads it
(`"version": "../package.json"`), and the updater compares that value
(`app.package_info().version`). Bump in lockstep:

- `apps/desktop/package.json` → `version`
- `apps/desktop/src-tauri/Cargo.toml` → `version` (Cargo cannot read
  package.json; `cargo test` fails while the two differ)

One version = one build. Once a bundle of a version has left the build
machine (handed out as a DMG, or published), any rebuild bumps the version:
the updater compares versions only, so a machine running an earlier build of
the same version is never offered the corrected one (2026-10-08: a pre-release
0.1.11 DMG kept a removed settings tab; fixed by shipping 0.1.12).

### 2. Build a signed internal bundle

From `apps/desktop/src-tauri`, with the signing key exported:

```sh
TAURI_SIGNING_PRIVATE_KEY="$(< "$HOME/.bnbloc/updater.key")" \
TAURI_SIGNING_PRIVATE_KEY_PASSWORD="" \
BNBLOC_DEFAULT_GATEWAY="$BNBLOC_URL" \
GO_TAGS=internal \
cargo tauri build
```

Output under `src-tauri/target/release/bundle/`:

- macOS (Apple Silicon): `macos/bnbloc.app.tar.gz` + paired
  `bnbloc.app.tar.gz.sig`; config package version is `0.1.3`.

The 2026-10-05 local internal build completed both files at these paths. Its
installed bundle reports version `0.1.3`. The archive has not yet been verified
against the baked public key or uploaded to production; do not publish its
signature until verification and authenticated artifact round-trip finish.

Without any `APPLE_*` env (the command above) the bundle is **ad-hoc signed**
(`signingIdentity: "-"` in `tauri.conf.json`) with the hardened runtime and
`src-tauri/Entitlements.plist`; it runs on the build machine and for members
who explicitly allow it, but Gatekeeper rejects it elsewhere. To ship a
Developer ID signed + notarized bundle, add the env vars from
[macOS signing and notarization](#macos-signing-and-notarization) to the same
command — nothing in the repo changes.

### 2a. Intel macOS (universal) and Windows builds

The bundle's architecture follows `--target`. The bridge sidecar follows it
too: `apps/desktop/scripts/build-bridge-sidecar.mjs` (Tauri's
`beforeBuildCommand`) reads `TAURI_ENV_TARGET_TRIPLE`, which `cargo tauri build
--target <triple>` sets, and writes `src-tauri/binaries/bnbloc-bridge-<triple>`
(`.exe` on Windows). It can also run alone with `--target <triple>`. Go
cross-compiles the bridge with CGO off, so no extra C toolchain is needed.

| build | command (from `apps/desktop`) | Rust targets | output under `src-tauri/target/` | updater target key(s) |
|---|---|---|---|---|
| macOS Apple Silicon (host) | `pnpm build` | host | `release/bundle/macos/` | `darwin-aarch64` |
| macOS universal (Intel + Apple Silicon) | `pnpm build:universal` | `rustup target add aarch64-apple-darwin x86_64-apple-darwin` | `universal-apple-darwin/release/bundle/macos/` | `darwin-aarch64` **and** `darwin-x86_64` |
| macOS Intel only | `pnpm build:x86_64` | `rustup target add x86_64-apple-darwin` | `x86_64-apple-darwin/release/bundle/macos/` | `darwin-x86_64` |
| Windows x64 (on a Windows runner) | `pnpm build:windows` | `x86_64-pc-windows-msvc` (default host on x64 Windows) | `x86_64-pc-windows-msvc/release/bundle/{nsis,msi}/` | `windows-x86_64` |

Prepend the same env as step 2 (`TAURI_SIGNING_PRIVATE_KEY`,
`BNBLOC_DEFAULT_GATEWAY`, `GO_TAGS`, and any `APPLE_*` signing vars).

The cross targets must come from the rustup toolchain. A Homebrew `rust` on
`PATH` ahead of `~/.cargo/bin` has only the host std and fails with
"can't find crate for `std`" for `x86_64-apple-darwin`; run the build with
`PATH="$HOME/.cargo/bin:$PATH"` (check: `rustc --print sysroot` points into
`~/.rustup`).

**Universal sidecar.** A universal build compiles the app once per arch; each
pass resolves `bnbloc-bridge-aarch64-apple-darwin` /
`bnbloc-bridge-x86_64-apple-darwin`, and the bundler then copies
`bnbloc-bridge-universal-apple-darwin`, which must be a fat binary. The sidecar
script builds both arches and merges them with `lipo -create` (Xcode command
line tools; macOS only). Check: `lipo -archs
src-tauri/binaries/bnbloc-bridge-universal-apple-darwin` prints
`x86_64 arm64`. Component sidecars downloaded at runtime are separate: the
desktop picks the artifact for its own arch (`darwin-amd64` on Intel), so a
component meant for Intel members must be published with `--binary
darwin-amd64=...` as well (`bnbloc cores publish-sidecar`).

**Publishing a universal archive.** The updater asks for
`{os}-{arch}` of the running machine, so a universal `bnbloc.app.tar.gz` serves
both keys. Publish it once per key, with the same version: the first run sets
`darwin-aarch64`; the second (same version) keeps that artifact and adds
`darwin-x86_64` (a *new* version would clear the other targets instead). The
upload is content-addressed, so the second run reuses the same artifact key.

```sh
B=apps/desktop/src-tauri/target/universal-apple-darwin/release/bundle/macos
for t in darwin-aarch64 darwin-x86_64; do
  bnbloc updates publish --archive "$B/bnbloc.app.tar.gz" \
    --signature "$B/bnbloc.app.tar.gz.sig" --version 0.1.11 --notes-file notes.md --target "$t"
done
```

An Intel-only build is published with `--target darwin-x86_64` alone. The
Platform → Updates wizard publishes `darwin-aarch64` only, so the Intel key is
always published from the CLI.

**Windows (documented; not part of the current release flow).** Build on a
Windows x64 machine or runner — Tauri does not cross-bundle Windows installers
from macOS:

- Prerequisites: Rust (MSVC toolchain, target `x86_64-pc-windows-msvc`),
  Visual Studio Build Tools with the "Desktop development with C++" workload,
  Go, Node 24 + pnpm. WebView2 ships with Windows 10 (1803+) and 11; the
  installer's default `webviewInstallMode` (download bootstrapper) installs it
  where missing.
- `bundle.targets: "all"` produces both an NSIS `-setup.exe` and a WiX `.msi`
  (WiX is downloaded by the Tauri CLI on first use; MSI builds need the VBSCRIPT
  optional feature enabled on newer Windows images).
- The sidecar is `bnbloc-bridge-x86_64-pc-windows-msvc.exe`; the script adds the
  suffix. The Rust supervisor's Windows stop path is `child_process.rs`
  (`#[cfg(windows)]`).
- Code signing: without a certificate the installers are unsigned and
  SmartScreen warns. Sign with an Authenticode certificate via
  `bundle.windows.certificateThumbprint` + `digestAlgorithm`/`timestampUrl`
  (certificate in the runner's store) or a custom `bundle.windows.signCommand`
  (e.g. a cloud HSM signer). Keep these values in the release environment's
  config overlay (`cargo tauri build --config <file>`), not in the repo.
- Updater payload: with `createUpdaterArtifacts: true` the signed installer
  itself (`*-setup.exe` + `.sig`, or `.msi` + `.sig`) is the update artifact
  for `windows-x86_64`. `bnbloc updates publish` and Platform → Updates accept
  only `bnbloc.app.tar.gz` today, so a Windows release is uploaded with
  `POST /v1/platform/artifacts` and set on the `windows-x86_64` target via
  `PUT /v1/platform/update-config` (url + signature pair, step 5).

### 3. Collect the release facts

For the built archive:

- **download URL** — the authenticated gateway URL returned by
  `POST /v1/platform/artifacts` (`/v1/artifacts/<key>`), backed by the
  production realtime blobstore. Do not use a private bucket/S3 URL.
- **signature** — the full contents of the paired `.app.tar.gz.sig`.
- **sha256 + size** — save the local archive digest and byte count; verify the
  authenticated gateway download returns identical bytes before publishing.

Map the build to the Tauri target key the gateway expects:

| build | target key |
|---|---|
| macOS Apple Silicon | `darwin-aarch64` |
| macOS Intel | `darwin-x86_64` |
| Windows x64 | `windows-x86_64` |

### 4. Publish the artifact

Use the super-admin API; the gateway streams directly to realtime's configured
blobstore. The client never reads bucket credentials, and no deployment bucket
URL is put into a release manifest.

```sh
curl -fsS -X POST "$BNBLOC_URL/v1/platform/artifacts" \
  -H "Authorization: Bearer $BNBLOC_SESSION" \
  -H 'Content-Type: application/octet-stream' \
  --data-binary @apps/desktop/src-tauri/target/release/bundle/macos/bnbloc.app.tar.gz
```

Save the returned `key`, `sha256`, and `size`. GET `/v1/artifacts/<key>` with
the same session bearer and compare its SHA-256 and size to the local archive.
The URL used by both clients and Release Hub is
`$BNBLOC_URL/v1/artifacts/<key>`; this endpoint requires a signed-in session.
Do not proceed unless the authenticated round trip matches exactly.

The paired `.sig` is selected from the local admin workstation's Tauri build
output. The administrator client reads and validates its Tauri Minisign header
and archive filename, then uploads the archive bytes; no manual gateway URL or
direct S3 credentials are needed.

### Publish from the signed-in administrator client

After building, open **Platform → Updates** in the signed-in super-admin client
connected to the gateway you intend to publish. Set the semver and notes, then
select the paired `bnbloc.app.tar.gz` and `bnbloc.app.tar.gz.sig` files. Click
**Upload and publish update**. The client uploads the raw archive through
`POST /v1/platform/artifacts`, verifies the returned content key/digest/size,
then reloads update configuration and Hub history to avoid stale overwrites.

- The publisher accepts only the signed updater `.app.tar.gz` and Tauri
  signature generated for that exact archive. A `.dmg` is for manual
  installation; it is not an updater payload.
- Publishing keeps the saved policy (channel, automatic checks, interval,
  auto-install, minimum version) and only turns updates on. Every target shares
  one version, so a new version clears the other targets' artifacts (they were
  built for the previous version); re-publishing the same version keeps them.
  New desktop clients request `stable`. With `autoInstall=false`, members see
  the standard update prompt.
- A same-version Hub row on a different channel is rejected. If Hub saves but
  the updater-config save fails, the error reports Hub saved but rollout
  inactive; retry after resolving the failure.
- Publishing goes to the gateway selected in the client. Verify that the gateway
  is the intended environment; a local developer blobstore is not production
  S3. Production releases require a super-admin session on production gateway.

### Publish from the CLI

`bnbloc updates publish` runs the same steps and rules as the client wizard
(see [cli.md](cli.md)): validate the archive name + Tauri signature, read the
current policy channel, upload, verify the authenticated download (sha256 +
size), save the Release Hub entry, then activate the updater under the current
channel. Each stage is printed; a failure says which stages completed.

```sh
bnbloc login --gateway "$BNBLOC_URL"
bnbloc updates publish \
  --archive apps/desktop/src-tauri/target/release/bundle/macos/bnbloc.app.tar.gz \
  --signature apps/desktop/src-tauri/target/release/bundle/macos/bnbloc.app.tar.gz.sig \
  --version 0.1.11 --notes-file notes.md
bnbloc updates status
```

### 5. Configure the release in the gateway (super admin)

In the app: **Platform → Updates** (super-admin only; backed by
`GET/PUT /v1/platform/update-config`). Super admins are the subjects listed in
the gateway's `BNBLOC_SUPER_ADMINS` env (default `root`).

**Release** (change every release):

| field | value |
|---|---|
| Version | the new semver (MUST be greater than the clients' current version) |
| Published at | RFC3339, e.g. `2026-09-20T00:00:00Z` |
| Release notes | shown in the update prompt |
| per-target Installer URL | the download URL from step 3, for each target you built |
| per-target Signature | the paired `.sig` contents (url + signature are set together) |

**Policy** (rarely changed):

| field | meaning |
|---|---|
| Updates (enabled) | master switch. Off = clients never check. |
| Channel | keep it **`stable`** (see the trap below) |
| Automatic checks | poll on launch + interval |
| Check interval hours | poll cadence |
| Auto-install | install without prompting |
| Minimum supported version | clients below this see a **forced** update |

Save. That writes the config to `platform_settings`; the next client poll picks
it up.

## macOS signing and notarization

Status: ready, waiting on an Apple Developer Program membership (a
**Developer ID Application** certificate; free accounts cannot notarize).
The repo side is done: `tauri.conf.json` → `bundle.macOS` sets
`hardenedRuntime: true`, `entitlements: ./Entitlements.plist` (intentionally
empty — the file lists each hardened-runtime exception considered and why it
is not needed) and `signingIdentity: "-"` (ad-hoc default).

`cargo tauri build` signs inside-out: every `externalBin` — the bundled
`bnbloc-bridge` sidecar in `Contents/MacOS/` — then the main binary, then the
`.app`, each with `--options runtime` and the entitlements file, then
notarizes and staples the `.app` before building the `.dmg` and the updater
`.app.tar.gz` (so the updater archive carries the stapled app). Component
sidecars downloaded at runtime are not part of the bundle and are not signed
by us; they run as separate processes, which the hardened runtime allows
(library validation only applies to code loaded into the app process).

### Env vars (read by `cargo tauri build`)

Signing — pick one:

| var | meaning |
|---|---|
| `APPLE_SIGNING_IDENTITY` | Keychain identity, e.g. `Developer ID Application: <Org> (<TEAMID>)` (`security find-identity -v -p codesigning`). Overrides the config's `"-"`. **Always set it for a real signature**, also in CI. |
| `APPLE_CERTIFICATE` + `APPLE_CERTIFICATE_PASSWORD` | CI: the exported `.p12` as base64 (`openssl base64 -A -in cert.p12`) and its export password; Tauri imports it into a temporary keychain. Use together with `APPLE_SIGNING_IDENTITY` — with the certificate alone the config's `"-"` is checked against the certificate name and the build fails. |

Notarization — pick one (if neither is set the build signs but skips
notarization with a warning):

| vars | meaning |
|---|---|
| `APPLE_API_KEY` + `APPLE_API_ISSUER` + `APPLE_API_KEY_PATH` | App Store Connect API key (Key ID, Issuer ID, path to the downloaded `AuthKey_<id>.p8`). Preferred for CI. |
| `APPLE_ID` + `APPLE_PASSWORD` + `APPLE_TEAM_ID` | Apple ID e-mail, an **app-specific** password, and the Team ID. |

Do not set notarization vars on an ad-hoc build: notarization requires a
Developer ID signature and fails the build otherwise.

Example (local release machine with the certificate in the login keychain;
`ASC_*` are your own shell variables holding the App Store Connect key id,
issuer id, and `.p8` path):

```sh
APPLE_SIGNING_IDENTITY="Developer ID Application: <Org> (<TEAMID>)" \
APPLE_API_KEY="${ASC_KEY_ID}" APPLE_API_ISSUER="${ASC_ISSUER_ID}" \
APPLE_API_KEY_PATH="${ASC_KEY_FILE}" \
TAURI_SIGNING_PRIVATE_KEY="$(< "$HOME/.bnbloc/updater.key")" \
TAURI_SIGNING_PRIVATE_KEY_PASSWORD="" \
BNBLOC_DEFAULT_GATEWAY="$BNBLOC_URL" GO_TAGS=internal \
cargo tauri build
```

### Verify

```sh
APP=apps/desktop/src-tauri/target/release/bundle/macos/bnbloc.app
codesign -dv --verbose=4 "$APP"                 # Authority=Developer ID Application: ..., TeamIdentifier=<TEAMID>, flags include runtime
codesign -dv --verbose=4 "$APP/Contents/MacOS/bnbloc-bridge"   # same Authority, runtime flag
codesign -d --entitlements - "$APP"             # the Entitlements.plist content (empty dict)
codesign --verify --deep --strict --verbose=2 "$APP"
spctl -a -vv "$APP"                             # accepted, source=Notarized Developer ID
xcrun stapler validate "$APP"                   # The validate action worked!
```

An ad-hoc build shows `Signature=adhoc`, `flags=0x10002(adhoc,runtime)`,
`TeamIdentifier=not set`, and `spctl` reports `rejected` — expected until a
Developer ID is configured.

## What makes an update actually push

The gateway returns a manifest only when ALL hold (else HTTP 204 → client thinks
it is up to date):

1. Policy **enabled** = true.
2. Requested target is one of `darwin-aarch64` / `darwin-x86_64` /
   `windows-x86_64`.
3. Client channel **== policy channel**.
4. That target has both a non-empty **url** and **signature**, and
   `release.version` is set.
5. `release.version` is **strictly greater** than the client's current version
   (semver).

`forced` is added when the client version is below `minimumSupportedVersion`.

## Traps

- **Channel must stay `stable`.** The internal client never sends a channel
  header/query, so the gateway treats it as `stable` (`requestUpdateChannel`
  default). If you set policy channel to `internal`, condition 3 never matches
  and NO client is offered the update, with no error — it just silently 204s.
  Only use `internal` if you also teach the client to send that channel.
- **url and signature are a pair.** Setting one without the other for a target
  yields no manifest for that target. The `.sig` must be the one produced for
  the exact `.tar.gz` at that url; a rebuilt archive needs its new `.sig`.
- **Version must increase.** Re-publishing the same version, or a lower one,
  never pushes (condition 5).
- **pubkey/private-key must match.** If the signing key is not the pair of the
  baked `plugins.updater.pubkey`, every client fails signature verification
  after download — looks like "updater broken."
- **The universal build needs rustup's `rustc`, not Homebrew's.** With
  `/opt/homebrew/bin` before `~/.cargo/bin` on the PATH, `pnpm build:universal`
  fails on the x86_64 half with "can't find crate for `core`" even though
  `rustup target list --installed` lists the target (the Homebrew compiler has
  no x86_64 standard library). Build with `PATH="$HOME/.cargo/bin:$PATH"`
  (2026-10-24, 0.1.107). Publish both targets from the one archive: `bnbloc
  updates publish … ` once per `--target darwin-aarch64` and `darwin-x86_64`
  (a new version clears the other target's artifact).
- **Verify on the real client path.** The updater runs on a live desktop client
  with a session token against the running gateway; a one-shot curl without the
  Bearer token 401s and does not reflect the client experience.

## Quick verification

With the stack running and a release configured, from a lower-versioned client
(or a curl standing in for one):

```sh
TOK=$(curl -s -X POST http://localhost:7090/v1/auth/login \
  -H 'Content-Type: application/json' -d '{"idpToken":"root"}' \
  | python3 -c 'import sys,json;print(json.load(sys.stdin)["sessionToken"])')

# darwin-aarch64 client currently on 0.1.1 asking for an update:
curl -s http://localhost:7090/v1/app/update/darwin-aarch64/0.1.1 \
  -H "Authorization: Bearer $TOK"
```

A configured, newer release returns the JSON manifest (`version`, `url`,
`signature`, `notes`, `forced`); otherwise HTTP 204 (already current / not
offered). On a real client, the update prompt appears via `DesktopUpdateWatcher`.

## Relationship to the other release surfaces

- **In-app Release Hub** (`docs/prd/2026-09-21-in-app-release-hub.md`) is a
  human-facing browse/download page fed by a separate release-history store; it
  does not drive the auto-updater.
- The **static download portal** is the public/multi-project
  download site; the internal auto-updater does not use it.
