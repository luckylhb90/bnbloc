# Deliverable cards render above the agent's reply

Status: fixed 2026-10-11.

## Repro

Ask an agent for files. The cards appear under your request and above the
agent's reply, as if they were part of the question.

## Root cause

A version's `sourceSeq` is the message that started the turn (the request:
the bridge records the job's `messageSeq`). The feed rendered each seq's cards
under the row with that seq, so the answer's files always sat above the
answer, live and after a reload alike.

## Fix

`web/src/lib/deliverablePlacement.ts` places a request's cards under the last
reply of its turn: the last reply of the turn's agent (from its job's
owner/provider) before that agent's next request, or, without job data, the
last of the replies right after the request. While no reply has arrived yet,
the cards stay under the request. `sourceSeq` keeps meaning "the request" on
the wire; only the rendering moved.
