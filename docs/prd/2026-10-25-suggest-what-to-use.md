# Suggest what to use: after a request is sent, the platform says what fits it, one click to ask again with it

Status: rewrite (2026-10-25). The first version suggested while the member was
typing; it never showed. This one suggests **after the send**.

## Why

A member who asks an agent to build something does not know that the Library holds the
thing built for it. Evidence (a real room, 2026-10-25): a member asked for a web page
with an AI assistant, got a hand-built Node server with an API key that nobody could
host or share, and spent about 25 turns on "how do I share it", "deploy it", "which
model". The platform already had all of it: the `interactive-apps` skill and the App
builder expert (the page, its data, `app.ai`, the Share link). Neither had ever been
used in that organization, and the composer's Expert picker is a flat list of 30.

Turning things on for everyone is wrong (some need a skill, most never will, and every
skill on costs every session). Choosing by hand is hard among dozens. So the platform
**recommends, the person decides, one click does it.**

### Why not while typing (the first version, withdrawn)

It asked the decision model after the member paused. Measured on the real flow: the
answer arrived about 2.4 s after the last key; people press Enter the moment they have
finished, so the row was gone before it showed. Nobody waits for a suggestion, and any
design that needs the answer before the member sends loses to the Enter key. It also
sent unsent drafts to a model, which needed a schedule, a cap and a privacy switch.

The one moment when the whole request is known **and somebody is waiting anyway** is
after the send: the agent takes tens of seconds; a decision takes about 1.5 s.

## What the member sees

They send a request. Their agent starts as usual (nothing is delayed, cancelled or
changed). Within about two seconds a card appears under their request in the feed, for
them only:

> **App builder fits this request.** Builds web pages and small apps that people open
> from a link… `[Ask again with it]`  `[Not now]`

- **An expert:** *Ask again with it* sends the same request again as a new message with
  that expert picked (the first answer keeps running and stays; the member chose to
  ask twice). The new message is a normal send: the same agent, the expert's
  confirmation of permissions when it declares any, the team plan's cost check.
- **A skill pack:** *Turn on and ask again*: turned on for the room when they may manage
  it, else for their own agents, then the request is sent again.
- **A tool:** *Add and ask again*: installed, then the request is sent again.
- *Not now* hides the card and keeps that candidate quiet for a month. A card also goes
  when the member sends another message or leaves the room.

At most one card per request, never before the agent has started, never for a request
that already wore the expert or the pack.

## Rules

1. **The platform recommends; the person decides.** Nothing is picked, turned on,
   installed or sent without a click. No default-on, no agent prompt text, no agent
   tool.
2. **Only what was sent is read.** The request is the message the member chose to send
   to an agent (the same text the agent gets), so nothing about drafts leaves the
   browser and there is no "typing" privacy case. It goes to the organization's
   decision model through the gateway, audited by count and latency, never by
   content, and it is cut to its first 800 and last 400 characters. On by default
   whenever the organization runs one for the member; Settings → Privacy can turn it
   off, and then no card appears from the model.
3. **One decision per sent request.** A card is asked for once, after the send is
   accepted, and never again for that message. Retried or edited messages do not ask
   again. A rate limit or failure shows nothing (no fallback keyword guess: a wrong
   card is worse than none, and this card is not racing the Enter key).
4. **Shown only when clear.** One choice question over the eligible candidates and
   "none"; the choice needs at least 0.6 and twice the runner-up's (or "none"'s)
   probability. An expert that wears a skill pack stands for it. Better silent than
   wrong.
5. **Only what is not already on.** Not the expert the request wore, the room's expert,
   a skill pack on for the room or the member, a tool already added, or a candidate the
   member dismissed this month.
6. **One owner.** The server's `librarysearch` owns what is a candidate for a request
   (rank, visibility, trim); `lib/suggestUse.ts` owns the question and how an answer is
   read; `lib/decisions.ts` owns the call; `views/room/useSentRequestSuggestion.ts`
   decides when a sent request is asked about and applies the click; the card only
   draws. The browser holds no catalog and cuts no list: the keyword matcher and the
   word-overlap cut are removed.
7. **Same rights as the Library's own buttons.** A skill pack for the room is the room
   dialog's `PUT /v1/content-uses` (owner or admin; refused, then for the member's own
   agents); a tool is the Components page's install; an expert is a pick, which the
   server already admits for any visible expert.

