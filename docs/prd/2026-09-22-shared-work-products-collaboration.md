# Shared Work Products — collaboration primitives on artifacts

Status: proposed
Date: 2026-09-22
Owner: maya

## Problem

bnbloc has the substrate for multi-person + multi-agent collaboration (rooms,
membership, multi-agent addressing, borrow, attachments, realtime SSE), but the
*collaboration itself* is thin. Today a room is mostly "several people and
several agents talking past each other in one chat." Chat is ephemeral (who said
what); real collaboration needs a **durable shared work product** that multiple
parties can see, change, comment on, and converge on.

We already have the storage and authoring layers for that product. What is
missing is the **collaboration layer** on top of it: the primitives that let
humans and agents *express intent on the same artifact and converge*.

## What already exists (do NOT rebuild)

- **Artifact version store** (`artifacts` + `artifact_versions`): a
  `parent_version` chain, `content_hash`, `summary`, `author_user_id`, and
  `baseVersion` optimistic concurrency (`PublishArtifactVersion` returns
  `ErrArtifactVersionConflict` → HTTP 409). Kinds: markdown, code, image, html,
  app. Handlers: create / list / get / publish-version / get-version, plus a
  transient live-preview `state` channel.
- **Agents already author artifacts.** The bridge (`services/bridge/artifacts.go`
  `publishArtifactNow` + the workspace file-manifest scan) detects files an
  agent writes in its workspace, maps them to a stable artifact id
  (`artifactIDForFile`), and CAS-publishes a new version attributed to `agent`.
  So "an agent produces a deliverable as an artifact, not a chat wall" is DONE.
- **Web**: `ArtifactPanel`, `StandalonePreview`, `diff.ts` render artifacts and
  version diffs. Agents read the room's artifacts via `.room/artifacts.md`.

Conclusion: the *material* of collaboration (a shared, versioned, agent-writable
product) exists. The gap is the *social protocol* over it.

## The gap: no way to express intent on a shared product

A real collaboration loop is: someone drafts → others react (approve, object,
request a change) → the draft converges. Today the only operation is
"publish a new version," which is a blind overwrite guarded by a 409. There is:

- no way to **comment** on a specific spot in an artifact ("this logic is wrong",
  "add a security section") — for a human OR an agent;
- no way to **propose a change** and have someone else (human or another agent)
  review/accept it — publish is fire-and-forget, not propose→approve;
- no way to **claim** an artifact ("I / @jack/codex will take this") so two
  agents don't edit the same file and thrash 409s;
- no way to **record a decision** ("we chose PostgreSQL because X") so the room —
  and every agent's next turn — knows what was already settled, instead of
  re-litigating it in chat.

## Design: four primitives, layered, each reusing the artifact store

### P1 — Artifact comments (anchor for all collaboration)

A comment is a message *bound to an artifact* (optionally to a version + a
content anchor: line range for code, block for markdown). Author is any room
member — **person or agent** (agents comment via the bridge, same as they post
replies). Comments are the shared vocabulary of "here's what I think about this
part."

- New table `artifact_comments(comment_id PK, tenant_id, artifact_id, version,
  anchor_json, body, author_user_id, resolved_at_ms, created_at_ms)`.
- `anchor_json`: `{kind:"line", from, to}` | `{kind:"block", path}` | `null`
  (whole-artifact). Anchors are advisory; a comment survives a version bump but
  is marked "on an older version" when its `version` < current.
- Endpoints (room service, room-scoped, member-gated — reuse
  `authorizeArtifactRead`):
  `POST /v1/rooms/{id}/artifacts/{aid}/comments`,
  `GET  .../comments`,
  `POST .../comments/{cid}/resolve`.
- Realtime: publish a transient `artifact_comment` event so open panels update
  live (same hub pattern as `artifact_updated`).
- **Agent side**: comments an agent should act on appear in its context. Extend
  `.room/artifacts.md` / a new `.room/artifacts/<id>.comments.md` so a driven
  agent reading the artifact also sees the open comments on it. This closes the
  loop: human comments "add a security section" → agent's next turn sees it.

### P2 — Change requests (propose → review → merge)

Upgrade "publish" from blind overwrite to an optional **proposal**. A change
request is a candidate artifact version that is NOT yet the current version until
approved.

- New table `artifact_change_requests(cr_id PK, tenant_id, artifact_id,
  base_version, proposed_content_hash, proposed_version_id, summary,
  proposer_user_id, status enum('open','approved','rejected','superseded'),
  reviewer_user_id, created_at_ms, decided_at_ms)`. The proposed content lives
  as a normal `artifact_versions` row on a side branch (parent = base_version),
  so we reuse the existing version store and diff machinery — a CR is just a
  version not yet promoted to `current_version`.
