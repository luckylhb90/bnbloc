# Fewer, sharper room tools as the platform grows (2026-10-20)

Status: slice 1 built (§5). Owner: the room tools (`services/bridge/roomtools`), the
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

1. Record tool calls; the `components` merge; the budget rule. **Built
   2026-10-20.**
2. Situation-dependent listing with `tools/list_changed`; the admins' AI
   summary.

### Slice 1, as built

- **Records.** The room tools server records every `tools/call` of a
  session bound to a job (`roomtools/calls.go`): tool, action (only a value
  of the tool's `action` enum), outcome `ok` | `refused` (the tool's own
  check, a gateway 4xx) | `error` (a 5xx, a transport failure, an
  unreadable answer). The job's outcome report takes them once, beside the
  session's MCP tools in `wire.AgentRunTools` (`calls`, `callsDropped`;
  at most `wire.MaxRunToolCalls` = 200 a run). Org admins read sums per
  tool and action at `GET /v1/tenant/jobs/tool-calls?from=&to=` (at most
  `wire.MaxToolUseRuns` = 5,000 runs a read, saying where it stopped),
  shown under Team, Runs, "Room tool use". A per-tool view over time
  (a chart) is left for the AI summary's slice: the read takes any range.
- **`components`** (list get try save publish) replaces `list_components`,
  `get_component`, `try_draft`, `save_component` and `publish_component`;
  each action keeps its parameters, checks and result, and a parameter of
  another action is refused naming the action that takes it. `room_setup`'s
  brief has the same shape (one sentence and its actions; its parameter
  notes moved to `room_help room_setup`).
- **Budgets.** tools/list 2,351 → 1,787 tokens (budget 2,630 → 1,790), the
  session prefix 3,090 → 2,520 (budget 3,090 → 2,520); guidance 740 → 734.
  `TestPromptBudgets` states the rule: never raised for a tool or action.
- The default content's expert builder and interactive apps packages name
  the `components` actions (their minor versions bumped).
