# A stream resuming below a missing sequence was replayed past it (2026-10-30)

## What happened

The failover drill (`scripts/failover-check.sh replicas`) on replicas without
owners lost one message on two streams (2 of 19,976 deliveries). It was sent
the moment a replica was killed.

## Why

The killed replica stored the message and died before putting it on the bus.
The other replicas saw the next sequence without it, waited the reorder hold,
delivered what followed and marked their streams to read the difference. Two
things went wrong after that:

- The held events were handed to the streams before the gap was marked, so a
  stream that woke on them could check for a gap before there was one.
- The replay buffer kept the events from before the missing sequence and went
  on after it, so the buffer had a hole in it. A stream that reopened on that
  replica below the missing sequence (its own replica was the one killed) was
  replayed from the buffer, which seemed to reach back far enough, and was
  never told to read the difference. It never got the message.

## Fix

When the hold gives up (`flushHeldLocked`), the room's streams are marked
before the held events go out, and the replay buffer starts again after the
hole. A stream resuming below it then finds the buffer does not reach back
and reads the difference (`SubscribeSince`).
`TestBusResumeBelowAHoleReadsTheDifference` covers it and fails without the
fix.

## Lesson

A replay buffer is only good for resuming if it has no holes. Whatever puts
an event into it past a missing one must restart it, or keep the hole in mind
when deciding whether a resume needs a gap.
