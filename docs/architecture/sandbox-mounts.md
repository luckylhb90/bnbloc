# Sandbox mounts a component contributes

An installable component can put read-only folders into the sandbox space a turn
runs in, without the bridge, the sandbox or the agent's flow knowing what the
component is. The first user is the local-skills component
(`docs/prd/2026-10-16-local-skills.md`); a member's own component uses the same
contract.

## The contract: `bnbloc.sandbox.mounts/v1`

A component writes one file, replaced whole, in its own folder of the bridge's
state directory (in Go: `component.MountsFile`, the public contract package):

```
<BNBLOC_BRIDGE_STATE_DIR>/mounts/<component>/mounts.json
{
  "contract": "bnbloc.sandbox.mounts/v1",
  "mounts": [
    { "host": "<workspaces root>/.mounts/<component>/<folder>",
      "guest": "<an absolute path in the guest>",
      "for": "own" }
  ]
}
```

- `component` is `[a-z0-9][a-z0-9-]{0,39}`.
- `for` is `own` (the default: the agent's owner's own runs), `borrowed` (runs
  another member borrows) or `all`.
- The component keeps the folders it mounts under
  `<workspaces root>/.mounts/<component>/` (`bridge.MountsHostRoot`): the machine
  has exactly one share, the workspaces root, fixed at its creation.

## What the bridge does with it

Before every sandboxed turn (`sandboxRuntime.prepare`) the bridge reads every
component's descriptor and adds the mounts that pass to the turn's space, **always
read-only**. It trusts the component with nothing:

| Check | Refused when |
|---|---|
| descriptor | not a plain file of at most 256 KiB, or not this contract; at most 16 components and 256 mounts each |
| host | not absolute and clean, not strictly inside the component's own folder, missing, not a folder, or a link that leaves that folder once resolved |
| guest | not absolute and clean, overlapping the turn's workdir, not strictly under one of the allowed guest roots (`BNBLOC_SANDBOX_MOUNT_ROOTS`, default the member's home), or already used by another mount |
| backend | the sandbox does not advertise `readOnlyMounts` (`sandbox-protocol.md`): nothing is mounted |

A mount that fails is dropped and logged (`bnbloc.bridge.sandbox_mount_dropped`);
the turn never fails because of a component. That holds for the sandbox's own
refusal too: a guest path belongs to one space of the machine, so the second
room that asks for a mount at the same fixed path is answered `conflict`; the
bridge then asks again with the mounts that can be shared (the `perSpace` ones)
and without the others (`bnbloc.bridge.sandbox_mounts_conflict`). The contributed mounts are the
guest's alone: the core's host-to-guest path mapping stays the workspace.

The files are read every turn, so installing, changing or removing a component
needs no restart, and a turn already running keeps what it started with.

## A folder every room needs: `perSpace`

A guest path belongs to **one space** of the machine (measured 2026-10-26: a
second space asking for the same guest path is refused with `conflict`, and a
host folder mounted by several spaces is re-owned by the last one, locking the
first out). So a descriptor entry that names one fixed guest path works for one
room's space only. A folder every room needs (an agent's installed files) is
declared `"perSpace": true`:

```
{ "host": "<workspaces root>/.mounts/<component>/agents/opencode/2.1.4",
  "guest": "<under a guest root>/agents/opencode",
  "perSpace": true, "env": "BNBLOC_MOUNT_OPENCODE" }
```

- The bridge gives each space a tree of **hard links** to the files of `host`,
  under `<component folder>/.spaces/<space>/<slot>`: no second copy on disk
  (measured: three spaces ran one 20 MB program, 20 MB on disk), no dependence
  on APFS clones, the same on any file system. Links are recreated as links,
  never followed; a file that is not a file, a folder or a link refuses the tree
  (the mount is dropped, a tree made earlier stays).
- The space's guest path is `guest` + `-` + the space's name, so no two spaces
  collide. `env` (only with `perSpace`, only `BNBLOC_MOUNT_<NAME>`, one name per
  mount) is set in the space to that path: a command line says
  `$BNBLOC_MOUNT_OPENCODE/bin/opencode` and means the same in every room.
- A tree that records `host` as its source is reused untouched; a different
  `host` (a new version, which is a new folder: **a version folder is never
  changed in place**) replaces the tree of that slot whole, built beside it and
  swapped in. The space has no process left from before when it is prepared, so
  nothing runs from the tree that goes.
- Not yet: trees of spaces that no longer exist are not removed (they hold the
  inodes of old versions, nothing else).
