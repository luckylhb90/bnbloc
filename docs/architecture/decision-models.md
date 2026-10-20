# Decision models

Design: [docs/prd/2026-10-17-decision-models.md](../prd/2026-10-17-decision-models.md).

A decision model answers bounded questions about an input with calibrated
numbers: a yes/no probability (`predicate`), one value of a list with a
probability per value (`choice`), a level of a scale with a probability per
level (`score`). The platform offers it the way it offers the media models:
an admin-configured provider, the caller's rate and budget, an audit row
without content.

## Owners

- `internal/decision`: the provider settings (over `internal/modelsettings`:
  base URL, models, sealed key, an organization's own on an allowed host),
  the adapter registry (`kind`), the request rules (`Validate`, every caller)
  and the call. One adapter today: `openai-decisions`, `POST {base}/decisions`.
  `FromSystemOne` / `ToSystemOne` convert the SystemOne format to and
  from the list format (`systemone.go`), so both routes are one call.
- `services/gateway/decisions.go`: the routes, admission, rate, audit.
  `runDecision` is the one owner of a route's call (the member route and an
  app's `~app/ai/decide`: the decision rate, the request's validation);
  `callDecision` the one call of a provider (the caller's provider, the
  call, its audit row), which an assist's gate uses in-process
  (`services/gateway/assist_gate.go`, `docs/architecture/ai-assist.md`).
- `wire/decisions.go`: the shapes; `web/src/lib/decisions.ts` the web's
  routes.

## Routes

| Route | Who | What |
|---|---|---|
| `GET /v1/decisions` | a member | `{available, models}` |
| `POST /v1/decisions` | a member (`drive`); an agent through the room relay (`?room=&job=`, set by the relay) | `wire.DecisionRequest` → `wire.DecisionReply` |
| `POST /v1/systemone` | the same callers | `wire.SystemOneRequest` → `wire.SystemOneReply` (the same call, maps keyed by name) |
| `POST ~app/ai/decide` | an app page's signed-in participant | the same, `app.ai.decide(req)` |
| `GET/PUT /v1/platform/decisions`, `POST …/test` | a super admin | the platform's provider; the test asks one predicate |
| `GET/PUT /v1/tenant/decisions`, `POST …/test` | an org admin | the organization's own (a host the platform allows) |

## Request and reply

`{model?, input, questions}`: `input` is a string or user messages of
`input_text` / `input_image` (a data URL; the gateway never fetches a link)
parts; 1–128 questions, names unique; a choice has 2–255 values, a score
2–10 levels lowest first. Bounds are in `wire` (`MaxDecision*`), checked
before a provider is called; a refusal names the field.

The reply is one answer per question, in order, each checked against its
question's type (a reply that does not match is unreadable, never passed
on half-matched): `probability` (predicate), `choice` + `confidence` +
`probabilities [{value, probability}]`, `score` (a level index, fractional)
+ `confidence` + `probabilities [{value (the level index), label,
probability}]`, or `{"type": "refusal"}` for a question the model
declined; and `usage {input_tokens, output_tokens}` (the upstream names).
An unknown `model` falls back to the provider's default.

SystemOne: `{model?, state, questions: {name: {type, instructions,
criteria}}}` with `noul` (criteria `{true, false}`, joined to the
instructions), `choice` (criteria a label → description map) and `score`
(criteria a level list); `state`, instructions and criteria may be any
JSON (judged as their JSON text). The reply is `{model, answers: {name:
…}, usage}`: `noul` (the probability of yes), a choice's per-label
`probabilities`, a score's `legend` and per-level `probabilities`.

## Auth, budget, audit

- Auth `key`: the sealed key. Auth `caller`: the member's own bearer is
  forwarded, only on the member's own call; an agent's call (the bearer is
  a bridge owner's) and an app page's refuse with 409 rather than forward
  someone else's session.
- Every call spends the caller's decision rate (600/min, burst 120, its
  own: a browser run makes one call per step, and must not starve the
  member's assist rate) and the cost budget; an organization with AI off
  gets 409.
- One audit row per call (kind `assist`, name `decide` or
  `app:<id>:decide`): the model, question and image counts, tokens,
  latency, failure class; never the input, questions or answers.
