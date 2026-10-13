# Sessions per channel, one workspace per agent (2026-10-13)

Status: approved by the product owner (2026-10-13); landed (commit after the record, 2886020).

Builds on `docs/bugs/2026-10-13-agent-loses-its-workspace.md` (an agent
keeps one workspace across its sessions).

## Problem

A drive slot is (room, core, provider, execution kind): every channel of a
room shares one agent session, so an agent's conversation memory mixes the
room's channels. A question in `#design` is answered with the context of
`#ops`, and a long channel crowds out a short one's history.

## Decision

- **A session is per channel:** (room, channel, core, provider, execution
  kind). Each channel's conversation is its own session's memory.
- **The workspace stays per agent:** (room, core, provider, execution
  kind), shared by all of that agent's channel sessions, as the workspace
  fix established. Files an agent writes from one channel are there in the
  next channel's turn.
- **Turns on one workspace never overlap.** The drive lock stays per
  workspace (the agent slot), not per session: two channels queue behind
  each other instead of running one agent concurrently in one working
  tree. Per-session locking would let two harnesses write one tree at
  once.
- **What the sessions of one workspace share** is the workspace's, not a
  session's: the `.room/` records tree and its pull state, the deliverable
  copies, the file listing deliverable publishing diffs against, and the
  sandbox fence's baseline. A fence breach drops every session of the
  workspace; a fresh session resets a sandbox workdir's protected paths
  only when no other session is bound to it (they are bnbloc's own,
  fence-verified, and another session wears them).
- **`.room/` is the room's records, the same for every channel session:**
  every channel an agent may read (`roomlog.AgentChannels`, never the
  people chat), as today. Because the tree is shared, it keeps every line,
  the agent's own replies and the current message included (the per
  session filters that dropped them would hide one channel's replies from
  another channel's session, whose memory does not hold them). The turn
  answers the `[now]` message alone; records are background.
- **Persisted bindings:** only workspace bindings persist (per agent, no
  channel). Sessions are not persisted: after a restart each channel starts
  a new session in its agent's workspace.

## Acceptance

- Two channels → two sessions, one workspace.
- Files written from channel A are visible in channel B's turn.
- Channel A's conversation is not channel B's session history (B's session
  receives only B's messages as turns).
- Turns on the same workspace never overlap.

## Known limits

The agent config a session wears (instructions file, skills) lives in the
shared tree: two channel sessions wearing different experts see the files
the most recently created one wrote. A core that delivers instructions
natively per session (an ACP adapter's `meta`/`env` mapping) keeps them
apart.
