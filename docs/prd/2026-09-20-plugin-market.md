# Plugin market — signed core packages an admin can install

**Date:** 2026-09-20
**Status:** historical — implemented at the time; superseded by the Components
API (`/v1/platform/components`, see
`docs/prd/2026-10-01-components-workbench-redesign-design.md`). The routes and
console screen named in this doc no longer exist.

## What this builds on

This is step 3 of three. It only makes sense because the first two landed:

1. `POST /v1/cores/install` now requires super-admin (it previously accepted any
   signed-in user, which would have made a market a remote-write primitive).
2. **Core Protocol v1** gives a core a contract to implement, so a conforming
   daemon attaches with no bnbloc code change. Without it a "market" would only
   distribute manifests describing daemons that already happen to match our
   expectations — a config-file uploader, not a plugin system.

## What already exists (verified, not assumed)

| piece | where | role here |
| --- | --- | --- |
| install / uninstall endpoints | `services/gateway/service.go` | the install API |
| super-admin RBAC | `requireSuperAdmin`, `rbac.PermManagePlatform` | who may install |
| `platform_settings` store | `internal/store/platform_settings.go` | installed-package registry |
| runtime core loading, no recompile | `core/manifestcore`, `coreset.FromEnv` | activation |
| protocol client + conformance suite | `core/protocol`, `core/conformance` | what a package must satisfy |
| ed25519 signing | `services/deviceauthority`, updater keys | package signatures |

The last one matters more than it looks: the updater's minisign key decodes to
algorithm `Ed`, i.e. **plain ed25519**. So package verification reuses
`crypto/ed25519` from the standard library and the key material we already
manage — no new dependency, no second trust root.

## Package format

One signed bundle describing a core, whatever its runtime:

```json
{
  "kind": "bnbloc.core.package/v1",
  "name": "acme-agents",
  "version": "1.4.2",
  "protocol": { "min": "1.0", "max": "1.0" },
  "runtime": "endpoint | manifest | sidecar",
  "endpoint": { "baseUrl": "https://agents.internal.example" },
  "manifest": { },
  "artifacts": {
    "darwin-arm64": { "url": "...", "sha256": "..." },
    "linux-amd64":  { "url": "...", "sha256": "..." }
  },
  "capabilities": ["interrupt", "writeFile"],
  "permissions": { "network": ["agents.internal.example"] },
  "publisher": "Acme Corp",
  "description": "…"
}
```

Signed detached, minisign-style: `package.json` + `package.json.sig`.

Three runtimes, one format — that is the point. `endpoint` for a daemon the
customer already runs, `manifest` for a simple REST core (the frozen DSL),
`sidecar` for a binary the desktop app supervises.

## Install flow

```
super admin uploads package + signature
        │
        ├─ verify ed25519 signature against a trusted publisher key
        ├─ reject unless protocol.min..max overlaps what bnbloc supports
        ├─ validate the package schema
        ├─ probe: run the conformance suite against the declared endpoint
        ├─ record in platform_settings (name, version, publisher, key id, hash)
        └─ register the core — no restart
```

Two decisions worth stating:

- **Conformance probe at install time, not first use.** A core that fails the
  contract should be refused while an admin is watching, not surface as a
  broken room later.
- **Trusted keys are configured, not implicit.** A package signed by an unknown
  key is refused. Self-signed development packages need an explicitly added
  key, which is what makes "signed" mean anything.

## Admin surface

`GET/POST/DELETE /v1/platform/core-packages` plus a platform-console screen:
installed packages with publisher, version, protocol range, signature status,
health; upload; enable/disable per tenant; uninstall.

Per-tenant enablement matters: the cores directory is shared, so installing and
exposing must be separable.

## Deliberately not in v1

- **A hosted public registry.** The product is self-hosted and air-gap
  friendly; upload-a-file is the honest primitive. A registry is a fetch layer
  on top and can come later.
- **Auto-update of packages.** The app updater exists; conflating the two adds
  a second update path before the first has users.
- **Sandboxing beyond process boundaries and a declared network allowlist.**
  Real isolation needs OS-level work; over-promising is worse than not claiming
  it. The `permissions` block is recorded and shown to the admin, and enforced
  only where we can actually enforce it.

## Acceptance

- A super admin can upload a signed package and have the core usable without a
  restart; a non-super-admin cannot.
- An unsigned package, one signed by an untrusted key, or one whose protocol
  range does not overlap, is refused with a message naming the reason.
- A package failing the conformance probe is refused at install.
- Installing a third-party conforming core requires no bnbloc code change.
- Public and internal builds, vet and tests all pass.

## Landing

- Package format + verification: `core/pkgmeta/` (public)
- Endpoints: `services/gateway/core_packages.go`
- Registry: `platform_settings`
- Console: `web/src/views/PlatformCorePackages.tsx`

Server-side implementation landed with signed package parsing/verification,
trusted-key lookup from `platform_settings` (`corepkg.trustedKeys`), install/list/
uninstall routes, install-time conformance probing, and runtime core registration.
The platform-console screen now lists installed packages, surfaces signature and
protocol refusal reasons next to the install controls, confirms uninstall by
package name, and explains that trusted publisher keys are still managed through
platform settings because no key-management endpoint exists yet.
