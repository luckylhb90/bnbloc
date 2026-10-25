# AI that checks its work (2026-10-25)

Status: built (bnbloc 057faf53 and the web loop; bnbloc-connections v0.8.0).

## 1. Why

Every assist in bnbloc gives one answer, once. The person sees it as the
model wrote it: a form filled, a connector drafted, a fix proposed. When
the answer is wrong, nobody tried it first. The person finds out by
saving it, enabling it, or using it.

The connections work showed what was missing.

- An OKX connector drafted from one sentence read as valid and could not
  sign. The model had put the signature beside the key instead of in it,
  and nothing checked.
- Four gateway releases went into answers the code had not expected: cut
  off at a token cap, an object where text was asked, a fence inside a
  string, a field cut at its bound.

What found the right answer each time was a loop, not a better prompt:
read the source, try the answer against reality, read what failed, and
answer again. This makes that loop a capability of the assist seam, for
every task that wants it. The person still gets one answer, and it is
one that passed its checks.

## 2. What it is (the end state)

An assist run may go through four stages. The checks are not the model's:
each is code, or reality.

1. **Sources.** The task may read what the person pointed at: pages at
   the URLs they gave.
2. **Answer.** The model answers in the task's shape.
3. **Check.** The answer is checked, and each failed check is a
   *problem*:
   - by the platform: the reply's shape (JSON, the declared fields);
   - by the caller: its own rules (a connector's validator, a form's);
   - by reality: what happened when the answer was tried (a read request
     the system refused, with its status and the body's first lines).
4. **Revise.** Given its previous answer and the problems, the model
   answers again. This repeats until the checks pass or the rounds run
   out (at most 3). The last problems go to the person with the answer, so
   a failure says why.

### 2.1 The platform (assist seam)

- **Shape repair, for every task.** A reply that is not the task's JSON
  object, or holds none of its fields, is revised once by the gateway
  itself, with the problem stated ("your reply was not one JSON object
  holding …"). Every web and service task gets this, with no change to
  the task or the caller. The audit row counts the rounds.
- **Revision requests.** `wire.AssistRequest` and
  `component.ServiceAssistRequest` gain
  `revise: {answer, problems: [≤ 8 short lines]}`. The prompt then adds
  `<previous_answer>` and `<problems>` blocks, fenced as data, and one
  fixed line saying to answer again fixing those problems. The line is
  short, pinned by a golden, and the same for every task. A caller that
  checks runs the loop. The platform bounds it: 3 rounds per request id,
  the same rate and budget as any run.
- **One loop, in each language that calls it:**
  - Go: `component.ServiceClient.AssistChecked(ctx, task, req,
    check, rounds)` for services; the gateway's own shape repair is
    `completeJSONTask`.
  - Web: `runCheckedJSONAssist(task, req, check)` for forms (over the
    pure `refineJSON`); the form's own validator is the check.

  The loop never accepts an answer that failed its check silently: it
  returns the last answer with its problems.
- **Sources: pages.** A task may declare the context `pages`. Its request
  carries `urls` (at most 3). The gateway fetches each with a public-only
  network guard: https, no private, loopback or metadata addresses, no
  redirect off the host, 2 MiB per page, 10 s. It sends the text, with
  markup removed, as `<pages>` (20,000 characters in all). The person's
  sources join the answer without being pasted.
- **Never in a prompt:** credentials. A check that tried an answer
  against a system reports the method, the path, the status and the
  body's first lines. Every header and query value a credential could be
  in is removed first; the caller does this, and the platform rejects
  problems over their bound.

### 2.2 Where it is used first

- **Connections, drafting**: the check is the connector's strict parse and
  validator. A draft that does not parse, or names a field connectors do
  not take, is revised with the error, so the admin gets a valid draft or
  the reason there is none.
- **Connections, trying**: a connector names a `test` request, a read
  that proves a connection works (`GET /api/v5/account/balance`); the
  draft includes it.
  - **The admin's "Try it"**: after connecting their own account, the
    admin runs the test. On a failure (OKX `50113` invalid sign), the
    review runs with the result as a problem and proposes the fix (a
    patch). The admin applies it, and "Try it" runs again.
  - **A member's connect**: the test runs right after they connect.
    Connections says "connected, it answers" or why not, before an agent
    finds out.
  - Only reads: a `test` must be a request the connector's read rules
    take.
- **Connections, review fixes**: a fix that would not make a valid
  connector is revised with the validator's error, instead of dropped.
- **Connections, drafting from a link**: the admin pastes the API's
  documentation URL with their sentence (`pages`).
- **Web forms, next**: the Library's drafts (an expert, a skill pack, a
  prompt pack) and Fill from a note check their answer with the form's own
  rules (`contentpkg.Form`) and revise before the person sees it.

## 3. Design review

| Lens | Answer |
|---|---|
| Actors | The person asks and decides; the model answers and revises; checks are code or reality, never the model judging itself. |
| Time and lifecycle | A run's rounds live in one request chain (at most 3); nothing is stored but the audit rows. |
| Scale and cost | A passing answer costs what it costs today. A failing one costs at most two more rounds, on the same rate and budget. Pages are fetched once per run. |
| Failure and recovery | The loop returns the last answer and its problems; callers show them. A source that cannot be fetched is a line in `<pages>`, not a failure. |
| Boundaries and privacy | Credentials never reach a prompt: probes are redacted by the caller, problems are bounded. Pages are public https only, through the network guard. |
| Abuse and untrusted input | A page's text, a previous answer and problems are all fenced as data. A page cannot widen the task's fields; a probe is read-only by its connector's rules. |
| One owner | One loop per language; one revision prompt layout (goldens); one fetcher for pages (`extgov`'s guard). |
| Defaults and fallbacks | Shape repair is on for every task. Revision and pages only where a caller or a task asks. Without an assist model nothing changes. |
| External contracts | `AssistRequest.revise`, `urls`; `ServiceAssistRequest.revise`; the `pages` context; `connector.Test`. |
| Load on users and agents | Agents: none. People: fewer wrong answers, and a reason when one is still wrong. |
| Observability | Each round is an audit row (task, round, failure class); a run's chain shares its id. |
| AI capability | It is the capability: answers that are tried before they are shown. |
| Component or platform | The loop, revision, shape repair and pages are the platform's assist seam; what a check means is the caller's (the connector validator, the probe, a form). |

**Pre-mortem.**
1. *Loops that never converge burn the budget.* At most 3 rounds, the
   same rate, and the last problems shown: a hard stop with a reason.
2. *A probe leaks a key to the model.* Probes carry no credential: the
   caller redacts headers and query values, and bounds the body to its
   first lines, before a problem is built.
3. *A page talks the model into something.* Pages are fenced data. The
   answer is checked like any other, and a person still decides.
4. *Revision makes answers drift from the request.* Every round keeps
   the original text and context; the revision only adds the previous
   answer and its problems.

## 4. Build order

1. The platform: shape repair; `revise` on both requests and the
   revision layout (goldens); `ServiceClient.AssistChecked`; the `pages` context and its fetcher.
2. Connections: drafting and fixes through `AssistChecked`; `test`
   (drafted, "Try it", after a member connects); a failed test as a
   problem for the review's fix; drafting from a link.
3. Web: `runCheckedJSONAssist`, first for the Library's drafts and Fill from a
   note.
