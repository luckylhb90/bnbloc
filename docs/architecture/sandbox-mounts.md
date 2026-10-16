# Sandbox mounts a component contributes

An installable component can put read-only folders into the sandbox space a turn
runs in, without the bridge, the sandbox or the agent's flow knowing what the
component is. The first user is the local-skills component
(`docs/prd/2026-10-16-local-skills.md`); a member's own component uses the same
contract.

## The contract: `bnbloc.sandbox.mounts/v1`

A component writes one file, replaced whole, in its own folder of the bridge's
state directory:

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
the turn never fails because of a component. The contributed mounts are the
guest's alone: the core's host-to-guest path mapping stays the workspace.

The files are read every turn, so installing, changing or removing a component
needs no restart, and a turn already running keeps what it started with.
