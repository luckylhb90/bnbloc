# Presence snapshots stormed a large room (2026-10-30)

## What happened

The channels delivery load test (`docs/load-testing.md`, "One large room
over channels") opened 1,024 member streams on one room. Before a message
was sent, streams started closing: `realtime_sse_overflows_total` reached
23,067 in one run, only 384 of 1,024 streams were open when sending began,
and 22,455 disconnects followed.

## Why

`Presence.Enter` published a `presence_join` and a `presence_snapshot` of
every present member on each arrival, and `finalizeLeave` a leave and a
snapshot. Every stream of the room received each snapshot. An arrival in a
room of n members cost n snapshots of n ids. At 1,024 members the
snapshots filled the streams' 64-event buffers; a full stream is closed
(the client catches up from history), and its reconnect was another
arrival.

## Fix

A stream writes its own `presence_snapshot` when it opens
(`Presence.Snapshot`); the room's stream carries only `presence_join` and
`presence_leave`. A preview still gets no presence. Test:
`internal/realtime/presence_test.go`. The same load test then ran with no
overflow and no disconnect.

## Lesson

A room-wide event whose size grows with the room is a broadcast of n², and
it shows only at a room size nobody had tested: the load test's rooms had
been 50 members.
