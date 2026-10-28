# The default channel left the rail; a section would not fold (2026-10-28)

Status: fixed (see Fix).

## Repro

1. In a room created with `POST /v1/rooms` and then set up through the API,
   open the room: the default channel is shown. Open a channel in a section:
   the default channel is gone from the rail.
2. In the same rail, click the section's caret while one of its channels is
   open: nothing folds.

## Root cause

1. The rail shows the channels a member follows (C18), and `joined` was only
   "has a follow row". The default channel's follow is written for the room's
   creator at creation, best effort, and for nobody in rooms created before
   follows existed, or by a path that failed it. The default channel cannot be
   left in the menu, but the server allowed leaving it, and a member with no
   row was "not joined", so it showed only while it was the open channel.
2. A section with the open channel in it was never treated as folded, so the
   caret toggled the stored state without any visible change.

## Fix

1. Every member is in the default channel: `channelView` reports it joined
   whatever the follow rows say, and leaving it is refused (409; hide it
   instead). One place decides (`channelView`).
2. A folded section stays folded and still shows the open channel and those
   with unread messages, as Slack does. The channels e2e covers both, with a
   member who has no follow row of the default channel.

## Why it was not caught

Lens 8 (defaults and silent fallbacks): "joined" was derived from a row a
best-effort write may not have made, and the e2e fixture gave every member a
follow of the default channel, so the missing case never ran.
