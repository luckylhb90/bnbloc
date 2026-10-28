# No stdio MCP tool was attached to any desktop agent since 2026-10-23 (2026-10-28)

Status: fixed (see Fix).

## Repro

A member installs the browser component (a stdio MCP tool; the desktop shows
it ready). Their agent is asked to test a page: it says no browser tool is
available and tries to install Playwright itself. The run's record
(`diagnostics.tools.mcp`) lists no tools and no missing ones. Every stdio tool
is affected (browser, media tools, a CLI tool), on every core and both host
and sandboxed drives; http tools are not.

## Root cause

Since "Several accounts in one app, all live" (2026-10-23) the desktop keeps
the tool binaries in one shared, content-addressed store
(`<cache>/mcp-tools/`) and each member's `index.json` in the member's own
directory (`<member>/mcp-tools/`), and names that member directory to the
bridge as `BNBLOC_MCP_TOOLS_DIR`. The bridge's one binary lookup
(`materializedTool`) still required the binary to lie inside that directory,
so every index entry was refused as "its binary is outside the tools
directory". The tools are offered (installed, not required by content), so the
refusal is only logged (`bnbloc.bridge.mcp_offered_skipped`) and the turn runs
without them: nothing told the member or the run's record.

## Fix

The two directories are named separately: `BNBLOC_MCP_TOOLS_DIR` stays the
member's index directory and `BNBLOC_MCP_TOOLS_STORE` (new, set by the
desktop) names the shared store. `materializedTool` requires a binary inside
the store (the index directory when no store is named, as a standalone bridge
or an older desktop keeps both in one place), so an index entry still cannot
point the bridge at an arbitrary file. The launcher (`bnbloc-bridge mcp-tool`)
takes the store too (`--store`), and a test builds the desktop's layout (index
in one directory, binaries in another).

## Why it was not caught

Lens 11 (release) and 1 (actors): the desktop change moved where the files
live, and its tests checked the desktop's index; the bridge's tests built
their own fixture with both in one directory, so no test held the two sides'
layouts together. An offered tool that cannot be attached is quiet by design
(an unmet *required* tool is reported), so the runs looked normal. The E2E
room's run (2026-10-28) found it: its agent reported no browser tool.

Question added to `docs/design-review.md` lens 11: "When one side changes
where it writes files another process reads, does a test build that layout
from the writer's own code or constants, not a fixture the reader made up?"
