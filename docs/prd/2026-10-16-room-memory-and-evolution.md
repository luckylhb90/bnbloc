# Room memory, knowledge and evolution (2026-10-16)

Status: design agreed with the product owner (2026-10-16) on the points under
"Decisions"; **not built**. Stage 1 is next. The design review below is checked
against the code where it says so and marked "to verify at build" where it is
not.

## Why

A room is a project: people and their agents work on it for weeks. What the
project has decided, how it is done here and what went wrong last time lives
only in the message history and in each agent core's own private memory, which
differs per core, is invisible to the members and does not follow the room. So
every new session, member or agent starts from the history's tail, and the same
corrections are made again.

This adds one store of **notes bound to the room**, a read path that hands them
to any agent as plain files, a write path in which an AI curator proposes and
people review, and a way for a lesson that proved itself in one room to become
something the whole organization can adopt.

## Decisions (owner, 2026-10-16)

- **A room is a project and memory belongs to it.** No personal memory in this
  design (a member's memory across rooms is a later, separate question).
- **One store, three kinds of content** (knowledge, memory, lessons), told apart
  by type and lifecycle, not three systems.
- **Humans review, the curator proposes.** The nominations to the organization
  are made by the platform's AI curator, not by a person writing them, and a
  person reviews each one. Room agents get no new tool and no new rule to do it
  ("light agent load").
- **Promotion to the organization is possible** for a lesson that is general,
  through the Library's existing organization scope and approval.
- **Cross-room discovery of general lessons is not built now, but the design
  leaves its seam** (below). It would read many rooms' content without their
  members' consent, so it is an organization admin's opt-in, off by default,
  when it is built.

## 1. What is stored

| Type | What it is | How it changes | Written by |
|---|---|---|---|
| `brief` | The project in one page: goal, conventions, current state, key decisions. Always loaded. | A few revisions, human and AI | a person; the curator proposes edits |
| `knowledge` | A decision (with its reason), a glossary entry, a convention, a pointer to a deliverable | Slow; a new note supersedes the old one | a person; the curator proposes |
| `memory` | A dated fact distilled from the conversation ("2026-10-16: decided X because Y", source: message 123) | Append-only; compacted into a weekly summary | the curator |
| `lesson` | How this project does something ("the weekly report goes through app X, format Y") | Needs independent evidence; can be superseded | the curator proposes |

- **Memory is an index over history, not a copy.** Messages are already stored
  and searchable. A note holds the distilled conclusion and a reference to its
  source (a message sequence number or a deliverable version), never the text it
  can be re-read from.
- **A note is small and about one thing.** Fields: `type`, `title`, `body`
  (Markdown, bounded), `status` (`active`, `superseded`, `archived`), `supersedes`
  (the note it replaces), `sources` (message seqs, artifact versions),
  `author` (a member, or the curator), `pinned`, `created_at`, `updated_at`,
  `verified_at`.
- **Tables** (MySQL, tenant and room scoped like every room record): `room_notes`
  (current state), `room_note_revisions` (append-only: who changed what, for
  rollback and audit), `room_note_proposals` (below). The store is the
  authority; every index is rebuildable from it.
- **Retention and deletion.** A room's notes are deleted with the room. Deleting
  or expiring a source message does not delete the notes distilled from it (the
  derivation is not tracked exactly): the note's source then reads "no longer
  available", it is marked unverified and ranks lower, and any member may delete
  a note. A handoff export carries the notes with the room (the project moves
  with its memory). *These two defaults are to be confirmed with the owner at
  build.*

## 2. How agents read it (where the effect is decided)

The platform materializes the room's notes into the workspace as `.room/memory/`,
the way it already writes `.room/library.md` and `.room/apps.md`. For an agent it
is plain files; no tool is added.

- **Tier 0, `BRIEF.md`:** the brief, always, with a fixed size cap.
- **Tier 1, `INDEX.md`:** one line per active note (title, type, one-line hook,
  updated), capped in lines; beyond the cap, grouped by type with counts and the
  highest-ranked first.
- **Tier 2, `notes/<id>.md`:** each note's body, opened on demand; the long
  tail is found with plain file search. A note someone else wrote is fenced as
  data (`roomlog.Fence`), like every other text a file hands an agent.
- **What enters the index**, in order: pinned, then recently updated or
  recently verified, then by type weight; superseded and archived notes never.
  An unverified or stale note is shown with its date, never as current fact.
- **The session guidance gains one line** pointing to `.room/memory/INDEX.md`
  (in the `.room/` legend, inside the prompt budget test). Nothing else is added
  to any prompt.
- **Retrieval has one owner**: `internal/notes` (`Select(room, query, limit)`
  returns note ids with scores). The materializer and the curator are its only
  callers; no agent-facing search path exists (the rule for message search holds:
  search serves human memberships only). Today it is the notes table's FULLTEXT
  index (ngram, like the message index) plus aliases the curator writes for each
  note. The message search owner (`internal/search`) is for messages and stays
  as it is: notes are another corpus with other semantics (ranked, status-aware).
- **Swapping the retrieval later** changes the implementation behind `Select`
  only: an OpenSearch index of notes (the cluster the message index can already
  use) with a vector field and a hybrid query, rebuilt from the notes table with
  a cursor-based reindex like `bnbloc search reindex`. It needs an embedding model
  setting next to the assist model's (not designed here) and an OpenSearch with
  vector support (to verify). The trigger is measured, not felt: a small set of
  "question → the note that should be found" built from real rooms, and a recall
  threshold below which the swap is due.

## 3. How notes are written

- **A person saves.** A message's menu has "Save to memory": the body is
  prefilled from the message (the curator may tighten it) and the person edits a
  sentence. Notes can be edited, pinned, superseded and deleted in the room's
  "What this project knows" panel. This path needs no AI.
- **The curator proposes, on a nightly sweep.** One sweep a night, owned by one
  replica at a time (an advisory lock, like the retention worker), walks the rooms
  that opted in and issues one run per room through the gateway's schedule-run seam
  (the organization's assist model and budget, audited as `assist`). A run reads
  what happened since the room's cursor (new messages, deliverables published,
  schedule results, corrections) and files **proposals**: add a note, supersede one
  (the proposal must name what it replaces), compact a run of memory notes, or edit
  the brief. A proposal carries its evidence (source refs), not a free summary.
  - *Why nightly is enough:* the recent conversation is already in an agent's
    history, so memory matters for what has scrolled out of it (days and weeks
    later), and proposals are reviewed in a weekly digest, so freshness is not what
    limits the loop. Nothing runs per turn: that costs money and adds noise.
  - *Cost follows new content, not room count:* a room with no new activity past
    its cursor costs one query. A room runs only once it has enough new material
    (a minimum number of new messages or a published deliverable) or a week has
    passed (compaction); otherwise its material accumulates for the next night.
  - *No spike:* runs are spread over a window (by a hash of the room id) so the
    assist model is not hit at once; a tenant has a daily cap on what the curator
    may spend, after which its remaining rooms wait for the next night; a tenant
    with `assistDisabled` is skipped.
  - *Not instant, on purpose:* an owner has "Update now" for one room (the same run,
    started by hand, rate limited), and "Save to memory" is immediate and needs no
    AI.
  - *Resumable:* each room's run is claimed with its cursor, so a crash or a failed
    run is retried the next night and never files a proposal twice.
- **Curation is off until the room's owner turns it on.** It reads the room's
  messages with the organization's model and costs budget; the organization's
  `assistDisabled` switch applies. Saving and reading notes work without it.
- **A person reviews.** Proposals wait in an inbox, grouped into a weekly digest:
  accept, edit then accept, or reject. A proposal not decided in 14 days expires;
  it is never applied by default. A room may set low-risk proposals (appending a
  dated memory note) to apply at once with an undo; briefs, knowledge and lessons
  always wait for a person. Who may review: the room's owner and admins.
- **A proposal's size is bounded**: a few a day per room, merged into the digest,
  so reviewing is minutes a week, not a chore. Whether that holds is an
  observation to make after launch (acceptance rate, expired proposals), not a
  guarantee.

## 4. Evolution inside the room

- **A lesson needs evidence.** The curator proposes one only after at least two
  independent signals in separate turns (a member's correction, a deliverable
  edited after delivery, a run that failed and was redone). One correction is a
  memory note, not a lesson.
- **A lesson is a note**, scoped to the room, read like any other. It changes
  how that project's agents behave and nothing else.
- **A change to a shared component** (an expert's instructions, a skill pack,
  a prompt pack) is never made in place by a run or by the curator. It is a
  proposal against a component version; accepted, it becomes a new version that
  rooms adopt by choosing it (versions stay pinned). The room's agents never
  edit their own instructions, skills or the workspace's instruction files
  directly: shared by many rooms and members, an edit made by one run would
  change everyone's behavior without review, and a line in a message could be an
  injection.

## 5. Promotion to the organization

The curator, not a person writing it, nominates a lesson as general; people
review at three gates.

1. **Nomination.** Once a lesson has held in its room for a while and its
   evidence passed the threshold, the curator drafts a nomination. A nomination
   is a proposal of its own kind in the room's inbox.
2. **Generalization draft.** The curator rewrites the lesson as a skill pack,
   expert or prompt pack draft with the project's names, links and customer
   details removed, and a secret scan runs over it. The scan that exists is the local-skills one
   (`internal/localskills`); stage 4 lifts its credential patterns into one shared
   owner both use (a second copy is not allowed). The draft is text only: no scripts, no tool or permission changes (those
   keep their platform-only route).
3. **Room gate (privacy).** The room's owner or an admin sees the original and
   the draft side by side and accepts, edits or rejects. Nothing leaves the room
   without this.
4. **Organization gate.** The accepted draft is submitted through the Library's
   existing request-and-approve flow (`manage_org`); the organization admin sees
   the diff and approves or rejects.
5. **Adoption is a choice.** The component appears in the organization Library
   (and in `.room/library.md`). A room may be *suggested* a match by its own
   curator; the room owner accepts and pins a version. Nothing is pushed into a
   room. A new version reaches adopters as a proposal, not as a silent change.
6. **Withdrawal.** An organization admin may withdraw a component; adopting rooms
   are told; their pinned copies keep working until they choose otherwise (to
   verify against the Library's existing withdraw behavior at build).

What the organization sees about its effect is counts only (adoptions,
withdrawals, corrections before and after in the adopting rooms), never a room's
content.

### The seam for cross-room discovery (not built)

A nomination has a `source` of `room` (the room's curator) or, later,
`organization` (a tenant-level curator that found a lesson recurring in several
rooms). Everything after the nomination (the draft, the room gate, the
organization gate) is identical and already requires each source room's owner to
review its own original. The tenant-level discovery would read rooms' notes
without their members asking, so it must be a tenant policy switch that is off by
default, audited, and limited to lessons a room already marked shareable. It is
a later PRD.

## 6. Stages

1. **Store, read path, human writing.** Tables, the notes API, `internal/notes`
   (`Select`), `.room/memory/` (brief, index, notes), the "What this project
   knows" panel, "Save to memory", the session-guidance line and its budget.
   No AI.
2. **Curator and inbox.** The room's opt-in, the scheduled curator, proposals,
   the review inbox and digest, compaction, auto-apply for low-risk appends.
3. **Lessons and component proposals.** Evidence threshold, lessons, proposals
   against component versions with version pinning.
4. **Promotion to the organization.** Nomination, generalization draft, the room
   gate, the Library request, adoption and update proposals, withdrawal.

Stage 1 stands alone and is useful on its own.

## 7. Design review

| Lens | Answer |
|---|---|
| Actors | Room members read and (with chat rights) edit notes; owners and admins review proposals and turn curation on; an org admin approves promotions; the curator acts as a system actor in the room, not as any member, and its authority is only to file proposals. A borrowed agent run in the room reads the room's notes like its other `.room/` files (the same exposure as the room's history today). A visitor and another room's members read nothing. |
| Time and lifecycle | Notes are versioned (revisions); a superseded note leaves the index but stays in history; proposals expire after 14 days and are never applied by default; the curator resumes from its cursor after a restart and a rerun of a slot files nothing twice (the proposal key is the room, the cursor and the content). A room deleted deletes its notes. A note's `verified_at` ages its rank. |
| Scale and cost | The active set stays small (compaction, supersession); total notes per room are capped and per-note bodies are bounded; `.room/memory/` has a byte cap per materialization and a line cap on the index. The curator is incremental (reads only past its cursor), rate limited per room, and spends the organization's assist budget (audited). |
| Failure and recovery | A curator run that fails changes nothing and is retried at the next slot; three failures in a row pause curation for the room with a visible reason (as schedules do); a failed materialization leaves the previous files in place; a proposal applied twice is a no-op (idempotent by its id). Review never has a silent failure: every error is shown in the inbox. |
| Boundaries and privacy | Notes are visible to the room's members only; never across rooms or tenants. A promotion crosses the room boundary only through the room gate (the owner sees original and draft) and the organization gate; the draft goes through the secret scan and drops names and links. Notes in `.room/memory/` are fenced as data. Deleting a source message does not silently delete derived notes (stated above). Handoff export carries notes (to confirm). |
| Abuse and untrusted input | Room text is untrusted input to the curator; a proposal can add text only (no permission, tool or script change), is bounded, and needs a person; an injected instruction in a message cannot reach a prompt without a reviewer accepting it. A flood of proposals is rate limited per room. A malicious promotion needs the room gate and the organization gate; adopted content is text and pinned by hash. |
| One owner | `internal/notes` owns selection and the one visibility rule; the Library keeps owning organization visibility and approval (no second sharing system); `roomlog.Fence` owns fencing; the credential patterns have one owner (lifted out of `internal/localskills` in stage 4); the schedule-run seam owns the curator's runs. Architecture tests forbid a second notes reader and an agent-facing search path. |
| Defaults and fallbacks | Curation is off by default; without it notes still work; without an assist model the curator is simply unavailable and says so. The index and brief caps degrade by ranking, never by failing. A missing embedding setting means keyword retrieval, which is the default anyway. |
| External contracts | New: notes API, proposal API, `.room/memory/` layout, the session-guidance line, new tables. A new proposal kind and nomination `source` are additive. The Library's contract is reused unchanged. |
| Load on users and agents | Agents: one line of guidance and files they already know how to read, no tool. People: one-click save, a weekly digest, and limits on how many proposals can exist; the real load (review fatigue) is measured after launch by acceptance rate and expiry, with auto-apply for low-risk appends as the relief valve. |
| Release and compatibility | Additive tables; no migration of existing data; pre-launch, no compatibility code. Curation ships off. A change to the index or caps changes golden files and the prompt budget test in the same change. |
| Observability | Counters for notes read/written, proposals by outcome (accepted, edited, rejected, expired), curator runs and failures, spend, `.room/memory/` size; an audit entry for every promotion step; the room's inbox shows the curator's last run and its failure reason. |

**Pre-mortem.** Six months on, memory is not helping. Likely reasons, and what
guards each: nobody reviews, so it stalls (acceptance and expiry are watched,
auto-apply for low-risk); the index is stale and misleads (`verified_at`, supersede
required, dates shown); notes pile up (caps, compaction); the lexical retrieval
misses (the recall test set and the swap path); an injection reached a prompt
(only reviewed text, fenced, text-only); a lesson leaked a customer's name
upward (room gate with a side-by-side draft, secret scan, org gate); org Library
is cluttered with weak skills (evidence threshold, adoption counts, withdrawal).

## 8. Acceptance (stage 1)

- A note is created, edited, superseded, deleted and rolled back from its
  revisions, by a member with chat rights; a visitor and another room's member
  are refused; tenant isolation holds (contract tests on memory and MySQL).
- `.room/memory/` holds `BRIEF.md`, `INDEX.md` and `notes/`, written only when
  content changes, within the byte and line caps, fenced for others' text; a
  golden file and the prompt budget test cover the session-guidance line.
- `internal/notes.Select` is the only reader; an architecture test forbids another
  and any agent-facing search path.
- A deleted room deletes its notes; a handoff export carries them (if confirmed).
- A recall test set (question → expected note) exists from the first real
  rooms, with the measured recall recorded in this PRD before stage 2.

## 9. Open items

- Confirm the two defaults: derived notes survive a source message's deletion
  (marked unverified), and the handoff export carries notes.
- The embedding setting and OpenSearch vector support (not verified).
- The sweep's window and its time zone (a deployment setting; not decided), the
  minimum new-material threshold and the tenant's daily spend cap (to set from
  real rooms after stage 2 runs).
- Who besides owners and admins may turn curation on or review (this PRD:
  owners and admins).
- Whether a room agent may surface a candidate lesson without a tool (not in
  this design; the curator reads outcomes instead).
