# The media tools never reach the gateway, and publish over each other (2026-10-17)

Status: fixed (this change; not yet released).

Found by review of de4f5bae ("the agent tools become an installable MCP
component") and e8ee14e3 ("the bridge is the credential"), before any
release carried them.

## What happened (each reproduced from the code)

1. **Every tool call was unauthenticated.** `cmd/bnbloc-media-tools`
   read `MEDIA_TOOLS_TOKEN` and never sent it: `http.Post` with no
   `Authorization`. Every call answered 401. The unit test's fake gateway
   did not check auth.
2. **Every generated image in a room got the same artifact id.** The
   gateway derived it as `"artf_" + hex(room + subject + name)[:24]`: 24 hex
   characters are the first 12 bytes of the input, which are all room id.
   `poster.png`, `poster-2.png` and `cat.png` in one room all came out as
   one id. With the version publish pinned at `baseVersion=0`, the second
   image of a batch answered 409 (the call 502), and so did every later
   image in that room.
3. **The tools worked only in a session's first turn.** The bridge put the
   job id and its own gateway token into the server's env when the session
   was created. The session binding sum covers env names, not values, so
   later turns reuse the session: `MEDIA_TOOLS_JOB` stayed the first
   turn's (the job grant then answers "job is not running"), and the token
   was the bridge's cached owner session, expiring with `SessionTTL`.
4. **The bridge's full gateway session was handed to a child process**, and
   the new `credentials: "bridge"` source gave it to any stdio component
   declaring the four `MEDIA_TOOLS_*` names, which the public bridge
   hardcoded.
5. **The session token could leave for a provider.** A media provider
   with auth `caller` was sent the request's `Authorization`; under a job
   grant that is the bridge owner's bnbloc session, while the call acts as
   the requester.
6. **Images were not attributed to the job** (no `sourceJobId`,
   `sourceSeq`, `channelId`), though the commit and PRD said they were.

## Fix

- **The room is the credential** (`mcp.credentials: "room"`, replacing
  `"bridge"`). A room-credential stdio tool declares no env; the bridge
  gives it the session's **room relay**: the room tools server's loopback
  `/relay/` address and the session's own room tools bearer
  (`BNBLOC_ROOM_RELAY_URL`, `BNBLOC_ROOM_RELAY_TOKEN`, reserved names). The
  relay forwards only `wire.RoomRelayRoutes` (routes that admit a job
  grant), with the bridge's own client, and sets `?room=&job=` itself from
  the job bound to the session at each turn's start. The gateway session
  never leaves the bridge, the grant follows each turn, and the bearer dies
  with the session binding. No component-specific name is in the bridge.
  A built-in (platform) pack requiring a stdio tool no longer counts as
  someone else's content.
- The tool sends the bearer; `credentials: "room"` is refused on an
  endpoint (remote) runtime and with any declared env.
- Gateway: the room service mints the artifact id; the publish carries the
  job's channel, `sourceJobId` and `sourceSeq`; a caller-auth provider is
  used only when the bearer's subject is the one the call acts as; the
  image sniff has one owner (`internal/media`).
- The stray `bnbloc-media-tools` build at the repo root was removed.

## Why it was not caught

- Fakes accepted what the real side refuses: the fake gateway took
  unauthenticated calls, and no test published two images into one room.
  The question "does each fake check what the real boundary checks
  (auth, conflicts, uniqueness)?" joins lens 9 (external contracts).
- A per-turn value (the job) was placed in a per-session channel (the
  server's env). Lens 2 (time and lifecycle) gains: "what does this value
  belong to — the session, the turn, or the call — and is it set where
  that lifetime starts?"
- A new credentials source was added for one component, with its names
  in the platform: lens 14 (component or platform) answered "component"
  for the tool but not for its plumbing.
