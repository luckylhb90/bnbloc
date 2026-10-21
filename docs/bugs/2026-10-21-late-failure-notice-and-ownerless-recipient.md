# A late failure notice read as news about the newest request; an ownerless recipient was accepted (2026-10-21)

Seen on a test deployment, in a test room where one member (here `maya`)
talked to her own agent (core `acp`, provider `opencode`).

## What happened

1. Message #7 was a chat to `{"core":"acp","ownerId":"","provider":"opencode"}`.
   Realtime stored it and queued a job with an empty owner. A bridge claims
   by owner, so no bridge ever took it; two minutes later the sweep failed
   it as `no_bridge_online` and posted its notice (#13, `notice: job-failed`,
   `sourceSeq: 7`): "The opencode agent is offline: no bridge picked up this
   request."
2. By then the member had sent #8, #10 and #12 to the same agent, which
   answered them; #12 was still running. The feed renders rows by room
   sequence, so the notice appeared at the bottom ("just now"), right under
   #12 and its live "working" strip. The member read "opencode is offline"
   as being about #12, which was running fine.

## Root cause

- **The notice.** The feed anchors delegated work to its request
  (`layoutTurns`, by `dispatch.parentSeq` and `sourceSeq`) and a turn's
  deliverables to its last reply (`placeDeliverables`), but a top-level
  agent row stayed at its own sequence. A notice carries the request it is
  about (`sourceSeq`) and the feed ignored it, so a notice that lands after
  later messages is shown as the room's latest event.
- **The recipient.** `PostChat` (realtime, the one chat publish path)
  trimmed the recipient's owner but never required one. An empty owner has
  no meaning: an agent is always one member's, and a platform agent's
  recipient names the host the client routed it to
  (`web/src/lib/platformRouting.ts`). The history shows where #7 came from:
  #5, #6 and #7 carry no `clientMsgId`, which the web composer sends with
  every message (`api.sendChat`), and no `scheduled` or `dispatchedBy`
  marker, so they were posted by a scripted API client, not the composer.
  The web had one latent copy of the same hole: `resolveRecipient` built
  "my own agent" as `ownerId: subject || ""`, and the team lead as
  `subject ?? ""`.

## Fix

- `layoutTurns` (`web/src/views/room/feedTurns.ts`) moves a top-level
  notice whose request is loaded and was followed by a later message to
  the end of that request's turn (before the next message), and marks the
  entry with the request (`about`). The notice line then reads
  "About: <the request's first line>" (`aboutRequest`). Replies keep their
  place; a request with delegated work keeps its notices in its block, as
  before. The "New" divider is one rule now (`unreadDividerAt`): above the
  first row on screen at or past the read baseline, a late notice excepted,
  so a moved notice never splits the read part of the feed. The feed has no
  "jump to" for anchored rows, so none is added; the request's own job
  status line still shows the failure.
- `PostChat` refuses an agent recipient without an owner (blank after
  trimming) with 400 `recipient.ownerId is required: name the member whose
  agent should answer` (`errRecipientWithoutOwner`), before anything is
  stored, published or queued. Every post path (a typed chat, a scheduled
  post, a dispatch, a messenger reply) goes through it.
- The web never builds one: `resolveRecipient` returns no own agent until
  the member's subject is known, and the team lead likewise.

Tests: `TestChatRefusesRecipientWithoutOwner` (empty and blank owner: 400,
no event, no job); `feedTurns.test.ts` replays the reported room (#7 … #13,
live and rebuilt from history: the notice renders after #7, before #8,
naming #7), plus the on-time, unloaded, late-reply and delegated cases and
the divider; `recipient.test.ts` (no subject, no own agent). Each fails with
its fix reverted.

Landed in 58d70552.

## Why it was not caught

- Lens: Time and lifecycle (a result that lands minutes after its request
  was shown at the time it landed, beside newer work), and Abuse and
  untrusted input (the server checked only the shapes its own client
  sends; an API client sent one it never does).
- Missing question or test: no feed test had a notice arrive after a later
  message; no chat test sent an agent recipient without an owner.
- Added to docs/design-review.md: lens 2 asks where a late result is
  shown and whether it names what it is about; lens 6 asks whether the
  server refuses every shape its own client never sends.
- Lesson: LESSONS.md, "Show a late result with the thing it is about".
