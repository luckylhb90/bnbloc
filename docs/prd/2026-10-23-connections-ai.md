# Connections: AI where a person decides (2026-10-23)

Amends docs/prd/2026-10-22-connections.md §2.6. That section wanted AI to
lead, but it gave every AI job to the agent that uses a connection, and it
said "no model runs inside the component". So the two decisions the
component asks people to make got no help. An admin decides which requests
read and which change; that policy is the whole safety boundary, and it
reaches the admin as raw JSON. A member decides each held change, and
sees it as a method, a path and a body.

## 1. What the AI does

### 1.1 It reviews a connector before an admin enables it

An admin adds a connector by hand, or an agent drafts one. The review then
runs on its own, again after every change to the connector, and on "Review
again". It says:

- **in one sentence**, what agents can do with the connector once it is
  enabled;
- **what they read** without asking, and **what they change** only after
  the member allows it, each in plain words;
- **findings**, ranked `high`, `medium` or `low`. Examples: a read rule
  that also matches a deleting request; a host outside the system's own
  domain; OAuth scopes beyond the connector's purpose; something in the
  guide that looks like a credential; a write rule so broad that any
  request counts as a change.

The admin page shows the review next to the connector and its draft. When
the review has a `high` finding, the Enable button says so. The admin
still decides; nothing is blocked.

### 1.2 It proposes rules from how the connector is used

The component counts two signals per connector at organization scope. It
keeps the method and the path, never the query or the body:

- **refused requests**: requests no rule classifies;
- **allowed changes**: held changes the members allowed.

When a new request shape is refused, or the same change has been allowed
three times, a proposal runs, at most once an hour per connector. It
suggests rules, each with a reason, for example: "`POST /rest/api/2/search`
is a search, a read" or "`PUT /pages/*` edits a page, a change". The admin
page lists them with Add and Dismiss.

- Add appends the rule to the connector's policy, clears the signals the
  rule now covers, and reruns the review (§1.1).
- A suggested rule must classify at least one recorded signal, and its
  method and path must be valid, or it is dropped.
- A proposed `read` rule says plainly that agents will then call it
  without asking.

### 1.3 Who decides

The AI proposes, and an admin decides. A review or a proposal never
changes a connector by itself, and never enables or disables one.
Classification stays deterministic: the policy's rules decide in the
request path. No model sits between an agent's request and its answer.

## 2. The platform primitive: a service's AI tasks (`assist`)

The assist seam (docs/architecture/ai-assist.md) is, until now, only for
the web. A service that declares `assist` may run its own JSON assist
tasks for an organization, as follows.

- **Request.** `POST /v1/component-assist/{task}`, signed by the service
  over `component.SignedRequest` like its store calls. The body is
  `component.AssistRequest`:
  - `org`, the organization the task runs for;
  - `member`, optional: who the run is for, as the service says;
  - `text`, the input the service builds, at most 16,000 characters. The
    platform fences it as data.
- **Answer.** `component.AssistReply`, the task's declared fields in their
  shapes (`assist.Fields`).
- **Which tasks.** A service runs only tasks named after it
  (`<service>-…`, `component.ServiceAssistTask`), and never a task the
  platform's web or gateway runs (`wire.AssistContracts`). The task
  resolves to the organization's version, else the platform's. A
  member's own copy and a shared copy are never used: what the task says
  is the service's word to the organization, restyled only by the
  organization. The task must answer JSON.
- **Admission.**
  - The service must be distributed to the organization and declare
    `assist`.
  - The organization must not have turned assists off, and a model must
    be set (otherwise 409). The run uses the organization's model, else
    the platform's.
  - Each service has a rate per organization: 20 runs a minute.
  - Each run is an `assist` audit row: the task and its version, actor
    `service/<name>`, for whom, the model and the token counts. The text
    is never recorded.
- **No context.** A service task gets only the text the service sends:
  no room, conversation or library. The platform reads nothing on the
  service's behalf.
- **Tasks are content.** They are ordinary assist packages kept in the
  service's own repository (`content/` in bnbloc-connections) and
  published at platform scope with `bnbloc content publish`. The platform
  ships no prompt and no name for them.
- **Fallback.** Without the capability, a model or the task, the service
  answers its pages without the AI part, and says why in one line.

`component.ServiceClient.Assist` is the service's client of it.

## 3. A plain-language summary on the held-change card: evaluated, not built

