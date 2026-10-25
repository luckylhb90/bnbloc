# Suggest what to use: the composer recommends an expert, skill pack or tool, one click to turn it on

Status: built. The organization's decision model places the request when it runs
for the member (on by default, a switch in Settings → Privacy); a local keyword
matcher is the fallback.

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
2. **What is sent, to whom, how often.** With the decision model on (the
   default whenever the organization runs one for the member), what the member
   writes goes to the organization's own decision model through the gateway
   (`POST /v1/decisions`: audited by count and latency, never by content; not
   stored), after a pause of 0.7 s, at most four times per draft: once when it
   first says enough, then only when it has grown 1.8 times and 40 characters
   and 5 s have passed; at most its first 800 and last 400 characters; a stale
   answer is dropped; a refusal, rate limit or failure keeps the model out for a
   minute. The member can turn it off (Settings → Privacy): suggestions are then
   worked out in the browser alone, by a keyword score over the Library's names,
   tags and descriptions, and nothing is sent.
3. **A suggestion is shown only when it is clear.** The decision model is asked
   one choice question over the eligible candidates and "none"; its answer is
   used when the choice has at least 0.6 and at least twice the runner-up's
   (or "none"'s) probability. Its probabilities are calibrated, so this is a
   threshold, not a tuned score: an ambiguous request ("improve the design…")
   came back 0.37 against none 0.33 and stays silent. The keyword fallback shows
   one only when at least two distinct words match (or one rare word of a name in
   a short request) and no close second has the same coverage. In both, an
   expert that wears a skill pack stands for it. Better silent than wrong.
4. **Only what is not already on.** Not the expert already picked or the
   room's expert, not a skill pack already on for the room or the member, not a
   tool already added. Dismissed candidates stay quiet per member.
5. **One owner** for the matching: `lib/suggestUse.ts` (the question and how an
   answer is read, the ask schedule, the keyword fallback). `lib/decisions.ts`
   owns the call. The hook loads the Library and applies the click; the row only
   draws.
6. **The same rights as the Library's own buttons.** A skill pack for the room
   is the room dialog's `PUT /v1/content-uses` (owner or admin); a refusal
   falls back to the member's own agents. A tool is the Components page's
   install. An expert is a pick, which the server already admits for any
   visible expert.

## Candidates and their descriptions

Candidates are what `GET /v1/components` returns for the member: content
experts and skill packs, and MCP tools (`kind: mcp`). Content authors steer
both paths with `description` (written as a trigger: "Use when…") and `tags`.
The decision model reads intent in any language, so the default content, which
stays in neutral English, is found by a request in Chinese; the keyword fallback
matches only words that appear in the descriptions and tags.

Measured on a real organization's Library (40 candidates; requests I wrote
in English and Chinese, plain, indirect and misspelled, and ones nothing fits):

- The decision model answered 18 of 18 sensibly in a first run, and sat out the
  ambiguous ones by probability.
- The keyword fallback, on 71 requests (49 and 22 held out from tuning): it picks
  the right candidate for about 60% of the ones that have one, stays silent for
  nearly all the ones that have none, and shows a wrong one about 1 time in 5
  of those it shows. It misses what shares no word with a candidate (a request in
  Chinese against English descriptions, "is this agreement safe to sign" against
  "review contracts"). That is why the model is the main path and the member
  who turns it off gets a plainer feature, not an equal one.
- Tried against the same sets and **not adopted**: BM25 weighting (identical
  results: with about 40 short documents it has nothing to saturate or
  normalize), and typo tolerance by edit distance (it recovers a few typos and
  adds wrong ones: "lunch" finds the launch-readiness team, "cook" finds the QA
  designer). A grid search over BM25 settings looked better on the first set and
  was worse on the held-out one. **Adopted:** words are known by their first six
  letters, so "translate" finds "translation" and "review" finds "reviewer"
  (+3 right, -2 wrong on the first set, no change on the held-out one).
- A call to the decision model is about 2,100 input tokens and one output token,
  1.2 to 1.8 s.

What improves the fallback is the content: descriptions written as triggers and
tags in the words people use, including their language.

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
- **Cost.** No agent tokens. At most four decision calls per draft, each about
  2,100 input tokens, against the member's assist rate (20 a minute): the ask
  schedule leaves room for the member's own AI buttons, and a 429 puts the model
  out of it for a minute. One extra `GET /v1/components` once a request has five
  characters (the Library read is then ready when the member pauses).
- **Privacy.** See rule 2: the one new thing is that text is sent as it is
  written, not when the member clicks; hence the schedule, the cut, the switch
  and the audit without content.