## Candidates, at any size of Library

The end state is a Library of thousands of experts, skill packs and tools. Two things
break at that size if the client holds the whole catalog: `GET /v1/components` lists
everything on every call (megabytes), and a word-overlap cut in the browser misses a
request whose words are not the description's ("is this agreement safe to sign" against
"review contracts"). So retrieval is the server's and the decision is the model's:

1. **The server finds candidates**: `GET /v1/library/candidates?q=<request>&limit=N`
   returns the N best matches (default 40, at most 100) among what the member may use
   (the one visibility function, `contentuse.Visible`, plus MCP tools not yet added),
   each as the few fields a decision needs: id, kind, name, label, a trimmed
   description, tags, the skill packs an expert wears. The member's own request is the
   query; it is not stored or logged (audit: count and latency).
2. **The decision model chooses among them** (one choice question, unchanged).
3. **The web draws a card.** It holds no catalog and cuts nothing.

How the server ranks, in the order of what a platform owns:
- **A mechanism, not a policy**: a `LibrarySearch` seam in the store beside the
  content-component store. The default implementation is a lexical ranker over each
  component's name, label, tags and description (BM25 over the stored fields, any
  language: Chinese by character pairs), in memory or MySQL FULLTEXT with the ngram
  parser already used for message search. At 40 components it returns all of them. An
  organization that wants semantic retrieval at 100k components plugs an embedding or
  OpenSearch implementation behind the same interface; nothing above it changes.
- **Authors steer it with content**: `description` as a trigger ("Use when…") and
  `tags`. A component that is never found is a content problem, not a platform one.

Cost at any size: one query bounded by `limit`, and one decision of at most `limit + 1`
choices (about 4,500 tokens at 40; the decision API caps a question at 255). The cost
does not grow with the Library.

Measured before choosing the default (a real Library of 37 experts and skill packs plus
synthetic ones up to 5,000 rows, 12 requests in English and Chinese, on MySQL 8.4 with
the ngram FULLTEXT parser and, separately, a BM25 over words and character pairs):

| First stage | Right candidate in the top 5 / 20 / 40 | Time at 5,000 rows |
|---|---|---|
| MySQL FULLTEXT, natural-language mode | 8 / 9 / 9 of 12 | ~55 ms including a client round trip |
| BM25 (words, Chinese pairs, 6-letter stems) | 8 / 9 / 9 of 12 | ~5 ms in process |
| The decision model over the same candidates | all of those 12 in an earlier run | 1.2 to 1.8 s |

Both lexical rankers miss the same three: a paraphrase ("is this agreement safe to sign"
against "review contracts"), a Chinese request against an English description, and an
indirect one ("turn my call notes into something I can send around"). These are the cases
the decision model reads best and a lexical cut cannot, so **a lexical top-K cannot be the
only first stage**. What the server does about it, without a model of its own:

- It returns the top-K lexical matches **and** the candidates the member has used or
  that the organization or platform put forward (a bounded, curated set: org and
  platform content marked as defaults, recently used by this member), so a paraphrase of
  something the organization offers still reaches the decision.
- The question is cost-bounded, not size-bounded: 255 is the decision API's cap, about
  8,000 tokens at 60 characters of description. The default K is 60 (about 2,500 tokens,
  under 2 s); an organization raises it.
- Past the point where a lexical first stage is not enough (a Library in the tens of
  thousands), the `LibrarySearch` seam takes a semantic ranker: embeddings of the
  descriptions, built once per version, queried by the request. The interface, the
  question and the card do not change. This is a plug-in, not built now: nothing in the
  platform embeds text today, and adding a model provider kind to the platform for one
  caller is exactly what the seam is for.

## Decision log: semantic retrieval (SQLite FTS5 + sqlite-vec, FAISS, Chroma), 2026-10-25

Asked: should the first stage use SQLite in memory with FTS5 and sqlite-vec, FAISS or
Chroma, "stronger than a MySQL fuzzy search"? Decision: **not now; the seam is where it
plugs in, and it plugs in as a component.**

- **The comparison was mis-framed.** The default ranker is not MySQL fuzzy search; it is a
  BM25 in the gateway's process (MySQL FULLTEXT was only a control, with the same hit rate).
  The real choice is lexical against semantic.
