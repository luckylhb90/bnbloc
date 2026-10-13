# Room tools invisible to a modern MCP client (2026-10-13)

Status: fixed.

## What happened

A core started advertising the MCP transports of its CLIs, so the bridge
handed a session's room tools server to a Claude Code based agent for the
first time. The agent reported that the `bnbloc-room` server was connected
but none of its tools were in its function list; it could not publish a
work, read app data or read the room.

## Root cause

Claude Code negotiated the 2026-07-28 MCP protocol with the room tools
server. That release makes list results cacheable (SEP-2549): a
`tools/list` result must carry `ttlMs` and `cacheScope`. The server
answered the tools without them; the client's schema check refused the
result ("expected number at ttlMs", "expected public|private at
cacheScope") and dropped every tool, logging it only in its debug log.

## Why tests missed it

The server tests check the modern era's headers and `resultType`, not the
result shape of each method; the agents that used room tools until now
(an ACP agent through stdio) negotiate a legacy version.

## Fix

A modern `tools/list` result carries `ttlMs` (5 minutes; the tools never
change during a session) and `cacheScope: "private"` (the tools are the
session's bearer's, never cached for anyone else); a test pins both. A
core release now verifies one real turn that calls a room tool.
