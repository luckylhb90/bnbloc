# CLI publishing: SSO login, component publish, desktop update publish

Status: implemented (working tree, uncommitted).

## Intent

The super-admin (or an assistant acting for them) publishes component packages
and desktop app updates from `cmd/bnbloc` against a production gateway that is
SSO-only (`POST /v1/auth/login` refused). Usage: [docs/cli.md](../cli.md).

## Design

- **CLI SSO.** `bnbloc login` starts SSO with
  `redirect_uri=<gateway>/sso-callback?cli_nonce=<nonce>` (256-bit nonce, start
  path from `GET /v1/auth/sso-config`). `completeSSO` parks the grant in the
  existing `ssoCodeStore` under `sha256(nonce)` (TTL 2 min, single use, a live
  entry is never replaced, nonce >= 32 chars) and redirects the browser to
  `#cliSignedIn=1` (no token, no code). `POST /v1/auth/sso/claim {"nonce"}`
  returns the grant once, else 404. The relay cookie carries `cli_nonce`
  because it is part of `redirect_uri`. The web renders a static "signed in to
  the bnbloc CLI" page for `#cliSignedIn=1` (no session adoption).
- **CLI credentials.** `--token`/`BNBLOC_TOKEN` > stored refresh token
  (`~/.bnbloc/config.json`, 0600, exchanged once per process, rotated token
  persisted atomically, only sent to its own gateway).
- **Update publish.** `bnbloc updates publish|status` mirrors the web wizard
  (`desktopRelease.ts`, `updateConfigFields.ts`, `artifactUpload.ts`); the Go
  mirror is `cmd/bnbloc/desktop_release.go` with tests mirroring
  `web/src/lib/updateConfig.test.ts`. The update/release DTOs moved from
  unexported gateway types to `internal/wire` so gateway and CLI share one
  shape. Policy: kept as is, only `enabled` turns on (WEB-10).
- **Component publish.** One publish call (`publishComponent`) and one upload
  helper (local sha256/size pinned against the response) shared by
  `cores install` and `cores publish-sidecar`; `--rollout` rides atomically in
  the publish request; `--seckey` defaults to
  `~/.bnbloc/component-publisher.key`; `keys gen` writes the key 0600 and
  refuses to overwrite without `--force`.

## Acceptance

- Gateway tests: claim before/after ACS, single use, wrong nonce, expiry,
  relay-cookie survival, replaced/weak nonce refused.
- CLI tests: refresh once + rotation persisted (0600, other keys kept), static
  token precedence, refused refresh hint, gateway binding, login poll + store,
  update publish happy path + every mismatch/partial failure, publish-sidecar
  flag mapping, prebuilt package passthrough, keys 0600.

## Known limits

- ~~The pending-grant table is per gateway process~~ — resolved by H3
  (2026-10-08 hardening batch): pending grants and desktop codes live in the
  shared `sso_handoff_codes` table, so any replica answers the claim.
- The nonce travels in the browser's start URL (and the IdP relay state);
  anyone who reads it within the 2-minute window can claim the grant first.
