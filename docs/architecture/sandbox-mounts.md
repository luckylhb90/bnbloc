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
    { "host": "<mounts root>/<component>/<folder>",
      "guest": "<an absolute path in the guest>",
      "for": "own" }
  ]
}
```

- `component` is `[a-z0-9][a-z0-9-]{0,39}`.
- `for` is `own` (the default: the agent's owner's own runs), `borrowed` (runs
  another member borrows) or `all`.
- The component keeps the folders it mounts under
  `<mounts root>/<component>/` (`bridge.MountsHostRoot`): the machine
  has exactly one share, the workspaces root, fixed at its creation.
- **The mounts root is the desktop's to name, not the bridge's to guess.** The
  desktop makes each plugin's folder (`skills_provider::mirror_root`, handed to
  the plugin as its `sidecar.mountRootEnv`) at
  `<workspaces root>/.mounts/<account>/<plugin>`, the account layer so two
  accounts' plugins never share a folder, and tells the bridge the folder that
  holds them (`BNBLOC_SANDBOX_MOUNTS_ROOT`, `SandboxConfig.MountsRoot`). A bridge
  no desktop runs falls back to `<workspaces root>/.mounts`. (The bridge once
  derived `<workspaces root>/.mounts/<component>` itself, without the account,
  from 2026-10-23, when the desktop gained the account layer, until 2026-10-28:
  every component's mount was dropped in the real app, local skills' included,
  with only a log line to show it. Every test put its folders where the bridge
  looked. `TestSandboxPrepare_liveWithTheDesktopsFolderLayout` and the desktop's
  `the_folder_a_plugin_writes_its_mounts_in_is_directly_under_the_one_the_bridge_is_told`
  now pin the two sides together.)
- A mount the bridge could not apply is not only logged: the notice of a failed
  sandboxed drive of that space says which component's mount was not applied and
  why.

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
{ "host": "<mounts root>/<component>/agents/opencode/2.1.4",
  "guest": "<under a guest root>/agents/opencode",
  "perSpace": true, "env": "BNBLOC_MOUNT_OPENCODE" }
```

- The bridge gives each space a tree of **hard links** to the files of `host`,
  under `<component folder>/.spaces/<space>/<slot>-<source>`: no second copy on
  disk (measured: three spaces ran one 20 MB program, 20 MB on disk), no
  dependence on APFS clones, the same on any file system. Links are recreated as
  links, never followed; a file that is not a file, a folder or a link refuses the
  tree (the mount is dropped, a tree made earlier stays).
- The space's guest path is `guest` + `-` + the space's name, so no two spaces
  collide. `env` (only with `perSpace`, only `BNBLOC_MOUNT_<NAME>`, one name per
  mount) is set in the space to that path: a command line says
  `$BNBLOC_MOUNT_OPENCODE/bin/opencode` and means the same in every room.
- The source is part of the tree's name, so a new version (**a version folder is
  never changed in place**: a new version is a new folder) is a new host path:
  the sandbox converges the space's mount to it on the space's next turn, and the
  old tree of that slot is removed once the new one is in place. The space has no
  process left from before when it is prepared, so nothing runs from the tree
  that goes. A tree made of the same source is reused untouched.
- **Trees of deleted spaces are swept**: at most every 30 minutes the bridge lists
  the spaces of every machine it uses and, only if every list was read, removes
  `<component>/.spaces/<space>` for a space not listed and idle for an hour (a
  space being created has its trees before the sandbox lists it).

### A fixed guest path in every room: `privateMounts`

A mount at one fixed guest path (local skills' skill folders) must reach every room,
and a plain bind mount cannot do it: the guest is one machine with one mount
namespace, so a path can hold one thing and its root is owned by one space
(measured: a second room asking for the path was answered `conflict`, so only the
first room had the folder). A backend that advertises **`privateMounts`**
(`sandbox-protocol.md`) makes a read-only shared mount private to its space: the
sandbox does not bind it in the machine's namespace but in a mount namespace of
each exec of that space (`guest-exec`, a table per space user), so the same path
means each room's own folder and no room sees another's, an own-only mount is
never visible to a borrowed run, and nothing of the descriptor changes. Verified
against the real sandbox: three rooms read the folder, each read-only, a borrowed
run and the machine itself see nothing, a changed folder is followed on the next
turn.

Without the capability (an older sandbox, another backend) the first room gets the
mount and the others are refused; the bridge then runs their turn without it
(above), and `perSpace` is how a component gets a folder to every room there.
A private mount leaves an empty directory at its path in the guest (the path's
name, never its content).
