# Deliverable cards vanish from older messages

Status: fixed 2026-10-11.

## Repro

An agent reply produced files (deliverables) more than 24 hours ago. Reopen
the room: the reply is in the feed, but the deliverable cards under it are
gone. The files are still listed under Files.

## Root cause

Cards are anchored under the message that produced them (a version's
`sourceSeq`). Live events anchor them as they happen; after a reload,
`useRoomActivity` re-seeded the anchors from `GET .../artifacts?limit=50`,
keeping only artifacts updated in the last 24 hours (at most 24) and then
fetching each one's versions. Every older deliverable was dropped by design,
so cards disappeared once they were a day old, and a busy room lost cards
beyond the 24 newest even sooner. The anchoring followed recency, not the
messages actually on screen.

## Fix

- room: `GET /v1/rooms/{roomId}/artifact-anchors?from=&to=` returns
  `{seq, artifactId}` for every version whose `sourceSeq` lies in the range
  (`store.ArtifactStore.ArtifactAnchors`, memory and MySQL), membership-checked
  like the artifact list.
- web: the room view anchors the cards of exactly the messages it has loaded
  (the feed's sequence range), and again when older history pages in. The
  recency window and the per-artifact detail fetches are gone. The time
  heuristic stays only for live versions published without a `sourceSeq`.
