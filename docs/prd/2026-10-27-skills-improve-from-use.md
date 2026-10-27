# Content and components improve from use: the AI proposes, the owner decides (2026-10-27)

Status: decided 2026-10-27 for skill packs (section 3); widened the same day to experts, prompt packs, assists, room packages and executable components (section 2a, decisions 5 to 9 wait for the owner).

## 1. Why

A skill pack is written once and then meets real runs. When it is wrong,
the same correction is made by hand in every room: on 2026-10-27 a release
skill told its agent to ask the person for a token and run an interactive
sign-in, which the room's connections had made unnecessary; the person
corrected it in chat, the skill's author never learned, and the next room
would repeat it. Nothing connects a run's trouble to the skill version it
wore: the job record keeps the expert, not the packs (`store.AgentJob`).

Prior art, and what it teaches: `2026-10-16-room-memory-and-evolution.md`
(withdrawn) built notes, routes and a panel into the platform; the owner
wanted the behaviour as installable content and components. Its domain
rules stand: a change to a pack is never made in place by a run; it is a
proposal against a version, with its evidence, that a person accepts.

## 2. What it is

A service component, **skill coach** (its own repository), reads the runs
that wore a pack, asks the organization's model for a revised version, keeps
the proposal, shows its diff, and on the owner's Accept saves it as the
pack's next version. The platform adds only what a component cannot do:
know what a run wore, let a component read runs as a member, and let a
component save a version as the member who clicked.

```
run wears pack P@v ──(platform: the run keeps P@v)──▶
skill coach page (owner) ──"Suggest improvements"──▶ runs of P@v the owner may read (platform primitive)
   ──▶ assist "skill-coach-propose" (component assist, the org's model) ──▶ proposal (component store)
owner reads the diff on the coach's page ──Accept (a page action, Member.Act)──▶
   platform primitive: save P@v+1 as the owner (the existing version path, org review) │ Dismiss
```

Platform (contracts only):

1. **The run keeps what it wore.** The bridge already resolves the job's
   content (`resolveJobContent`); it reports the packs in effect (id,
   version, source) with the run, kept on `agent_jobs` beside the expert,
   under the run's retention, and shown on the run's detail.
2. **`runs` primitive** for a service component that declares it: `GET
   ComponentRunsPath?pack=<id>&version=` answered for the member a page
   call attests (`component.Member`): the runs that wore that pack version
   and that this member may read through the existing run read tiers,
   newest first, at most 50, each with outcome and failure reason, tool
   call outcomes (no arguments), the request, the reply's start and the
   next human message in the same thread after the reply (text cut to a few
   hundred characters). Nothing is judged by the platform.
3. **`contentVersion` primitive**: `POST ComponentContentVersionPath` with
   the member's page action grant (`Member.Act`, one click, one save): a
   new version of a pack that member may change (`ownContent`), on a base
   version (conflict when it is not the latest), through the existing
   version path; an organization copy then waits for the organization's
   publish review as today. A component cannot save without a click.

