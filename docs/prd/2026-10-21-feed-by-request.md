# The room feed by time or by request (2026-10-21)

Status: proposed. Owner: the room feed (`web/src/views/room/MessageFeed.tsx`,
`feedTurns.ts`), member preferences.

A member chooses how a room's conversation reads: **by time** (today: every
message and agent step in arrival order) or **by request** (each request
is one block: its agent, status, elapsed time and latest step in the
block's head; its steps inside, collapsed; its answer at the block's foot).

## 1. Why

- QA 2026-10-21: two agents ran the end-to-end tests in one room at once;
  their steps and replies interleaved and were hard to follow. The owner
  reviewed a demo (`bnbloc-demos/grouped-turns.html`) and asked for both
  views with a switch: "两种效果都可以支持，用户自己选择怎么展示".

## 2. What it is

- A switch in the feed's header: By time / By request. The choice is the
  member's (stored with their account, so every device follows it);
  default By time.
- By request:
  - a block per request (a person's message to an agent, a scheduled or
    event-triggered run, a delegated request), in the order requests were
    made; a block's head shows the agent, live status and elapsed time,
    the latest step, and the step count; clicking opens its steps;
  - the answer (final reply, deliverables, cards such as find-a-way or a
    publish card) sits at the block's foot, always visible;
  - messages that answer no request (people talking, room lines) stay in
    time order between blocks;
  - a late notice belongs to its request's block (the rule built
    2026-10-21 for By time holds here too).
- One layout owner (`feedTurns.ts`) builds both views from the same rows;
  unread and "jump to latest" work in both.

## 3. Design review

| Lens | Answer |
|---|---|
| One owner | Both views from one turn layout; one preference. |
| Load on users | One switch; default unchanged. |
| AI capability | The block head's latest step is the agent's own words; no new model call. |
| Release | Additive; default is today's view. |
