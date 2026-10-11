# Welcome card says "no platform agent" to the member who hosts them

Status: fixed 2026-10-11.

## Repro

A member publishes their own agents as platform agents (they are the only
host) and opens the home view, in the desktop app or a browser. The welcome
card says "No platform agent is available right now — an admin can enable
platform agents for your organization", although both agents are published,
online, and are the member's own.

`GET /v1/platform-agents` returned both agents with the member as the only,
online host; `GET /v1/my/agents` listed them online.

## Root cause

`defaultPlatformChoice` picks the first published agent with an online host
that is **not the member** (a member never borrows from themselves, so
`platformChoices` drops their own host). With every host being the member,
no choice remained, and the card treated "no choice" as "nothing published"
and gave the admin message. It never considered the member's own online
agents, which are exactly what that member should start with, and it could
not tell "nothing published" from "published but every other host offline".

## Fix

The card's start action is one decision, `welcomeStart` (`lib/onboarding.ts`):

1. a platform agent served by another member's online host (unchanged);
2. otherwise the member's own online agent (`GET /v1/my/agents`), preferring
   one they publish as a platform agent: "Start a room with your <agent>"
   creates the room and selects that agent, with no platform grant;
3. otherwise no start, with a message that says why: nothing published (ask
   an admin) or published but every host offline (try again shortly).

The composer's injected pick (`composerAgentPicks`) now names whether it
wants the member's own agent, so the new room selects it.
