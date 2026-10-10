# Platform packages → Components: upload, rollout policy, member catalog

Date: 2026-09-30
Status: historical — the rollout backend and member catalog landed here and
carry forward, but the `core-package` routes/UI names in this doc were
replaced one day later by the Components Workbench clean cutover
(`docs/prd/2026-10-01-components-workbench-redesign-design.md`); the
`/v1/platform/core-packages` routes now 404. The
interactive demo (`docs/demo/2026-09-30-platform-packages-redesign.html`)
remains as the design reference.

## Intent

The platform console's core-package module was built for a handful of
signed core packages, install-by-paste. It now has to distribute **more
kinds of components** (cores today; plugins next, per
`docs/prd/2026-09-20-multi-kind-market.md`), and distribution needs a
**rollout policy**: an admin publishes a component and chooses whether
every member gets it automatically or members choose to install it
themselves.

## Pain points in the current screen (verified, `PlatformCorePackages.tsx`)

- Install is **paste raw JSON** into a 12-row textarea plus a signature
  textarea; the file inputs only shovel text into those boxes. No
  validation feedback until the server round-trip fails.
- `PublishSidecarPanel` is a second, disjoint 10-field form (5 per-platform
  binary pickers) beside the paste form — two mental models for "add a
  component".
- The installed list is **read-only cards + Uninstall**: no rollout
  policy, no visibility into who has what, nothing user-facing.
- One kind only (`bnbloc.core.package/v1`); nothing can express a plugin.

## Design (demoed)

**Admin console — one "Components" page.**

- **Publish**: one Components Workbench supports two inputs: (1) import a complete JSON package, or (2) build a package from a short kind/runtime-specific form. The form only asks for fields required by that package shape. For sidecars, upload real platform binaries first; the final artifact URLs and SHA-256 values are then included in the exact JSON bytes shown for signing.
- **Runtime-specific contract**:
  - core endpoint: protocol range + endpoint URL, no binary upload;
  - core manifest: protocol range + manifest JSON, no binary upload;
  - core sidecar: protocol range + loopback launch config + binary artifacts; Core Protocol sidecars need no manifest, while a legacy REST drive manifest is optional;
  - auth-provider endpoint and plugin sidecar keep their current package-specific contracts, runtime restrictions, hooks, and permissions.
- Signature verification, artifact checks, rollout, persistence, and kind-specific probes remain the existing Gateway publish workflow; the UI never invents signatures or successful server validation.

### Unified builder acceptance

- A super-admin can build endpoint and manifest cores without binary uploads.
- A super-admin can build a Core Protocol sidecar without a manifest: select a target platform, upload its real binary, and sign the final JSON containing the gateway-returned artifact URL and SHA-256.
- Legacy REST sidecars may include an optional drive manifest.
- Plugin sidecars and auth-provider endpoints keep their own required contract fields and runtime restrictions.
- Importing a complete JSON package remains available.
- Replacing a binary clears the signature because final package bytes changed. Gateway signature, artifact, and package validation remain authoritative.

2. **Rollout policy per component** (a segmented control on each card, editable after publish):
   - **Auto-install** — every member gets it; members cannot remove it.
   - **Opt-in** — members see it in their catalog and choose.
   - **Off** — registered but not distributed (hidden from members).
3. **Installed list**: kind badge (core / plugin), rollout control,
   adoption stat (installed/member count), trust block, uninstall with
   confirm. Kind filter (All / Cores / Plugins).

**Member view — a "Components" surface** (demo's second tab):

- *Your components*: auto-installed ones marked "Managed by your admin"
  (no remove); opt-in installs the member added (removable).
- *Available to add*: the admin's opt-in catalog with Install/Uninstall.
- Components the admin turned Off never appear.

Semantics map to what exists or is already designed: super-admin install
API (`POST /v1/platform/core-packages`), trusted-key registry, tenant
enablement overlay (`PUT .../tenants`) stays orthogonal (not in this
demo's scope), and package `kind` selects the contract per the
multi-kind market design.

## Acceptance (for the real implementation, when scheduled)

- Admin can publish by drag-drop or file pick; every validation failure
  surfaces the server's exact reason inline (no toast-only errors).
- Rollout policy is editable post-publish and takes effect for members
  without re-login (next `GET` of the member catalog reflects it).
- Auto-install components are present + non-removable for members;
  opt-in components are installable/uninstallable by the member; Off
  components are invisible to members.
- The member surface is kind-aware (core vs plugin) with no
  core-specific jargon on plugins.
- No hardcoded environment data: rollout state, package list, adoption
  counts all come from the platform-settings-backed store, never literals.

## What landed (2026-09-30)

**Backend**

- `extpipe.Record.Rollout` (`auto` | `optin` | `off`; empty ≡ auto, so
  pre-existing records keep the old everyone-gets-it behavior) +
  `Pipeline.SetRollout` (persist + audit, read-time filter like SetTenants).
- Per-member opt-in installs: `component_installs` table
  (`internal/store/component_installs.go`, memory + MySQL; migration
  `2026_09_component_installs`; both schema.sql files) keyed
  (subject, kind, name).
- Gateway (`services/gateway/components.go`):
  - `GET /v1/components` — the member catalog (auto → installed+managed;
    optin → installable; off → omitted; tenant overlay respected).
  - `POST /v1/components/{name}/install` / `DELETE …/install` — member
    opt-in / removal (install requires rollout=optin).
  - `PUT /v1/platform/core-packages/{name}/rollout` — super-admin policy
    switch.
- The rollout gate is enforced on every member surface: `/v1/cores`,
  `/v1/components`, and `/v1/cores/{name}/artifact` (the desktop's
  sidecar/manifest download path), so a member's desktop only fetches what
  their catalog says is theirs. The desktop sidecar manager needs no change
  — it already follows the member-scoped `/v1/cores`.

**Web**

- Platform console: the paste-JSON forms are replaced by a dropzone +
  parsed review card (facts, capabilities, network permissions) with the
  rollout choice at publish time; published cards carry a live
  Auto/Opt-in/Off segment (`ui/Segment`); sidecar publish + trusted keys
  panels unchanged.
- Member "Components" view (sidebar, `#/components`): Your components
  (auto = "Managed by your admin"; opt-in = removable) + Available to add.
- `api.ts` split: market wire types moved to `lib/apiTypes.ts`
  (file-length gate).

**Deferred from the demo** (not in this landing): the simulated validation
pipeline animation (real install errors surface inline instead), adoption
counts (needs per-member aggregation once more than the desktop consumes
the catalog), and plugin kinds beyond `core` (the multi-kind market
design).
