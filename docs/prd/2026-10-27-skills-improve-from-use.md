# A skill pack improves from use: the AI proposes, its owner decides (2026-10-27)

Status: decided 2026-10-27 (section 3): a component of its own; slice 1 building.

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

## 4. Placement

| Part | Where | Why (platform reason) | What an organization swaps |
|---|---|---|---|
| The packs a run wore | platform (bridge report, `agent_jobs`) | events only the platform sees | nothing |
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
| 5 Boundaries and privacy | No text from a room the owner cannot read. The proposal stores run ids, not copied conversation; opening one checks the reader's access again. |
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

1. Platform: the run keeps its packs (bridge report, store, run detail).
2. Platform: the `runs` and `contentVersion` primitives, with conformance
   tests.
3. The component: repository, package, pages, store, propose task.
4. Content: the scheduled room package.
