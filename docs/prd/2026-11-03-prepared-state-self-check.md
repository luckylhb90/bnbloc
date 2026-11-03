# Prepared state checks itself: one rule for what a core builds for others' programs (2026-11-03)

Status: built in bnbloc-acp 0.2.28 (`a6f8674`), published to QA as optin;
decisions P1-P5 accepted by the owner 2026-11-03. The case that started it is
fixed (`docs/bugs/2026-11-03-opencode-upgrade-strands-sandbox-setup.md`).

## Why

A core prepares state on the member's machine for programs it does not own
(an agent CLI, an npm package, the sandbox helper) and trusts it later. Each
piece has its own rule for "still good", written when it was built and
tested only from empty. When the program changes underneath (the member
upgrades opencode, an agent package publishes a new version, the desktop
ships a new sandbox helper), a rule that cannot see the change trusts stale
state or, as on 2026-11-03, refuses it forever without being able to repair
it: every sandboxed opencode session failed until a person deleted a
directory by hand, and the reason the room saw was the last line of a model
list.

The owner's question: is this general, and can it heal and check itself
instead of being patched case by case?

## Inventory (bnbloc-acp, read 2026-11-03)

| State | Where | Built for | Its key today | When that changes |
|---|---|---|---|---|
| opencode routing config dir | `sandbox.go` `prepareOpencode` | the opencode version, the vmexec path | vmexec path hash; version checked by reading opencode's own files | **stuck forever** (fixed today by a reset before the warm-up) |
| npx cache entry per space | `warm_npx.go` | an npm package spec | space + agent name + spec hash; `.bnbloc-installed` marker | a `@latest` spec never re-resolves; a failed start removes the marker (`distrust`), so a broken entry heals; a stale but working one is kept |
| npm cache dir per space | `sandbox.go` `npmDir` | a space | agent name + space hash | never cleaned: 5.4 GB on this machine, 30+ dirs, many of retired spaces |
| `opencodeVersions` memo | `sandbox.go` | the opencode binary | path, size, mtime | re-read on change (correct) |
| observed catalogs | `catalog.go` `observed-catalogs.json` | a guest agent's models | agent name | replaced by the next live session (correct); an agent removed from the config is not read |
| probe status | `catalog.go` `registry.status` | agents.json entry | the config entry (`reflect.DeepEqual`) | re-probed on change (correct) |
| profile record | `profile.go` `<ws>.profile.json` | the files a profile wrote | workspace | rewritten per session (correct) |
| in-use record | `inuse.go` | live sessions | — | rewritten every minute (correct) |

Three patterns, then:

1. **Correct**: the key names everything the state depends on, and a change
   of key rebuilds (probe status, version memo, profile record).
2. **Heals on failure only**: a marker removed when a start fails (npx). It
   repairs what breaks loudly, never what is stale and works.
3. **Can neither see nor repair**: the readiness check names the version,
   the repair cannot reach it (the opencode dir, before today). This is the
   one that failed.

And one more that is not a correctness bug but the same lack of an owner:
nothing removes state whose user is gone (npm dirs of retired spaces).

The same class exists outside bnbloc-acp, each with its own rule today: the
desktop's sidecar sweep (`sidecar_sweep.rs`, judged against the live
catalog by build), the sandbox bundles, the previewer cache, and the
bridge's `workspaces.json`. Those already key on a content pin (sha256) and
are swept against the live catalog; they are the model to follow, not a
gap.

## Proposal

One small mechanism in bnbloc-acp, used by every piece of state it
prepares, and the same questions held by a design-review lens everywhere
else.

### 1. A prepared directory carries the key it was built for

`prepared.go`: a directory the core prepares has a stamp file
(`.bnbloc-prepared.json`) written **last**, after the build succeeded:

```json
{ "kind": "opencode-routing", "key": { "opencode": "1.19.5", "vmexec": "<path>", "plugin": "<sha of the plugin text>" }, "builtAtMs": 1793676441714 }
```

`Ensure(dir, kind, key, build)` is the one entry point:

- the stamp matches `key` → use it, nothing else is read;
- the stamp is missing, unreadable or names another key → remove what the
  kind declares it owns (`Owned` paths; never the whole directory unless the
  kind says so), run `build`, then the kind's `Ready` check, then write the
  stamp;
- `build` or `Ready` fails → no stamp, the error names the kind, the key
  and the step, never a line of the program's output as the reason.

`Ensure` runs under the existing `warmFlight` (one build per directory at a
time, not owned by the request that asked). The key is computed from facts
the core reads cheaply (a version memoized by binary identity, a path, a
hash of the text the core itself writes), so a change of any of them is a
change of key.