The proposal: the held change's page would show what the change does in
words ("append a row to Q4 budget, Sheet1"), flag effects such as
outside recipients or a deletion, and say when the change does not match
what the member asked.

**What it would touch on the agent's side, and how much:**

| Path | Effect on the agent |
|---|---|
| The `202` the agent gets for a held change | None if its body stays byte-for-byte the same. If the summary or a "mismatch" verdict were added, the agent would read a model's judgment of its own request and argue with it or adapt to it. It must never be added. |
| Latency of the agent's request | None if the summary runs after the `202`. If the component waited for the model before answering, every held change would be seconds slower. |
| The card in the room | None: cards are notices, and `roomlog.DecodeRow` skips notices, so no agent reads them as conversation. |
| `.room/connections/*.md` | None if the summary is never written there. |
| The agent's next turn | Only through the member's decision. A false "mismatch" makes the member refuse more often, and the agent sees more refusals. That is the intended effect of a better decision, but a noisy model makes it worse. |
| Prompt injection | The body is the agent's text, so it can steer the summary ("this is harmless"). That fools the member, not the agent, and the raw request stays one click away. |

**What it would take:**

- The summary runs only after the `202`, and only on the member's page,
  not in the card text.
- The `202` body is pinned by a golden test.
- To say "does not match what you asked", the summary needs the
  member's own request. The platform would have to resolve the turn's
  request itself, so that neither the agent nor the component supplies
  it. That is a `turn` context the platform resolves, the first service
  task with any context.

**Status: not built in this round.** §1.1, §1.2 and §2 have no path to the
agent at all. This one has a narrow but real path through the member's
decision, and it needs a context primitive. It waits for an explicit
decision; the conditions above are the bar it must meet.

## 4. Design review

| Lens | Answer |
|---|---|
| Actors | Admins read reviews and decide on proposals. Members are unaffected. Agents are unaffected: no answer they receive changes. |
| Time and lifecycle | A review is kept per connector with a hash of what it reviewed, and reruns when the connector changes. Signals are bounded (50 per connector, oldest first out); a proposal runs at most once an hour per connector. |
| Scale and cost | One model call per connector save, per draft, per "Review again", and per proposal (debounced). Nothing runs per request. |
| Failure and recovery | Model off, task missing or the call failing: the page shows the connector without a review and says why; "Review again" retries. A pending review times out after two minutes. |
| Boundaries and privacy | The model sees a connector's JSON (no secret is in it: client and provider secrets are kept apart) and request methods and paths, never queries, bodies or credentials. Runs use the organization's chosen model. |
| Abuse and untrusted input | An agent's draft is untrusted: its review is fenced as data and the instructions say so. A draft built to fool the review still needs an admin to enable it, and it shows the raw JSON beside the review. A suggested rule is validated and must match a recorded signal. |
| One owner | The model is reached only through the gateway's assist seam (no model client in the component); the policy's classification stays `connector.Classify`; reviews and signals live in the component's store. |
| Defaults and fallbacks | On when the capability, a model and the task are present; otherwise the old page. |
| External contracts | `component.AssistRequest` / `AssistReply`, the `assist` capability, `ServiceAssistTask`. |
| Load on users and agents | Agents: none. Admins: a summary and ranked findings instead of raw JSON. |
| Release and compatibility | Additive: a gateway with the primitive, the component with the capability (an upgrade the admin consents to), and the two tasks published as content. Each part degrades without the others. |
| Observability | An audit row for every run; the component logs each failed run with its reason. |
| AI capability | The review and the proposals are the AI paths of this change. |
| Component or platform | The platform adds only running a service's own tasks on its model, with admission, rate and audit. Every connector noun, the prompts and the pages are the component's. |

**Pre-mortem.**
1. *A drafted connector writes "this connector is safe" into its guide to
   pass the review.* The input is fenced as data, and the instructions say
   to judge the rules, not the descriptions. The admin sees the rules
   beside the review.
2. *An admin trusts a review that missed something.* The page labels the
   review as AI and keeps the full JSON beside it. Enabling is still a
   click on rules the admin can read.
3. *Proposals pile up and nag.* There is at most one proposal run an hour
   per connector, and only on a new refused shape or a third allowed
   change. Dismiss clears the proposal.
4. *A service burns the organization's model.* Each service has its own
   rate per organization and every run is audited. An organization that
   turns assists off turns this off too.
5. *A service runs the platform's own tasks to impersonate it.* Its tasks
   must be named after the service and cannot be a contract name.
