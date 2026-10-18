# Browser component

Design: [docs/prd/2026-10-17-browser-component.md](../prd/2026-10-17-browser-component.md).
The component lives in its own repository,
[`luckylhb90/bnbloc-browser`](https://github.com/luckylhb90/bnbloc-browser)
(its `docs/design.md` is how it works: the tools, a step, checkpoints, the
browser host, the live view). This page is the platform's side.

A room agent operates web pages in a dedicated, visible Chrome profile on
the machine of the person who asked, signed in as them. It is an installable
MCP tool component, `credentials: "room"`: it reaches the gateway only
through the room relay, as the person the agent is answering. Installing it
is the switch: a turn gets every MCP tool its requester installed, with no
skill pack (`mcptool.Resolve`, PRD §3.6). Nothing in the platform names it.

## What the platform gives it (all generic)

- **The relay's facts** (`GET <relay>/session`, `component.RelaySession`;
  `services/bridge/roomtools/relay.go`): the bound job, room, channel,
  requester, the bridge's owner, the turn's message seq and whether the turn
  is the requester's own message.
- **The decision rate** (`services/gateway/decisions.go`): `POST
  /v1/systemone` through the relay, the operation and target choices a step
  is made of.
- **The live view** (any tool's): `component/live.go` (the tool's side of the
  contract), `internal/wire/job_live.go` (the room's), `services/gateway/job_live.go`
  (`POST /v1/live` under the job grant), `services/realtime/job_live.go` (the
  newest frame per job, watching, control, input), `web/src/views/live/` and
  `web/src/lib/jobLive.ts` (the thumbnail, the large view, pop-out). A view
  that waits for a person (a sign-in) outlives its turn while its tool keeps
  posting, admitted by `store.LoadRanJob` and a relay `?job=` the session
  drove (PRD §3.7); the room lists the views now at `GET
  /v1/rooms/{roomId}/live`.
- **The fence** (`component.Fence`): page text in a tool result is fenced the
  way every room tool fences what someone else wrote.
