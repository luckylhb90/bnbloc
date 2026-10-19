# Every tool update silently restarted the conversations that used it (2026-10-19)

Found on QA: an agent answered "what can I help you with?" in the middle of
a sign-in it had been running for an hour.

## What happened

- The bridge reuses a channel's agent session while its binding checksum
  (`core.AgentProfile.BindingSum`: instructions, skill files, MCP servers)
  is unchanged, and starts a new one when it changes.
- A stdio MCP tool was named in the profile by its binary, whose path
  carries its version (`mcp@browser-<sha256>`). Each release of a tool
  changed the checksum of every session that used it. The browser
  component was released seven times that day; each release restarted the
  member's conversation.
- The new session remembered nothing. Under the pull model its first prompt
  was the current message alone (the history is in `.room/`, which it did
  not read), and the room was not told: the restart was a log line.

## Fix

- Stdio tools start through the bridge's launcher, by name
  (`bnbloc-bridge mcp-tool --dir <tools> --name <tool> -- <args>`), never
  by their versioned binary: an update no longer changes the profile, and
  each start runs the version the desktop verified last. The turn's check
  that the gateway's pin is materialized is unchanged.
- A new session of an agent that already took part in the channel is
  handed the conversation over (`roomlog.ComposeHandover`): the older part
  condensed, the newest exchanges verbatim with its own replies, where the
  rest is (`.room/channels/<id>/recent.md`), then the message. An agent new
  to the channel keeps the pointer prompt.
- Every session replacement tells the room why (`noticeNewSession`: new
  content, a deleted message, a session that could not be continued, an
  outgrown context).

Tests: `TestToolUpdateKeepsTheSessionBinding`,
`TestToolLauncherRunsTheMaterializedBinary`, `TestComposeHandover`,
`TestReplacedSessionIsHandedTheConversation`. Verified by hand: the real
browser tool, started through the launcher under the agent's sandbox
profile (`bnbloc-vmexec confine`), answers MCP initialize and tools/list.

## Why it was not caught

- Lens: Time and lifecycle (what follows a dependency's update) and
  Defaults and fallbacks (a silent reset).
- Missing question or test: what a component's own update does to the
  sessions using it; whether a person learns their agent lost its memory.
- Added to docs/design-review.md: lens 2 asks what an update of something a
  session depends on (a tool, a skill) does to the session.
