# Extensibility governance — capability vs. policy are separate layers

**Date:** 2026-09-24
**Status:** normative — supersedes the 2026-09-20 "what to pluginize" ruling

## What changed and why

The earlier version of this doc (2026-09-20) answered "does everything need to
be a plugin?" with a per-capability verdict table that decided, *at the
architecture level*, that auth/storage/webhooks must **not** be extensions —
they stayed built-in or build-tagged. That conflated two things that must be
separate:

1. **Capability** — *can* the platform load/drive this kind of extension through
   one uniform mechanism?
2. **Policy** — *should a given deployment allow* a particular extension to be
   installed/enabled, given its trust, blast radius, and audit needs?

Using "we won't build the capability" as the way to enforce "this is dangerous"
is architectural cowardice: it bakes a policy decision into missing code, so the
danger is managed by *absence* rather than by *control*. The result is exactly
the mess we have — SSO hardcoded behind build tags, storage frozen in a
constructor switch, no webhook seam at all — three different ad-hoc answers to
one question.

**New position: build ONE extension foundation that can host any A-class kind
(external HTTP capability describable as config/contract), and govern what a
deployment actually permits through an explicit policy + audit layer — not
through architectural omission.**

Dangerous kinds (auth, storage, webhooks) are not excluded from the foundation.
They are **governed on it**: signed, super-admin-gated, capability/permission-
scoped, enable-after-install, fully audited, and revocable. Control, not
absence.

## The two layers

### Layer 1 — the extension foundation (capability)

One shared shell hosts every kind uniformly:

- signed-package upload + ed25519/minisign verification against trusted keys,
- super-admin install / list / enable / disable / uninstall,
- MySQL-persisted registry (`ext.<kind>.<name>`),
- `kind`-dispatched typed contracts (core, auth, webhook, storage, …),
- backend orthogonality (native / manifest / sidecar) per kind,
- host-set per kind (which services activate it),
- a uniform `/v1/extensions` surface + console.

Adding a kind = define its typed contract + register a `KindDescriptor`. The
shell does not change. This is the geist of `multi-kind-market.md` (typed
per-kind contracts over one shell — **not** a universal package DSL; that trap
still stands: each kind carries only its own contract's fields).

### Layer 2 — governance & audit (policy)

Separate from "can we host it", every install/enable/drive/uninstall passes
through a policy + audit layer that a deployment configures:

- **Trust:** signature required (dev-unsigned only behind an explicit flag);
  trusted publisher keys are the root.
- **Gate:** super-admin install; higher-stakes kinds (auth) require an explicit
  second **enable** step after install, with the IdP entityID/cert fingerprint
  pinned.
- **Scope:** capability/permission grants (`permissions.network` allowlist
  *enforced*, not just recorded); per-tenant enablement overlay.
- **Blast-radius controls per kind:** e.g. an auth kind's assertion→tenant/role
  mapping and open-redirect allowlist are enforced by the kind's contract, not
  left to each provider; a webhook sink is async-only and cannot block/observe
  the hot path; a storage kind is fenced from control-plane truth stores.
- **Audit:** every lifecycle transition (install/enable/disable/uninstall) and,
  where it matters, every drive (auth login, webhook delivery) is recorded with
  actor + extension identity + signature/keyId. This is how "should this be
  allowed" is answered continuously — by observability + revocation, not by
  never building the door.

## The one rule (restated)

> Any external capability that can be described as a typed contract + config is
> hostable on the foundation. Whether a *specific* extension is permitted in a
> *specific* deployment is a governance decision (trust + scope + audit +
> enable-gate), enforced at Layer 2 — never by refusing to build Layer 1.

The old "if you expect only 2-3 implementations it's a registry, not a plugin"
heuristic is retired: a build-time registry and a runtime-installed extension
now share the same foundation (a built-in is a pre-installed, non-removable
extension — the Backend⟂Source model already in `core/provider.go`). The number
of implementations no longer decides the *mechanism*; it may still inform the
*governance posture* (a rarely-changing, high-stakes kind ships disabled by
default and requires explicit enable).

## Hot-path safety is a contract property, not a reason to exclude

The one hard invariant the old doc got right, kept and reframed: **nothing runs
arbitrary synchronous code on a hot path (Hub.Publish, auth assertion, persist).**
This is not enforced by refusing webhooks — it is enforced by the *webhook kind's
contract being async-sink-only*, and the *auth kind's contract being a
native-backed handshake with a fixed, reviewed mapping seam*. The contract shape
per kind is where hot-path and blast-radius safety live. That is control, and it
lets us build the door instead of walling it off.

## Room semantics stay core — by contract, still hostable-adjacent

Room collaboration model, membership, ordering, message durability stay
**built-in product truth**. Extensions **observe** (webhook), **authenticate
into** (auth), or **drive agents within** (core) the room — they never redefine
it. This is not an architectural exclusion of an "extension kind"; it is simply
that "redefine room semantics" is not an A-class external-HTTP capability and
has no contract. If someone proposes it, the answer is a governance No, recorded
and audited — not a missing mechanism.

## Related

- `docs/prd/2026-09-20-multi-kind-market.md` — the shared-shell + typed-per-kind
  contract shape (still valid; this doc adds the Layer-2 governance split).
- `docs/prd/2026-09-20-core-protocol-v1.md` — the agent-core wire contract.
- `docs/internal/2026-09-24-unified-core-extension-registry.md` — the landed
  Backend⟂Source unified core registry the foundation generalizes.
- (next) the new foundation design doc + per-kind contract PRDs (auth first).
