# Fewer, sharper room tools as the platform grows (2026-10-20)

Status: proposed. Owner: the room tools (`services/bridge/roomtools`), the
session guidance (`roomlog.SessionGuidance`), the prompt budgets
(`services/bridge/prompt_test.go`).

Every session starts with the room tools' list and the session guidance:
14 tools, about 2,630 tokens of tool list and 745 of guidance (budget
3,090). The prefix is cached, so its cost is small; what grows with every
tool is the agent's choice: more tools, longer descriptions, more wrong or
missed calls. This PRD keeps the list short and each description sharp as
capabilities are added.

## 1. Why

- The owner, 2026-10-20: "未来可能工具越来越多，那要怎么办？"
- Today: `room_setup` (~380 tokens of descriptions) already holds one
  domain's actions; the five content tools (`list_components`,
  `get_component`, `try_draft`, `save_component`, `publish_component`,
  ~420) are one domain split five ways. Each addition so far raised the
  budget (2940 → 3060 → 3090 on 2026-10-20).
- Nothing records which tools agents call, so nothing tells which tools
  earn their place.

## 2. What it is (the end state)

### 2.1 One tool per domain, details on demand

- A tool is a domain with an `action` (as `room_setup` is): its description
  says in one sentence what the domain covers and lists the action names;
  each action's parameters and rules are in `room_help <tool>` (already the
  pattern for long details).
- The five content tools become one `components` tool (list, get, try,
  save, publish). Other merges follow the data (§2.3).

### 2.2 Tools that appear when they apply

- A tool whose domain is absent from the room is not listed: `app_data`
  only in a room with an app; `ask_agent` / `list_room_agents` only with
  another agent in the room; a component's MCP tools only when installed
  (already so).
- A core that supports MCP `tools/list_changed` gets the list changed
  when the room changes (an app is made, an agent joins); one that does
  not keeps the full list (today's behaviour).
- Groups an agent rarely needs (authoring components) can be listed in
  `room_help` and added on request on cores that support list changes.
  Built only when §2.3 shows they are rarely used.

### 2.3 Measured, not guessed

- The bridge records each tool call (tool, action, ok or refused, no
  arguments) on the run record beside the tools the session had
  (`wire.RunDiagTools`); an admin view lists calls and refusals per tool
  over time.
- The budget test keeps the session prefix at or under its budget; a new
  tool or action must fit it by merging or shortening, never by raising
  it.
- An agent task suite (shared with
  `docs/prd/2026-10-20-no-built-in-content.md` §2.6) checks that after a
  merge or a hidden tool, agents still find and use the capability.

### 2.4 AI

The tool-use records feed a monthly AI summary for platform admins (a
gateway schedule run over the records): tools rarely called, actions often
refused or misused, with the likely cause and a proposed merge or
rewording. People decide.

## 3. Design review

| Lens | Answer |
|---|---|
| Load on agents | The point: fewer tools, one-line descriptions, details on demand; the prefix only shrinks. |
| One owner | One tool per domain; one budget test; one record of calls. |
| External contracts | `tools/list_changed` used only where the core supports it; others unchanged. |
| Observability | Tool calls and refusals per tool, per run. |
| Release and compatibility | Pre-launch: the content tools are replaced by `components`, no aliases. |
| AI capability | The admins' summary of tool use (§2.4). |

## 4. Pre-mortem

1. **Merged tools are harder to call right.** Each action keeps typed
   parameters; refusals name the right action; the task suite checks.
2. **A hidden tool is needed.** Only tools whose domain is absent are
   hidden; the room changing re-lists them.
3. **Records leak content.** Tool and action names and outcomes only.

## 5. Build order

1. Record tool calls; the `components` merge; the budget rule.
2. Situation-dependent listing with `tools/list_changed`; the admins' AI
   summary.
