# A skill pack improves from use: the AI proposes, its owner decides (2026-10-27)

Status: proposed (decisions below wait for the owner).

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

```
runs that wore pack P@v ──(platform records P@v on the run)──▶ evidence of P@v
owner opens P in the Library ──"Suggest improvements"──▶ improve-skill assist(evidence, files)
   ──▶ a proposal: changed files + a reason and the runs behind each change
owner reads the diff ──Accept──▶ P@v+1 (the existing version path and org review) │ Dismiss
```

1. **The run keeps what it wore** (platform). The bridge already resolves
   the job's content (`resolveJobContent`); it reports the packs in effect
   (id, version, source) with the run, kept on `agent_jobs` beside the
   expert, under the run's retention.
2. **Evidence of a version** (platform): `GET
   /v1/content-components/{id}/evidence?version=` for the pack's owner:
   the runs that wore it that the owner may read (decision 1), newest
   first, each with its outcome and failure reason, its tool calls'
   outcomes (no arguments, as diagnostics keep them), the request, the
   reply's start, and the next human message in the same thread after the
   reply (where a correction would be). Bounded (the last 50 runs, text cut
   to a few hundred characters each). Nothing is judged here: a correction
   is the AI's reading, not a platform heuristic.
3. **Proposals** (platform, the seam the 2026-10-17 knowledge PRD named):
   `POST /v1/content-components/{id}/proposals` stores a candidate version
   (the files that change, the base version, a summary, per change a reason
   and the run ids it rests on) that is not a version: nobody wears it.
   The owner sees it on the pack's Library page as the existing per-file
   diff (`contentFileChanges`), each change with its reason and links to
   its runs; **Accept** makes it the next version through
   `POST …/versions` (base check, patch bump; a shared org copy then goes
   through the organization's existing publish review); **Dismiss** drops
   it. A proposal against a base that is no longer the latest says so and
   offers to propose again.
4. **The proposing** (content): an assist task `improve-skill` (contract in
   `internal/wire/assist_contracts.go`: input the pack's files and the
   evidence; output `changes[{path, after, reason, runs[]}]`, `summary`,
   or `nothing` with why). The Library's **Suggest improvements** button on
   a pack its owner opens runs it (decision 2); an organization that wants
   it on a schedule installs a room package whose scheduled task runs the
   same task for its packs (content, not platform).

What it does not do: change a pack by itself; read runs the owner may not
read; learn from one room into another room's files.

## 3. Decisions (for the owner)

1. **Whose runs are evidence.** Recommended: the runs the pack's owner may
   read already (rooms they are in, their own agents' runs, everything for
   an org admin of the pack's organization). Alternatives: every run of the
   organization that wore it, reduced to signals (outcome, failure reason,
   tool outcomes; no text), which teaches less and still crosses rooms; or
   full text organization-wide (refused: it shows room conversations to
   someone outside the room).
2. **When a proposal is made.** Recommended: when the owner presses
   **Suggest improvements** (an assist, metered as assists are), plus the
   optional scheduled room package. Alternative: the platform starts one by
   itself after N refused tool calls or failed runs of a version (a
   threshold nobody chose yet, and a cost nobody asked for).
3. **Accepting never skips review.** An accepted proposal is an ordinary
   new version: members who follow the latest get it; a pinned use stays
   pinned; an organization copy waits for the organization's publish
   approval when the organization requires it. Recommended as is.
4. **Who receives proposals for a pack someone else wrote** (an org
   pack, a colleague's): its author (the row's owner). A member who only
   uses it can press Suggest improvements too; the proposal then goes to the
   author, who decides (a member cannot change someone else's pack).
   Recommended.

## 4. Placement

| Part | Where | Why (platform reason) | What an organization swaps |
|---|---|---|---|
| The packs a run wore | platform (bridge report, `agent_jobs`) | events only the platform sees | nothing |
| Evidence read for the owner | platform (gateway route, read tiers of runs) | identity and the attested caller; who may read which run | nothing |
| Proposals: kept, shown as a diff, accepted into a version | platform (content components) | consent: only the owner changes a pack, through the existing version and review path | nothing |
| `improve-skill`: reading the evidence, writing the changes and reasons | content (an assist package) | — | its own prompt, model, rules |
| Running it on a schedule | content (a room package with a scheduled task) | — | its own cadence, scope |

Name test: no skill, product or task name in the platform beyond the assist
contract's name. Fork: an organization changes the assist or the schedule
package, never the platform. Replace: a second proposer (another assist, a
component) posts to the same proposals route.

## 5. Design review

| Lens | Answer |
|---|---|
| 1 Actors | Owner (decides), users of the pack (their runs are evidence only where the owner may read them, decision 1), an org admin (reviews org copies as today), the assist (proposes, never applies). A member who presses Suggest on someone else's pack sends a proposal to the author; they cannot accept it. |
| 2 Time | Evidence ages out with the runs' retention; a proposal against an outdated base is marked and never merged silently; proposals not decided in 30 days are dropped (a number to confirm). |
| 3 Scale and cost | 50 runs × a few hundred characters per proposal: bounded input. One proposal at a time per pack and owner. Assists are metered and rate-limited as today. |
| 4 Failure | The assist answers nothing usable: "no change suggested" with its why. Accept conflicts (someone published meanwhile): "propose again". |
| 5 Boundaries and privacy | No text from a room the owner cannot read. The proposal stores run ids, not copied conversation; opening one checks the reader's access again. |
| 6 Abuse | A room member steers a run so that the pack "learns" something harmful: nothing is applied without the owner, the diff shows every line and the runs behind it, and an org copy also passes the organization's review. A proposal cannot touch files outside the pack, add executables or change `component.json`'s `requires` without saying so in its own line of the diff. |
| 7 One owner | Versions are made only by the existing version path; diffs only by `contentFileChanges`; run reads only by the runs' read tiers. |
| 8 Defaults | No automatic proposing (decision 2); no automatic accepting ever. |
| 9 External contracts | The assist output contract, versioned with the others (`bnbloc content contracts`). |
| 10 Load on users and agents | Agents: none (nothing new in their context). Owners: one button and a review. |
| 11 Release | Additive: a nullable column for the packs, a table for proposals (a migration only adds), the route and the Library section; the assist ships as content. Runs before the release have no packs and give no evidence. |
| 12 Observability | Proposals made, accepted, dismissed per pack (audit rows); the evidence count shown with each proposal. |
| 13 AI capability | This is the AI capability: improve-skill proposes; people decide (the house rule). |
| 14 Component or platform | Section 4. |

Pre-mortem: *the evidence is mostly noise* — the owner sees the runs behind
each change and dismisses; the assist may answer "nothing"; *proposals
pile up* — one open per pack and owner, dropped after 30 days; *a pack
silently changes under a room* — only through Accept, and pinned uses stay
pinned; *it reads conversations it should not* — evidence is computed per
reader, at read time.

## 6. Slices (of the end state above)

1. The run keeps its packs (bridge report, store, run detail shows them).
2. Evidence route + proposals (store, routes, Library diff with Accept and
   Dismiss).
3. The `improve-skill` contract and assist package, the Suggest button.
4. The scheduled room package (content).