The component (bnbloc-skill-coach): pages (the member's packs and
"Suggest improvements"; a proposal with its per-file diff, each change's
reason and links to its runs; Accept, Dismiss), its store (proposals, one
open per pack and owner, dropped after 30 days), a component assist task
`skill-coach-propose` (input: the pack's files and the runs; output:
`changes[{path, after, reason, runs[]}]`, `summary`, or `nothing` with
why), and, for an organization that wants it, a schedule (a room package's
scheduled task that opens the coach's suggestion for its packs: content).

What it does not do: change a pack by itself; read runs the member may not
read; learn from one room into another room's files.

## 2a. Everything that can improve from use

Skill packs were the first case; the same loop serves every kind of
content, and gives executable components (which no AI rewrites) a report.

| What | Evidence it learns from (signals below) | What the coach proposes | Who decides |
|---|---|---|---|
| Expert (instructions, required packs and tools) | S1 S2 S4 S5 S6 of runs that wore it | a new version: instruction changes; a skill pack or MCP tool to require or drop | its author |
| Skill pack | S1 S2 S3 S4 S6 of runs that wore it | a new version (files) | its author |
| Prompt pack, a room's quick prompts | S4 S5 of runs a prompt started (S8); requests typed by hand again and again | reworded prompts; a new prompt for a repeated request | its author; a room's owner |
| Assist (an AI feature's instructions) | S7: answers discarded or changed before they were applied | a new version of its instructions | its author |
| Room package, a room's task setup and schedules | S1 S4 S5 of scheduled and event-started runs | revised task text, schedule, recipients | its author; the room's owner |
| MCP tool, core, plugin (executable, signed) | S3: its calls refused or failing, by tool; S1 of runs that wore it | never its code: (a) a usage note for the expert or pack that wears it (content, as above), (b) a report for its maintainer with the evidence, which the member sends | the member (sends); the maintainer |
| Something new | the same instructions given by hand across runs | a new skill pack drafted from them (the existing `draft-skill-pack` contract) | the member, who saves it or not |
| Connector (policy, guide) | refused, held and failed calls | stays in the connections component's own loop (Suggest rules, AI checks), the same pattern; the coach links to it | an org admin |

**Signals** are facts the platform records; it judges none of them (the
propose task reads them):

| | Signal | Recorded today? |
|---|---|---|
| S1 | A run's outcome and failure reason | yes (`agent_jobs`) |
| S2 | Room tool calls: tool, action, outcome | yes (run diagnostics, no arguments) |
| S3 | MCP tool calls: component, tool, outcome (no arguments, no result) | no: only the attached components' names; added by the bridge |
| S4 | The next human message in the thread after the reply | yes (messages; read at evidence time) |
| S5 | The same request asked again (ask again, a retry, a regenerate) | partly (job attempts); a link from the new job to the one it redoes |
| S6 | A person's version of a deliverable right after the agent's | yes (artifact versions carry their author) |
| S7 | An assist's answer applied, changed or discarded | no: the web reports it (fields applied, not their values) |
| S8 | The quick prompt a request came from | no: the request carries the prompt's id |

The run keeps every content component and MCP tool component it wore,
with versions (today: the expert only, and MCP names). The `runs`
primitive takes any component id; `contentVersion` takes any content kind
the member may change, never an executable.

## 3. Decisions (answered 2026-10-27)

0. **An independent component**, not platform code; the platform gives
   contracts only.
1. **Evidence**: only runs the member asking may read already.
2. **When**: the owner presses Suggest improvements, or a schedule the
   organization installs; never by a platform threshold.
3. **Accept** makes an ordinary version: latest followers get it, pinned
   uses stay pinned, organization copies go through publish review.
4. **Someone else's pack**: any member may ask for a suggestion; it goes to
   the pack's author, who alone accepts.

Raised by section 2a (recommendations; to confirm):

5. **First widening**: experts with skill packs (signals S1, S2, S4, S6 are
   recorded today), then prompt packs, room packages and assists (S5, S7,
   S8), then executable components (S3).
6. **Executables are never changed**: for an MCP tool, a core or a plugin
   the coach writes a usage note into content and a maintainer report the
   member sends themselves (copied, or opened at the component's declared
   support address); it never files anything on its own.
7. **New signals record names and outcomes only** (S3, S7, S8): no
   arguments, results or text, under the runs' retention.
8. **Connectors stay with the connections component's own loop**; the
   coach links there.
9. **"Something new" is offered, not made**: a drafted pack appears as a
   proposal the member saves or drops, like any other.

## 4. Placement

| Part | Where | Why (platform reason) | What an organization swaps |
|---|---|---|---|
| The packs a run wore | platform (bridge report, `agent_jobs`) | events only the platform sees | nothing |
| Signals S3, S5, S7, S8 (names and outcomes, no text) | platform (bridge, web, `agent_jobs`) | events only the platform sees | nothing |
| `runs` primitive | platform (component contract) | identity and the attested caller; who may read which run | nothing |
| `contentVersion` primitive | platform (component contract) | consent: a pack changes only by its owner's click, through the existing version and review path | nothing |
| Proposals, diff, pages, the propose task, its prompt | component (bnbloc-skill-coach) | — | its own coach, prompt, model, rules |
| Running it on a schedule | content (a room package's scheduled task) | — | its own cadence |

Name test: the platform names no skill, task or coach. Fork: an
organization changes the component or its prompt, never the platform.
Replace: a second component with `runs` and `contentVersion` does it its
own way.

## 5. Design review

| Lens | Answer |
|---|---|
| 1 Actors | Owner (decides), users of the pack (their runs are evidence only where the asking member may read them), an org admin (reviews org copies as today), the component (proposes; saves only on the owner's click, `Member.Act`). A member who asks about someone else's pack sends the proposal to the author; they cannot accept it. |
| 2 Time | Evidence ages out with the runs' retention; a proposal against an outdated base is marked and never merged silently; proposals not decided in 30 days are dropped (a number to confirm). |
| 3 Scale and cost | 50 runs × a few hundred characters per proposal: bounded input. One proposal at a time per pack and owner. Assists are metered and rate-limited as today. |
| 4 Failure | The assist answers nothing usable: "no change suggested" with its why. Accept conflicts (someone published meanwhile): "propose again". |
| 5 Boundaries and privacy | No text from a room the owner cannot read. The proposal stores run ids, not copied conversation; opening one checks the reader's access again. The new signals (S3, S7, S8) keep names and outcomes only, never arguments, results or the text of an answer. A maintainer report leaves bnbloc only when the member sends it. |
| 6 Abuse | A room member steers a run so that the pack "learns" something harmful: nothing is applied without the owner, the diff shows every line and the runs behind it, and an org copy also passes the organization's review. A proposal cannot touch files outside the pack, add executables or change `component.json`'s `requires` without saying so in its own line of the diff. |
| 7 One owner | Versions are made only by the existing version path (the primitive calls it); run reads only by the runs' read tiers. |
| 8 Defaults | No automatic proposing (decision 2); no automatic accepting ever. |
| 9 External contracts | The two primitives are component contracts with conformance tests; the propose task's output is the component's own (a component assist answers text, the component validates it). |
| 10 Load on users and agents | Agents: none (nothing new in their context). Owners: one button on the coach's page and a review. |
| 11 Release | Platform: a nullable column for the packs (a migration only adds), two component primitives; older bridges report no packs (their runs give no evidence). The component ships on its own (its repository, a signed package, optin first). |
| 12 Observability | Proposals made, accepted, dismissed per pack (audit rows); the evidence count shown with each proposal. |
| 13 AI capability | This is the AI capability: improve-skill proposes; people decide (the house rule). |
| 14 Component or platform | Section 4: the platform keeps facts and consent; the behaviour is the component's. |

Pre-mortem: *the evidence is mostly noise* — the owner sees the runs behind
each change and dismisses; the assist may answer "nothing"; *proposals
pile up* — one open per pack and owner, dropped after 30 days; *a pack
silently changes under a room* — only through Accept, and pinned uses stay
pinned; *it reads conversations it should not* — evidence is computed per
reader, at read time.

## 6. Slices (of the end state above)

1. Platform: the run keeps every content and MCP tool component it wore,
   with versions (bridge report, store, run detail).
2. Platform: the `runs` and `contentVersion` primitives, with conformance
   tests.
3. The component (bnbloc-skill-coach, named for its first case): pages,
   store, propose task; skill packs and experts.
4. Signals S5, S7, S8; prompt packs, assists, room packages.
5. Signal S3; usage notes and maintainer reports for executables.
6. Content: the scheduled room package; "something new" drafts.
