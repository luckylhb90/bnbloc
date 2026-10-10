# Narrow rooms showed an empty feed: no default channel was selected

Status: fixed

## Repro

Open any room in a viewport narrow enough that the channel rail becomes a
drawer (phones, ~375px). The feed says "This room is quiet" although the room
has history; opening the channel drawer once makes the messages appear.

## Root cause

Selecting the room's default channel was a side effect inside `ChannelRail`
(it fetched the channel list and called `onSelect(first)` when nothing was
active). Since the stage-driven layout (98699e6) a narrow room renders the
rail only inside a drawer that is mounted while open, so the selection never
ran: `channelId` stayed null and history/stream/read cursors never started.
Room state depended on a presentational component being mounted.

## Fix

`useRoomChannels` (room-level hook in `RoomView`) owns the channel list and
the default selection; `ChannelRail` only renders the list it is given and
creates channels. Verified with the 375px screenshot run (feed shows the
default channel's messages without opening the drawer).
