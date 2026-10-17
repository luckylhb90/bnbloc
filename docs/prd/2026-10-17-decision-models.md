# Decision models: calibrated answers as a platform call (2026-10-17)

Status: building (2026-10-17). The upstream contract is the OpenAI
Decisions format, served by an LLM proxy the deployment already runs (an
internal deployment's proxy and keys are in its private overlay, never
here).

## 1. Why

A decision model answers bounded questions about an input with a number,
not free text: a yes/no probability, one choice from a list with a
probability per choice, or a score on a scale. It is what routing,
triage, classification, moderation and a coding agent's own judgment
points need ("is this command safe", "is this finding worth telling
another agent", "which operation and which element next"). It is cheap
(output tokens free, input a fraction of a chat model's) and fast, and
it is calibrated where a chat model guessing a number is not.

Today every caller wires its own: a coding agent's judge kernel holds its
own key and endpoint, a browser agent posts to the vendor directly. Each
has its own key, budget and no audit. The platform already owns exactly
this for chat (the assist model) and media (transcribe, image): an
admin-configured provider, an organization's own on an allowed host, the
caller's rate and budget, an audit row without content. A decision model
is a third provider of the same kind.

## 2. What it is

- **A capability `decide`** in the provider settings (`internal/decision`
  over `internal/modelsettings`, the shared endpoint rules): the
  platform's provider, an organization's own on a host the platform
  allows, `kind` selecting the wire adapter, a models list (the first the
  default), auth `key` (sealed) or `caller` (the caller's own bearer is
  forwarded — only when it is the acting member's own, never a bridge
  owner's token acting for someone else).
- **One adapter, `openai-decisions`**: `POST {base}/decisions` with the
  OpenAI Decisions request (`model`, `input`: a string or user messages of
  `input_text` / `input_image` parts, `questions`: an ordered list of
  `predicate` / `choice` / `score`) and its reply (`answers` in order, a
  probability for a predicate, `choice` + `confidence` + per-value
  probabilities for a choice, a fractional `score` + `confidence` +
  per-level probabilities for a score, `usage`). Another family is a new
  adapter file and one registry entry.
- **One route, `POST /v1/decisions`**, the same body shape as upstream
  minus nothing: the platform validates it (1–128 questions, names
  unique, 2–255 choices, 2–10 levels, bounded text and images), picks the
  model (an unknown name falls back to the default, like media), calls
  the provider and answers the provider's reply normalized to
  `wire.DecisionReply`, the upstream reply's own field names, so a caller
  written for the OpenAI format changes only its base URL and key.
  `GET /v1/decisions` answers whether it runs for the caller and the
  models to pick from.
- **A second request format, `POST /v1/systemone`**, for callers written
  for the SystemOne format (questions and answers are maps keyed by name:
  `noul` / `choice` / `score`, criteria as a label map or a level list,
  `state` any JSON value). It is the same call: `decision.FromSystemOne`
  converts the request to the list format (written order kept; a noul's
  `true`/`false` criteria join its instructions; an object state,
  instruction or criterion is judged as its JSON text) and the list rules
  validate it; `decision.ToSystemOne` writes the reply keyed by name
  (`noul`, per-label probabilities, a score's `legend`). One provider call,
  one adapter, one rule set; the conversion is two pure functions. Its
  bounds are the list format's: a choice needs 2 labels and a score 2
  levels.
- **A refusal is an answer.** A model declining one question answers it
  `{"type": "refusal", "name"}` in either format; the other answers stand.
  A caller decides what a refused question means (most treat it as
  "uncertain"); the call is not failed for it.
- **Who may call:** a member session (`drive` permission, like assists);
  an agent through the room relay (`wire.RoomRelayRoutes` gains the
  route, so an installable component asks as the turn's requester with
  nothing to configure); an app's page through `app.ai.decide(req)` (a
  signed-in participant, never a share-link visitor). Each call spends the
  caller's assist rate and the cost budget, and is audited (kind
  `assist`, name `decide`, the model, the question count, latency, the
  failure class; never the input, the questions or the answers).
- **The consoles:** the platform's and the organization's media provider
  pages gain a third card, "Decision model".

Not in this round: batching many inputs per call, streaming (upstream
has none), a decision cache, a 1-label choice or 1-level score in the
SystemOne format (refused, naming the field).

## 3. Future callers (not built here)

- A coding agent's judge kernel points its remote judge at
  `{gateway}/v1/decisions` with a member API session: one key, the
  organization's model, its budget and audit.
- A judge kernel or a browser agent written for SystemOne points its base
  URL at `{gateway}/v1/systemone`: its request and reply shapes are
  unchanged.

## 4. Design review

| Lens | Answer |
|---|---|
| Actors | A member (their session), an agent (as the turn's requester, through the room relay), an app page (a signed-in participant; a visitor is refused). A super admin sets the platform's provider and the hosts an organization may name; an org admin its own; an org that turned AI off gets 409. |
| Time and lifecycle | Stateless; settings are read per call, so a change applies to the next call. A call is bounded by the provider timeout (60 s; decisions answer in about a second). No TTL'd credential is held: the room relay's grant is per turn. |
| Scale and cost | Per-member assist rate (20/min) and the cost budget, as every model call. Bounds: 128 questions, 255 choices, 10 levels, 64 KiB of text input, 8 images of at most 4 MiB each, a 1 MiB reply read. The usage is audited so spend is visible per member and model. |
| Failure and recovery | No provider or AI off: 409 with a sentence. Provider 4xx: its status and its own message (a bad question names the field). Unreachable or garbage: 502. No retry (a decision is cheap to ask again; the caller decides). |
| Boundaries and privacy | The key is sealed, never returned. The input, questions and answers are never logged or audited. The provider client is held to its host (no redirects, no private addresses). Caller auth forwards only the acting member's own bearer. Images arrive inline (data URLs), never fetched by the gateway. |
| Abuse and untrusted input | Every field is bounded and validated before the provider is called; names are unique; a choice value is text only. The answers are numbers and labels the caller supplied, so a reply cannot inject instructions; a caller that shows `name` or a label to a model fences it as its own data. |
| One owner | `internal/decision` owns the provider call and the request rules; `internal/modelsettings` the shared endpoint rules; `services/gateway/decisions.go` admission, rate, audit; `wire.RoomRelayRoutes` which routes the relay reaches; `lib/decisions.ts` the web's routes. |
| Defaults and fallbacks | No provider: the route says so (409); `GET` answers `available: false` and every entry hides. An unknown model falls back to the provider's default, like media. No silent fallback to a chat model. |
| External contracts | The upstream shape is pinned by an in-repo fake from the vendor's documented example (request and real reply), and the admin's Test button makes one real call (one predicate) at configure time. The SystemOne format is pinned by its documented example (object state and texts included) converted, sent through the same fake and written back. |
| Load on users and agents | Nothing for members until an app or a component uses it. No agent tool or guidance is added by the platform: a component that wants agents to ask decisions brings its own tool and reaches the route through the relay. |
| Release and compatibility | Additive route, settings key `decision.decide`, SDK method, relay route; pre-launch, no migrations. |
| Observability | One audit row per call (model, question count, input and output tokens, latency, failure class); a provider failure is `obs.ServerError` with the class. |
| AI capability | The feature is an AI capability for every other feature: apps (`app.ai.decide`) and components classify, route and triage with a number instead of parsing a chat reply. |
| Component or platform | Platform: the provider settings, admission, budget, audit (what a component cannot do safely). Callers and agent tools are components or apps. The platform names no question, no use and no vendor. |

**Pre-mortem.**

1. **"Every call came back 400 from the proxy."** The adapter's body was
   tested only against our own fake. The fake is built from the vendor's
   documented request and its real reply, and the Test button makes one
   real predicate call when an admin saves the provider.
2. **"An org's spend went through the roof."** Every call spends the
   member's rate and the cost budget and is audited with token counts;
   an org can turn AI off; the platform can restrict hosts.
3. **"A SystemOne caller's answers came back wrong or reordered."** The
   question map is read in written order (never through a Go map) and a
   name written twice is refused; each answer is keyed by its own
   question's name after the list reply is checked position by position.
4. **"A bridge owner's token reached the proxy for someone else."**
   Caller auth forwards only when the token's subject is the acting
   member (the media rule, one owner); the relay's calls never carry
   the requester's bearer, so a caller-auth provider refuses them with a
   sentence.

## 5. Milestones

- **D1:** `internal/decision` (settings, adapter, validation), the routes,
  the console cards, tests.
- **D2:** `app.ai.decide`, the relay route, the SDK docs.
- **D3 (internal overlay):** the deployment's provider configuration and
  the callers' wiring notes.
