# Room event stream takes 15s to open behind the production proxy

Status: fixed

## Repro

Production (`bnbloc smoke`, or curl against a quiet room):

```
curl -N -o /dev/null -w '%{time_starttransfer}\n' \
  -H "Authorization: Bearer $TOK" "$GW/v1/rooms/$ROOM/events?presence=0&replay=0"
# 15.57  (every time; equals the realtime heartbeat interval)
```

## Root cause

realtime's `handleEvents` flushed the response headers but wrote no body until
the first event or the 15s `: keep-alive` heartbeat. The gateway's reverse
proxy forwards headers immediately (measured ~1ms), but the edge proxy in front
of the gateway holds a response's headers until its first body bytes arrive.
So every subscription to a quiet room "opened" only at the first heartbeat:
the client's connected state, and anything gated on it, lagged 15s. Events were
never lost (they carry body bytes), only the open was late.

## Fix

realtime writes an SSE comment (`: connected`) right after the headers and
flushes, so every intermediary forwards the response at once, and sets
`X-Accel-Buffering: no` so nginx-style edges do not buffer the stream. All
stream readers (browser EventSource, web parser, bridge, CLI) already ignore
comment lines (the heartbeat is one). Regression test:
`TestEventsStreamOpensWithoutWaitingForHeartbeat`.