- Flow: `POST .../artifacts/{aid}/change-requests` (proposer, human or agent) →
  others see a diff (existing `diff.ts`) → `POST .../change-requests/{cr}/approve`
  promotes it to current (CAS on base_version; 409 if the base moved) or
  `/reject`. Approval authority = room role (owner/admin, or artifact owner via
  P3 claim).
- **This is the human↔agent and agent↔agent review loop**: agent A proposes a
  change, human or agent B approves. It also makes the existing 409 meaningful —
  a losing concurrent edit becomes a change request to rebase, not a lost write.
- Direct `publish` stays for the trivial single-author case (e.g. the agent's
  own scratch draft); CRs are opt-in for "this needs review."

### P3 — Claims (avoid collision, assign ownership)

A lightweight lease on an artifact: "X is actively editing this." Prevents two
agents from editing one file and thrashing 409s, and gives artifacts an owner
for P2 approval authority.

- Reuse the shape of the existing agent-borrow grant / lease model
  (`shared_agent_leases` is a good reference): `artifact_claims(artifact_id,
  claimant_user_id, expires_at_ms, ...)`, TTL-bounded, refreshed on activity,
  auto-released on expiry. A claim is advisory (a soft lock + a UI signal), not
  a hard mutex — publish still CAS-guards correctness.
- `POST .../artifacts/{aid}/claim` / `/release`; the claimant is the
  authenticated caller (person) or the driving agent (`owner/provider`).
- Realtime `artifact_claimed` event so the panel shows "🔒 jack/codex is editing."

### P4 — Decision log (converge and remember)

A special artifact kind `decision`: a durable, append-mostly record of "what the
room decided." Any member/agent can propose a decision from the discussion;
it captures the statement, rationale, proposer, and participants.

- Reuse the artifact store with `kind='decision'`; each decision is one artifact,
  its versions are amendments. Or a dedicated `room_decisions` table if we want
  cross-artifact querying — decide during design review (lean: reuse artifacts).
- **Agent value**: a driven agent reads `.room/decisions.md` and stops
  re-arguing settled questions ("we already chose PostgreSQL"). This is the
  single highest-leverage anti-thrash feature for long multi-agent rooms.

## Recommended MVP (first slice)

**P1 (comments) + P2 (change requests)**, because together they are the minimal
complete collaboration loop and both reuse the existing version store + diff:

> A human says "draft an architecture doc" → codex produces artifact v1
> (already works) → jack comments "add a security section" (P1) → codex's next
> turn sees the comment in its context, proposes a change request (P2) → jack
> approves → artifact v2. No chat wall, a reviewable diff, an audit trail.

P3 (claims) and P4 (decisions) follow once the loop is proven.

## The agent-collaboration boundary (unchanged)

Per `docs/prd/2026-09-11-agent-collaboration-boundary-decision.md`: bnbloc
provides addressing + authorization + **visibility of shared products**, and
does NOT implement agent orchestration. These primitives are all
visibility/authorization surfaces — comments, proposals, claims, decisions are
*records and permissions*, not a workflow engine. An agent CHOOSES to read a
comment and propose a change; bnbloc only makes the comment visible and the
proposal reviewable. This keeps us on the right side of the boundary.

## Acceptance criteria (MVP)

- A member (person or agent) can post a comment on an artifact (optionally
  anchored to a version/line range); it appears live for other members and in a
  driven agent's context via `.room/`.
- An agent driven after a comment is posted receives that comment in its prompt
  context (verified end-to-end, like the attachment fix: real turn, real
  comment, agent acts on it).
- A change request creates a reviewable side-branch version; approve promotes it
  (CAS on base_version, 409 on a moved base); reject/supersede close it.
- Direct publish still works for the single-author case.
- Every new endpoint is room-scoped + member-gated (reuse `authorizeArtifactRead`
  and the room's tenant, per the room-tenant boundary fix), with realtime events
  for live panels. Transient events excluded from history `persist()`; comments
  and CR metadata are durable (their own tables + migrations in both
  `internal/store/schema.sql` and `deploy/schema.sql` + `migrations.go`).

## Open questions (for design review)

1. Comment anchoring across version bumps — advisory re-anchor, or drop to
   whole-artifact when the anchored range changes? (Lean: advisory, mark stale.)
2. P4 decisions — reuse `kind='decision'` artifacts, or a dedicated table?
   (Lean: reuse artifacts; revisit if cross-room decision queries are needed.)
3. Approval authority for change requests — room role only, or does a P3 claim
   grant the claimant merge rights on that artifact? (Lean: room admin/owner OR
   active claimant.)
4. Do agents propose change requests by default, or only publish directly unless
   told to propose? (Lean: agent publishes its own scratch directly; proposes a
   CR when editing an artifact it does not own / that is claimed by another.)
