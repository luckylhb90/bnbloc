# Bug: agents did not reply in rooms (three root causes)

Status: done
Commits: 0989abf, 7d0ed63, f5b9186

Symptom reported repeatedly as "the agent doesn't reply" / "message stuck on
Sending…". It was three independent defects on the reply path, fixed in turn.

## 1. A parked turn was treated as busy (`0989abf`)

Repro: send a follow-up to an agent that already replied → "agent still running
after 90s", no reply.
Root cause: idle detection keyed "busy" off any non-empty `activeTurnId`. A
completed turn parks with `activeTurnId` set but `phase:"waiting"`
(`settledAtUnixMs=null`), which is idle/ready, not running.
Fix: a turn is busy only while its phase is actively working
(running/streaming/working/executing) and unsettled; parked phases are idle.
Applied in the core's `SessionIdle` (now the manifest idle predicate) and the bridge's `isIdle`.

## 2. Replies dropped by the activity dedup cursor (`7d0ed63`)

Repro: an agent runs (working→idle) but the reply never appears in the room;
no error.
Root cause: the bridge posted replies without `agentSessionId`. Activity dedups
new messages per `(room, agentSessionId)` via a cursor; with the field empty,
every session's replies collapsed onto one empty-key cursor. Once that cursor
was advanced to a large value, later replies (lower seq) were treated as replay
and were neither stored nor published.
Fix: the bridge sends `agentSessionId = rs.sessionID` so each session keeps its
own cursor. (Also fixed a related realtime bug: `Sequence` was derived from
replay-buffer length and repeated after the buffer slid, so the client's
seq-dedup silently dropped events — replaced with a per-room monotonic counter.)

## 3. SSE stream dropped on idle, missing the reply (`f5b9186`)

Repro: in a quiet room the message echo and the reply never arrive; the client
shows repeated reconnects and presence churn ("request timed out" in console).
Root cause: the room SSE loop only wrote on an event, so an idle room sent no
bytes and the browser EventSource / proxies dropped the connection on their
read-idle timeout; any reply produced during the dead window was missed.
Fix: emit an SSE comment `: keep-alive` every 15s so the stream stays open;
EventSource ignores comment lines, so no client change was needed.
