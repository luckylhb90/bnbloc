# Bug: room mention, routing, and stale-toast issues

Status: done
Commits: f6cf16c, 18fb84e, fe41787, 74a871e, 56e815a

A cluster of composer/routing bugs found while testing multi-agent rooms.

## @-list showed a generic "Agent" instead of specific agents (`f6cf16c`)

The mention list surfaced a single generic entry rather than each addressable
agent. Fixed to list concrete `owner/provider` handles (e.g. `maya/codex`,
`jack/claude-code`) with a robot glyph.

## False "No agent in this room yet" toast (`18fb84e`)

Root cause: the browser send path still called an obsolete `say` route to drive
the agent, which 409'd (no session) and popped a misleading toast. With
recipient routing the bridge drives agents; the `say` path was dead. Removed it.

## In-text @agent did not override the composer selector (`fe41787`)

Repro: selector set to claude-code, message typed `@jack/codex …` → went to
claude-code (the selector), contradicting the @. Per design, an in-text
`@owner/provider` naming a known room agent now wins over the selector.

## @-list dropped other members' agents past the picker cap (`74a871e`)

Root cause: the mention list was `[people, my agents, others' agents]` then
sliced to 6, so another member's borrowable agents fell off the end. Lead the
list with agent handles (mine + others) before bare people, and raise the cap.

## Mention autocomplete broke on the handle separator (`56e815a`)

Root cause: `detectMention` captured only `\w*`, so autocomplete died the moment
the user typed `/` (and never matched providers with `:`/`-`). Broadened the
token to `[\w/:.-]`. Also replaced the fragile recipient RegExp interpolation
with an exact @-token scan so handles with regex-special characters can't break
or falsely match.
