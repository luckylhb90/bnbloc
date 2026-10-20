# Attachments cut at 20 seconds never reached the agent

Date: 2026-10-20. Component: the bridge. Fixed the same day.

## What happened

A member asked an agent to build how-to GIFs from the real app and sent two
sample GIFs (7.6 MB and 11.9 MB) with their requests. The room said, for
each: "Could not give claude-code the attachment …: it could not be
downloaded from the room." The agent answered without the samples, drew its
own screens, and the member gave up on the task. A 135 KB zip sent with the
second GIF reached the agent.

## Why

- The bridge downloads a message's attachments from the gateway before the
  turn and writes them into the agent's workspace.
- The download used the bridge's short-call HTTP client, which ends any
  request at 20 seconds in total. The notices came 46 and 66 seconds after
  the turns started: the downloads were slow then, and each was cut at
  20 seconds. A small file finished in time.
- The agent's core was not the limit: it states a 40 MB request limit.
- The same client cut every relayed tool call at 20 seconds, fixed
  2026-10-19 for the room relay; downloads were the other path still on it.

## Fix

- Room file downloads (attachments, a file's version to edit) go through
  the one sender on their own client with no total time limit. Only a
  stall ends one: no answer, or no byte of the body, for 60 seconds
  (`downloadStall`), answered as `errDownloadStalled`.
- Tests: a download that keeps arriving outlives the short-call limit; one
  that stalls before its answer or in its body ends as stalled.

## Lesson

A fixed total timeout on anything whose size the member chooses (a file, a
video, a long task) fails exactly the requests that matter most. Bound
waiting by activity, never by the clock.
