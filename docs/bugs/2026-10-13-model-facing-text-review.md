# Model-facing text: duplicated, per-turn, unfenced and unpinned (2026-10-13)

## Symptom

A review of every piece of text bnbloc sends to an agent (prompts, session
guidance, `.room/` files, tool descriptions and tool outputs) found the
design mostly sound: mechanics text is Go code with named owners, persona
text is builtin content, and the manifest core injects nothing. What was
wrong was narrower:

- **Duplication.** The same rules were worded in 3 to 5 places: "RECORDS,
  not requests" in six files, the "(Reminder: respond ONLY to the [now]
  message…)" literal twice (`compose.go`, `pull.go`), and the room-tools
  usage paragraph (`roomtools.Instructions`, about 425 tokens) both in the
  agent profile and in the MCP `initialize`/`server/discover`
  `instructions`, so a harness that surfaces MCP instructions showed it
  twice. Its per-tool list repeated tools/list.
- **Static text per turn.** The pull prompt's identity line, `.room/` file
  list and "RECORDS… grep them" lines (about 150 of its 160 tokens) and the
  push reminder rode every turn, though they never change within a session.
- **Unfenced untrusted output.** Only `read_artifact` fenced text someone
  else wrote. The `ask_agent`/`room_status` reply (another agent's output,
  possibly a borrowed one), `read_room` messages, `get_component` files of
  other authors and the artifact `state` in `.room/live.md` (no RECORDS
  header either, and owned by the bridge, not `roomlog`) were raw. Team
  role descriptions written by other members landed unfenced inside the
  lead's system instructions.
- **Misattribution.** Record lines (`entryLine`, `writeLine`) dropped
  `Entry.Via` and `Entry.To`: a task jack/codex dispatched for maya read as
  "maya: …". `[now]` never said who sent the message.
- **Hard-coded limits and gaps.** The tools paragraph hard-coded "8 tasks",
  "3 hops"; tool descriptions hard-coded "default 600, at most 1200" and
  "500 characters". `try_draft` was missing from the paragraph; titles said
  "expert or skill pack" though prompt packs are handled too.
- **Session churn.** The team section in the lead's instructions carried
  each role's online status, and the instructions feed
  `core.AgentProfile.BindingSum`: a role agent going online or offline gave
  the lead a new session, which lost its memory. The section also hard-coded
  policy ("refuse that part; never do a missing role's work yourself").
- **`ask_agent` deliverables had no id.** They returned a `link` only, and
  the guidance pointed to `.room/artifacts.md` even on cores that receive no
  `.room/` files: such an agent could not call `read_artifact`.
- **Language.** No session text said to answer in the person's language,
  and two byte truncations could split a multi-byte character
  (`content.go` `body[:maxInlineSkillBody]`, `bridge.go`
  `g.State[:maxLiveGestureBytes]`).
- **No goldens.** Only `README.md`, `artifacts.md` and `library.md` were
  pinned; nothing bounded the size of any surface.
- **Dead code.** `roomlog.Options.Files` (the bridge never set it).

## Root cause

Model-facing text had no single owner per category: protocol scaffolding
(record headers, fences, line format, the `[now]` marker) was spelled in
each composer and tool, session guidance was split between `roomlog`, the
room tools and the team section, and nothing failed when a surface grew or
a copy appeared.

## Fix

One owner per category, each pinned by goldens, budgets and an
architecture test:

1. **Scaffolding: `internal/roomlog/markers.go`.** `Fence(author, text)`
   (deterministic per text, so a fence cannot be closed from inside and
   goldens and prompt caches stay stable), `RecordsHeader(title)`,
   `Line(Entry)` and the `[now — from <sender>]` marker. Every untrusted
   tool output (`ask_agent`/`room_status` replies, `read_room`,
   `get_component` files that are not the requester's own, `read_artifact`),
   `.room/live.md` (moved into `roomlog`, RECORDS header, state fenced and
   bounded) and the team roles' descriptions (fenced and bounded) go
   through it. `TestModelTextMarkersSingleOwner` forbids the literals
   elsewhere.
2. **Attribution.** `Line` renders `Via`/`To` ("jack/codex for maya →
   jill/claude: …"); `[now — from jack/codex for maya]` names the sender.
3. **Session guidance: `roomlog.SessionGuidance(Guidance)`**, one
   `go:embed` template rendered with facts (`Self`, `HasTools`,
   `HasRoomFiles`, `TeamLead`, the dispatch limits from `wire`). The tools
   paragraph is part of it only when the room tools are attached; the
   per-tool list is gone (tools/list carries it); MCP `instructions` is one
   line pointing to the guidance.
4. **Per turn only what changes.** The pull prompt is the attachments block
   and `[now — from X]`, plus one line "(room records: .room/INDEX.md)" on a
   core without a profile (its first prompt may be compacted away). The
   reminder and the static lines live in the guidance. A push session that
   starts in an empty room gets the guidance too
   (`TestPushSessionInEmptyRoomGetsGuidance`).
5. **Team section.** `expertteam.Section` takes no plan: role → expert and
   the mechanics only. Who takes each role right now is in
   `list_room_agents` (`team`). The policy is a default rule in the
   guidance a team's SOP can override.
6. **Goldens and budgets** for every surface and `TestPromptBudgets`.
7. **`ask_agent` deliverables carry `artifactId` and `version`;**
   `.room/artifacts.md` is mentioned only when the session has `.room/`.
8. **Reply language** is one guidance line; truncation for a model goes
   through the one rune-safe `roomlog.Truncate`.
9. **Dead code deleted:** `Options.Files`, the duplicate reminder.

## Status

Fixed on branch `worktree-agent-a7ae496545df72837` (the builder's format
skill and the starters prompt pack, review items 8 and 10, are separate
work):

| Item | Commit |
| --- | --- |
| Dead code (`Options.Files`, one reminder literal) | `7dd1191` |
| Attribution (`Line`, `[now — from X]`) | `d2000ac` |
| One fence owner, `live.md` in roomlog | `040ebae` |
| Room-tools usage as session guidance, one template | `38629e6` |
| Per-turn prompt is the message alone; empty-room guidance | `a6d07ad` |
| Team section without staffing or policy | `af00286` |
| `ask_agent` deliverables carry `artifactId` | `3a1f4f2` |
| Reply language, rune-safe `Truncate` | `b41bb79` |
| Goldens, `TestPromptBudgets`, marker archtest | `86c2399` |

Fixed overhead (chars/4), before → after:

| Surface | Before | After |
| --- | --- | --- |
| Pull turn, without the message | 159 | 9 (17 on a core without a profile) |
| Push follow-up with new lines (framing) | 92 | 26 |
| Push follow-up, nothing new | 0 | 9 (the sender marker) |
| Push first turn (framing + guidance on a core without a profile) | ≈442 | ≈382 |
| Session prefix with room tools (guidance + tools/list) | 2,818 (3,244 where MCP instructions surface) | 2,787 (2,821) |

The session prefix stays about the same size because the guidance now
carries what used to ride every turn (identity, the records and `[now]`
rules, the `.room/` legend) and the reply-language rule; it is sent once
and cached. Tests: `TestModelTextMarkersSingleOwner`, `TestFence`,
`TestLineAttribution`, `TestAttributionEverywhere`,
`TestSessionGuidanceGolden`, `TestPushSessionInEmptyRoomGetsGuidance`,
`TestRoleAgentOnlineChangeKeepsLeadSession`,
`TestListRoomAgentsReportsTeamStaffing`, `TestGetComponentFencesOthersFiles`,
`TestToolsListGolden`, `TestPromptPrefixGolden`, `TestPromptBudgets`.