- **What semantic retrieval buys:** paraphrases, a request in one language against a
  description in another, indirect requests. These are the three misses measured above and
  the reason the decision model exists as the second stage. What it does not do: a vector
  store holds vectors; it does not make them. Every option needs an embedding model, and the
  platform has none (no provider kind, no index, no invalidation on a new version).
- **Why none of the three, now:** the real Library is 37 components, and the lexical stage
  is 37 ms at 5,000. FAISS is a C++/Python library with no per-member filtering, and
  visibility must be applied inside retrieval. Chroma is a separate stateful Python service
  the platform would then own. SQLite with sqlite-vec is the best of the three (embedded,
  one file, lexical and vector together) but is a second store beside MySQL, needing cgo or
  a Go port, with its own lifecycle. A page offered as the case for them (framewiki's
  knowledge base) describes a different system (a RAG service over faiss, zvec or numpy,
  with Qwen3 embedding and reranker models at 8 cores and 16 GB) and states no benchmark,
  so it is not evidence either way.
- **Where it belongs when it is needed:** an installable retrieval component implementing
  `librarysearch.Ranker` (a service component behind the contract, its own repository), free
  to use sqlite-vec with FTS5 for a hybrid index; or MySQL's vector type or OpenSearch k-NN
  when the Library lives in those. The platform keeps the interface, visibility filtering
  and the bound; the component owns the model and the index (core rule: the platform
  provides only what it must).
- **When to revisit:** a Library of several thousand where members report "the right one
  was never suggested". The check is cheap and does not touch the platform: index the real
  descriptions with a small embedding model once and compare top-60 recall with the lexical
  stage on the same requests.
- **Not adopted, and why:** a "why was this candidate chosen" field on the shortlist, to
  compare rankers later. Worth adding when a second ranker exists.

## Where the card lives

A sent request is a row in the feed. The card is a row-level companion of it, like the
agent's publish and share proposals: drawn under the request, for the person who sent
it, never stored on the server (a reload shows nothing; the suggestion is for the
moment the agent is working). The feed's own row model is untouched.

## Placement

| Part | Placement | Why | What an organization would swap |
|---|---|---|---|
| Finding candidate components for a request | **Platform seam** (`LibrarySearch`, default lexical) | It is the one place that knows what the member may see (visibility) and that scales with the deployment; a client cannot hold a thousand-item catalog. Reason: identity and isolation | The ranker behind the interface: embeddings, OpenSearch |
| Choosing among the candidates | **Platform capability, already built** (the decision model) | Calibrated, audited, budgeted; a component could not do it safely | The provider of the decision model |
| What makes a component findable | **Content** (descriptions as triggers, tags) | Authors know their own content | Rewrite the descriptions |
| The card and its one click | **Platform** (web client) | The product's own surface for the member's conversation; names no expert, skill or tool | Nothing |

Name test: no expert, skill pack, tool or language is named in platform code. Fork test:
an organization with a different ranker plugs it in. Replace: a second ranker installs
beside the first.

## Lenses (pre-mortem)

- **Actors.** Only the sender sees it. A request sent to people only (no agent) gets no
  card. A visitor has no composer. A member who may not manage the room falls back to
  their own agents and is told.
- **Time.** The request is already sent; the card arrives 1 to 3 s later and is
  dropped if the member left the room or sent another message. Asking again is a
  second, separate message: the first is never edited or cancelled (rule: never stop
  a live run).
- **Cost.** One decision call per request that went to an agent, against the member's
  assist rate (20 a minute). No calls while typing. A 429 or failure shows nothing and
  keeps the model out for a minute.
- **Noise.** The failure to avoid is a wrong or repeated card: the threshold, the
  one-per-request rule, "not already on" and the monthly dismissal.
- **Privacy.** Only text the member sent to an agent, trimmed, audited without content,
  off with one switch.
- **Reload/offline.** Nothing persists; nothing breaks.
- **Scale.** Bounded at every size (above). A Library of 100,000 needs the semantic
  ranker behind the seam; the interface, the question and the card do not change.
- **Visibility.** The server ranks only what the viewer may see, so a candidate the
  member could not use is never named in a card or sent to the model.
- **Not done.** Suggestions for people-only messages; learning from clicks (no click
  data is collected, by design).
