# A sent message stays "Sending…" forever (2026-10-09)

Status: fixed (web), pending deploy.

## Repro

Browser or desktop, any room: send a message. Intermittently (1 in 5
headless runs against production) the bubble keeps "Sending…" although the
server stored it and the room stream delivered its echo. Reported with a
screenshot on desktop 0.1.20 (the agent then failed for an unrelated
reason: the model key's budget was exhausted, a 429 from the provider).

## Evidence

- Server: the message was persisted at once (history seq 1746) and a live
  stream with the web client's exact query (`channelId=…&allChannels=1&
  includePeople=1`) receives the echo within ~0.6 s.
- Client (EventSource instrumented in a headless run of the production
  bundle): the failing run received both `chat_message` echoes with the
  right `clientMsgId` and channel, yet both rows stayed pending.

## Root cause

`feedModel.ackSent`. The echo carries the `clientMsgId`, so an echo that
arrives before the POST response confirms the optimistic row in place
(`applyIncoming`). When the POST ack then lands, `ackSent` finds no separate
echo row and rewrote the row to `status: "pending"` unconditionally; nothing
confirms it again. Whether the echo or the ack wins is a network race,
hence intermittent. The existing echo-before-ack test used an echo without
a `clientMsgId` (appended, then merged), so it never exercised this path.

## Fix

`ackSent` keeps an already-confirmed row confirmed and only records the
sequence. Regression test `testClientMsgIdEchoBeforeAckStaysConfirmed`.