The opencode directory, the npx entries (key: the spec **and** the version
npm resolved it to, read from the entry's `package.json`), and any later
prepared state go through it. The npx marker becomes this stamp.

### 2. A failed use distrusts the stamp

What `distrust` does for npx today, for every kind: a session that fails to
start from prepared state removes its stamp, so the next start rebuilds
once. A second failure after a rebuild is reported as that, not retried in
a loop (P2).

### 3. `bnbloc-acp check` reports prepared state

`check` lists, per kind, each directory: its key, whether it matches what
the core would build now (`current`, `stale: opencode 1.18.35 → 1.19.5`,
`unstamped`), its size and when it was last used. `check --repair` rebuilds
the stale ones and removes the unused ones (P3). The member, the desktop's
diagnostics and support read the same lines.

### 4. State whose user is gone is removed

A prepared directory records its last use (the stamp's `usedAtMs`, written
at most once an hour). At start the core removes those of a kind that
declares an expiry and are older than it (P4): npm dirs of spaces unused
for 30 days, say.

### 5. Proven by an upgrade test per kind

Every kind has one test that builds it for version A, then uses it with
version B (a fake program whose behaviour copies the real one's: opencode
keeps a `package.json` it finds), and one that removes the stamp mid-build.
`TestEveryPreparedKindHasAnUpgradeTest` fails on a kind without them.

## Placement

| Part | Place | Why | What an organization would swap |
|---|---|---|---|
| `prepared.go`, stamps, check | component (bnbloc-acp) | the state and the programs are the core's | another core, with its own |
| Lens 11 question | platform docs | how every component and the desktop are reviewed | — |
| Desktop caches | already platform, pin-keyed | unchanged | — |

## Design review

- **Actors:** the member (upgrades programs), the agent's package publisher,
  the desktop (ships the sandbox helper), the core (builds), a borrowed
  run (uses a space's cache). Two cores on one machine have separate homes.
- **Time and lifecycle:** a build cut halfway leaves no stamp; a program
  upgraded while a session runs changes the key for the next start only.
- **Scale and cost:** a key costs a stat and a memoized `--version`; a
  rebuild costs what the first build cost. Cleanup bounds the disk (today
  5.4 GB unbounded).
- **Failure and recovery:** the point of the change; a rebuild that fails
  twice says so and stops (P2).
- **Boundaries and privacy:** stamps hold versions and paths, no secrets;
  removal is limited to the kind's declared paths under the core's home.
- **Abuse:** the directories are write-denied to the agent already; a
  stamp written by a confined process cannot exist (the stamp is written by
  the core, outside confinement).
- **One owner:** `Ensure` is the only builder of prepared state; an
  architecture test refuses a `MkdirAll` of a prepared kind elsewhere.
- **Defaults:** no stamp = not trusted (fail closed, then rebuild).
- **External contracts:** each kind's `Ready` check is written against the
  real program's behaviour and its fake copies it (lens 9).
- **Load on users:** zero in the common case; `check` explains the rest.
- **Release:** first run after the release finds every existing directory
  unstamped and rebuilds each once (a seconds-long npm reinstall per space
  from the seeded host cache; opencode's set-up). Rolled out `optin` first.
- **Observability:** each rebuild logs kind, key change and duration;
  `check` shows the state.
- **Pre-mortem:** a key that misses a fact (a program that changes
  behaviour without changing its version) still goes stale: §2 catches it
  on the first failure; a key with a fact that changes every run (a temp
  path) rebuilds forever: the upgrade test with two runs of one version
  catches it.

## Decisions (accepted by the owner 2026-11-03: every recommendation)

- **P1. Unstamped state at first run:** rebuild every directory once
  (recommended: simple, the cost is one reinstall per space), or adopt one
  that passes `Ready` by stamping it as it is.
- **P2. A rebuild that fails again:** report and stop until the key changes
  or the member runs `check --repair` (recommended), or retry with a
  backoff.
- **P3. `check --repair`:** offered (recommended), or `check` only reports.
- **P4. Expiry of unused state:** npm dirs of spaces unused 30 days
  removed at start (recommended); other kinds keep theirs; or no expiry.
- **P5. Other cores and the desktop:** this PRD covers bnbloc-acp only, and
  lens 11's new question covers the rest at review (recommended); or a
  shared Go package in `core/` that every core uses (only once a second
  core prepares state: AGENTS.md "extract on the second occurrence").

## Progress

- bnbloc-acp `prepared.go` (`a6f8674`, v0.2.28): `ensurePrepared` is the one
  builder and truster; the opencode routing dir (key: opencode version and
  the routing plugin's hash, which names the sandbox helper) and the npx
  entries (key: the package spec) go through it; the npx marker is gone;
  `distrustPrepared` after a failed start; a failed build is held for its
  key (P2); `check` lists every prepared directory and `--repair` clears
  failed and unfinished ones and removes unused ones (P3); a space's npm cache
  unused 30 days is removed at start (P4); `TestEveryPreparedKindHasAnUpgradeTest`
  holds every built kind to an upgrade test. Each test was checked red with
  its rule removed.
- Differs from §1: an npx entry's key is the spec only (the resolved version
  is read by check); a working entry of an older version is kept until a start
  from it fails, which distrusts it (the version npm resolves a range to can
  change without the agent breaking, and reinstalling every space at every
  publish is the cost the seeded install avoids).
- Verified on the owner's machine (2026-11-03, desktop with 0.2.28): the
  unstamped opencode directory was rebuilt once and stamped
  (`opencode 1.19.5`), `bnbloc smoke --agent acp/opencode --model
  <haiku>` passed twice, the second without a rebuild. `check` on that
  machine listed 46 directories, 8.3 GB.
- Not run: the 30-day expiry on the owner's machine (nothing is that old
  yet); Windows (the sandbox does not run there); `check --repair` on the
  owner's machine (it removes state; the owner runs it).
