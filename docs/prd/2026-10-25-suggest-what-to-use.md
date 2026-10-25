# Suggest what to use: the composer recommends an expert, skill pack or tool, one click to turn it on

Status: slice 1 built (experts, skill packs, MCP tools; a local keyword matcher).
Slice 2 (open): an assist that ranks across languages.

## Why

A member who asks an agent to build something does not know that the
Library holds the thing built for it. Evidence (a real room, 2026-10-25): a
member asked for a web page with an AI assistant, got a hand-built Node
server with an API key that nobody could host or share, and spent about 25
turns on "how do I share it", "deploy it", "which model". The platform
already had all of it: the `interactive-apps` skill and the App builder
expert (the page, its data, `app.ai`, the Share link). Neither had ever been
used in that organization (0 uses, 0 members) and the composer's Expert picker
is a flat list of 30 experts.

Turning things on for every member is wrong: some members need a skill and
some never will, and every skill that is on adds its description to every
session of every agent. Choosing by hand is hard when there are dozens of
experts, skill packs and tools. So the platform **recommends, the person
decides, one click turns it on.**

## What the member sees

While writing a request in a room's composer, a slim row above the input says
what fits it:

- `App builder fits this request · Use · ✕` (an expert: picked for the
  message, like the Expert picker; the banner and the picker show it).
- `Interactive apps fits this request · Turn on · ✕` (a skill pack no expert
  here wears: turned on for the room when the member may manage it, else for
  their own agents).
- `Browser fits this request · Add · ✕` (an MCP tool the member has not added).

At most one suggestion, never blocking the send, never before the member
has written enough to say what they want, dismissible for good (30 days).

## Rules

1. **Recommend, never enable by itself.** Nothing is turned on, picked or
   installed without the member's click. No global default-on, no agent
   prompt text, no new tool or platform rule for agents.
2. **The draft stays in the browser.** Ranking is local (a keyword score over
   the Library's names, tags and descriptions the client already has); nothing
   about what the member writes is sent anywhere to produce a suggestion.
3. **A suggestion is shown only when it is clear.** At least two distinct
   words of the request match the candidate, or one word the author declared
   as a tag when the request is short (at most five meaningful words); a
   close second candidate with the same coverage means no suggestion; an
   expert that wears a matched skill replaces the skill. Better silent than
   wrong.
4. **Only what is not already on.** Not the expert already picked or the
   room's expert, not a skill pack already on for the room or the member, not a
   tool already added. Dismissed candidates stay quiet per member.
5. **One owner** for the matching: `lib/suggestUse.ts` (tokenizing, scoring,
   the rules above). The hook loads the Library and applies the click; the
   row only draws.
6. **The same rights as the Library's own buttons.** A skill pack for the room
   is the room dialog's `PUT /v1/content-uses` (owner or admin); a refusal
   falls back to the member's own agents. A tool is the Components page's
   install. An expert is a pick, which the server already admits for any
   visible expert.

## Candidates and their descriptions

Candidates are what `GET /v1/components` returns for the member: content
experts and skill packs, and MCP tools (`kind: mcp`). Content authors steer
the match with `description` (written as a trigger: "Use when…") and `tags`
(keywords, in any language). The default content stays in neutral English (it
hard-codes no other language), so a request in another language matches only
the packs an organization wrote or localized in that language (an
organization's experts often are): the cross-language case is slice 2.

## Lenses (pre-mortem)

- **Actors.** A visitor or read-only member has no composer. A member who is
  not a room admin cannot turn on a room skill: the click falls back to their
  own agents and says so. Another member's agent that is borrowed does not wear
  the requester's own skills: the toast names "for your agents" so it is not
  taken for the room.
- **Time.** A skill turned on applies from the next turn, not the one being
  written (the toast says so). Suggestions are computed after the member pauses
  (700 ms), never per keystroke; the catalog is fetched once the first
  suggestion is wanted and kept for five minutes.
- **Noise.** The failure to avoid is a wrong, repeated suggestion. The rules
  above, the dismissal and the "one at a time" limit are the defences; the
  matcher's tests hold real catalog names against real requests, including the
  ones it must stay silent on.
- **Cost.** No server work, no assist call, no agent tokens. One extra
  `GET /v1/components` per room visit that gets a suggestion (the Library
  already reads it).
- **Privacy.** See rule 2.

## Slice 2 (open)

A ranking assist on the organization's model for requests the keyword score
cannot place (another language, an indirect request). It would take the draft
and the candidates' labels and return a candidate; the draft would then leave
the browser, so it is the member's setting, off by default for personal
drafts. The row, the actions and the dismissal stay as they are: the matcher
sits behind `suggestUse`'s signature.
