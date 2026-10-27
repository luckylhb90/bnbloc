# Model credentials for agents in the sandbox: keys that never enter it (2026-10-27)

Status: **design; slice 1 (the experiments that need no new code) done 2026-10-27, results in §8; nothing built.** Owner when built: a new component (its own
repository, `bnbloc-model-credentials`), the agent manager's catalog (which agent uses
which provider), and one small platform mechanism in the bridge and one in the
desktop (§4). Builds on `2026-10-26-sandbox-only-agents-design.md` (§5, whose
third row, a token file in the guest, this record **replaces**), `docs/architecture/
service-components.md` (egress) and `docs/architecture/sandbox-protocol.md`
(secrets, hostProxy). Reviewed against `docs/design-review.md` (§6) with a
pre-mortem (§7); the experiments that must pass before anything is built are §8.

An agent that runs in the member's sandbox must call a model, and a model call
needs a credential. The question this record answers is **where the credential
lives and what the agent can do with it**, with security first: a coding agent
reads untrusted text all day (web pages, issues, files, tool output), so the
design assumes it **will** be prompt-injected and asks what that costs.

## 1. What the owner needs

- A member can run an agent in the sandbox (OpenCode 2 today; Gemini, Qwen,
  Cline, Kilo, Auggie, DeepSeek Harness, Codex and Claude's ACP adapters run
  there already, checked, and wait for exactly this) **with their own model
  API key, without the key ever being in the sandbox.**
- Nothing about the member's own logins on the host changes. An agent whose
  access is a **subscription** login (Claude Pro/Max, a ChatGPT plan) is not part
  of this: Anthropic's terms forbid using those OAuth tokens in any tool but its
  own (read from the coverage listed under Sources; the primary page,
  `code.claude.com/docs/en/legal-and-compliance`, was not opened and must be read
  before a catalog row relies on it), and the safe way to use such an agent stays
  the hybrid mode, where the vendor's own CLI keeps its login on the host.
- The member chooses, with the consequence on the row before they choose (D1 of
  the sandbox design). Nobody else's run can use their key (D2, never borrowed).

## 2. Threat model (security first)

**Assets.** The member's model API keys; what they can spend; what they can send
to the model provider under the member's name.

**Actors and what each can do.**

| Actor | Capability assumed |
|---|---|
| The agent in the sandbox, prompt-injected | runs any code as the space user; reads the whole space (env, files, memory of its processes), makes any network request the sandbox allows |
| A borrowed or platform run on the same machine | same, for another person's or the platform's account |
| Another member's agent in another space | same, in another space of the machine |
| Anyone reading logs, rooms, prompts, crash reports | sees whatever a component writes there |
| The organization's infrastructure (gateway, store) | sees what is sent to it; this record sends it no key |
| Malware already on the member's computer | out of scope: it could read the member's own keychain and every agent's login anyway |

**Invariants (each is tested, §8 and §9).**

1. **No credential byte in the guest.** Not in env, files, process memory,
   command lines, the egress alias, the CA files, or any answer the guest gets.
   The guest holds a **placeholder** that is a fixed shape per provider and has
   no relation to the key (never a prefix, suffix or hash of it).
2. **Only the member's own runs.** The decision is the bridge's own-run decision
   (the one that already decides the owner's forwarded environment). A borrowed or
   platform run gets no placeholder, no claim, no injection.
3. **Only during the bound turn.** Between turns a claimed host answers 503 and
   nothing is sent (existing rule).
4. **Only to the hosts the provider's catalog names**, over TLS the bridge itself
   opens to that host; the request's own host is never taken from anything the
   agent sends beyond the claimed name. A redirect is never followed with the
   credential: it goes back to the agent, whose next request is to a host that is
   claimed or not.
5. **The agent's own credentials are removed, then ours set.** Every header and
   query parameter that could carry a credential for that provider is removed
   from the agent's request first (`EgressSend.Remove`), so the placeholder never
   reaches the provider and an agent cannot smuggle a header past the injection.
6. **Responses are scrubbed** of the exact credential in headers and in an error
   body (status 400 and above, at most 64 KiB), so a provider that echoes the
   key in an error does not hand it to the agent. A streamed success body is
   not scanned.
7. **Fail closed.** No component, no key, a rejected key, a TLS failure: the
   request is refused with a card that names what to do; it is never sent without
   the credential and the credential is never sent without the checks above.
8. **At rest in the OS keychain** (macOS Keychain, Windows Credential Manager),
   namespaced per account and per component, readable by that component only. A
   platform without one is refused, not given a file (§3.7).
9. **Every use is counted and shown**: requests, bytes, status, host, per run;
   the credential's name is logged, its value never.
10. **A limit the agent cannot raise**: requests per minute, per turn, per day,
    set by the member (and bounded by the organization).

**Residual risk, stated.** A prompt-injected agent **can use the credential
during the member's own turn, to the provider's host**: spend quota, send content to
the provider the member already trusts. It cannot read the key, keep it, use it
after the turn, use it from another run, or send it to another host. This is a
capability, not a secret, bounded by 9 and 10. It is the best that a model
call made by code in the sandbox allows: the same agent on the host would hold the
key itself.

## 3. The design

### 3.1 Shape

```
sandbox guest (no key)                      the member's computer
 agent ──HTTPS_PROXY, alias bearer──▶  bridge (room tools server, the proxy)
   placeholder key, local CA                │  1. at turn start: ask each local provider
                                            │     "hosts for this job?" (own run only)
                                            │  2. per request: ask "inject for this request?"
                                            ▼
                                   model-credentials component (desktop plugin)
                                     keys in the OS keychain (via the desktop's secrets API)
                                     decides: own run, agent allowed, limits, key present
                                            │  answers Inject{remove, set, query, stream, limits}
                                            ▼
                                   bridge opens TLS to the model host itself,
                                   applies the inject, **streams** the answer back
                                            ▼
                                   the provider's host (api.openai.com, …)
```

### 3.2 The parts

- **The model-credentials component** (a desktop plugin, own repository). A page
  where the member adds a key per provider, sees where it is used and the last
  uses, sets limits and removes it. Holds the **provider catalog** as data: provider
  id, the exact hosts, how the credential is attached (header name and scheme,
  or a query parameter), the **placeholder shape** (so an agent's local format check
  passes), where to get a key, a cheap check request. Decides every injection.
  An organization swaps its own build or catalog; nothing in the platform names a
  provider.
- **The agent manager's catalog** says which agent uses which provider:
  `sandbox.credentials: [{"provider": "openai", "env": "OPENAI_API_KEY"}]`. The
  placeholder goes into that variable of the agent's space. `sandbox.signIn:
  "needed"` becomes `"subscription"` (never offered: use the hybrid mode) or is
  replaced by `credentials`; an agent with neither works without signing in
  (free models). The manager's row says it before the choice: "Uses your OpenAI
  key. It stays on this computer; the sandbox sends it only to api.openai.com,
  during your own runs."
- **The bridge** gains one mechanism, **local egress providers** (§3.3), and one
  change to the exchange, **streaming** (§3.4). It owns the proxy, the local CA,
  the alias and the attestation; it learns no provider.
- **The desktop** gains one host API for plugins, **`secrets`** (§3.7): put, get
  and delete a value, namespaced, backed by the OS keychain.
- **The sandbox** gains nothing. The placeholder uses the space's existing
  environment; the CA is already in `.room/egress/`.

### 3.3 The seam: local egress providers

A plugin that declares the capability `egress-provider` is asked by the bridge,
over its loopback URL with its per-launch bearer, the same two questions the bridge
asks an organization's egress service today (`docs/architecture/service-components.md`):

- **`POST /egress/hosts`** `{turn}` → `component.EgressHosts{hosts, env}`. At each
  turn start. `turn` is built by the bridge: room, job, **drive** (`own` when the
  bridge's own-run decision says so, else `borrowed` or `platform`), and the agent's
  core and provider (so the component can allow a key per agent). The component
  claims hosts only for an own run with a key; for an own run **without** a key it
  still claims the provider's hosts, so the request is stopped with a card instead of
  leaving unauthenticated. `env` is the placeholders.
- **`POST /egress/inject`** `{turn, method, url, header}` (no body) →
  `{remove, header, query, stream, limits, card}`. Per request. The answer is
  `component.EgressSend` plus `stream: true` and `limits`. The credential is in the
  answer, in the bridge's memory for that request, never in a file or a log.
- **`POST /egress/report`** `{turn, host, status, bytes, ms}` after the request,
  for the component's counters and its page. A report never carries a body.

Why **per request** rather than one grant per turn: the component counts, limits
and can refuse each request, a rotated key takes effect on the next request, and a
removed one stops the next request. The cost is a loopback call of about a
millisecond, and the body is not sent to the component, so a large upload costs
nothing extra.

Why the **bridge** and not a gateway service: the key stays on the member's
computer. A service holds its secrets in the platform store, sealed with the
platform key: the organization's infrastructure would hold every member's personal
key. That is the right place for an **organization-owned** key (an admin enters it
once; the existing egress service, no new platform work, decides by drive and can
serve `platform` runs too) and it is a later sample, not this record's path (§9).

The seam reuses the existing shapes (`EgressHosts`, `EgressSend`, `Card`); the
local transport differs from the service's only in who attests the turn: the bridge
itself, which is on the member's computer and already holds the job.

### 3.4 Streaming (what exists, what it measured, what changes)

Another session built streaming on the member-send path while this was being
designed (`docs/prd/2026-10-27-streamed-member-sends.md`, commit `c4d356ea`):
answers stream to the command after the service's `Accept`, a request body over
8 MiB streams to a component that says `streams`, a send is bounded by activity
(2 minutes with nothing moving), `Expect: 100-continue` is answered. That is most of
what this record needs, and it is reused, not rebuilt.

**Measured against that code (E2, 2026-10-27):** it does **not** make a model
completion stream. `sendHere` reads the answer's first `MaxEgressReportBody`
(64 KiB) with `io.ReadFull` before it reports to the component and relays; a
completion arrives at a few hundred bytes a second, so the first token reaches the
agent only after 64 KiB have accumulated or the stream ends. With a 6 s stream of 100 bytes per
300 ms, the response was handed back at 6.0 s and the first bytes reached the agent at 6.0 s.
That is right for a `git clone` (the head is read at once) and wrong for SSE.

So a credential injection answers with `relay: true` (a field of `EgressSend`), and
the bridge, for that send only:

- reads the answer's **head only** (status and headers), reports nothing of the body
  to the component (an injection never needs the body), and relays each chunk as it
  arrives, flushing every write;
- ends on the existing **activity bound** (no byte either way for the window),
  never on total time; the window is the provider's catalog entry, default 10
  minutes (the 2-minute idle bound is right for git and too short for a model that
  thinks), and the proxy's own idle deadline (`egressIdle`) is raised for a relayed
  connection to match, because a stream with no bytes for two minutes while a model
  thinks must not be cut;
- scrubs the credential from response headers and, for status 400 and above, from
  the body (buffered, at most 64 KiB); a streamed success body is not scanned;
- streams the request body with no 8 MiB cap (an image), as the existing
  `streams` path does;
- reports status, bytes and time to the component when the stream ends or the agent
  drops it (`/egress/report`), so a cancelled stream is counted.

One sender stays (`sendHere`, which gains the `relay` mode); no second one.

**A second finding (E1): Codex opens a WebSocket first.** It sends `GET
/v1/responses` with `Upgrade: websocket` to the model host and falls back to
HTTPS when that fails. The proxy strips the hop headers (`Upgrade` among them), so
the request would be forwarded as a plain `GET` **with the credential injected**:
harmless but wasteful, and a provider that answers it differently would make the
fallback slow or wrong. A request with `Upgrade` to a claimed host is answered `426`
by the proxy itself and never sent, so the fallback is immediate and nothing carries
the key.

### 3.5 What the agent sees

`OPENAI_API_KEY=<placeholder shaped like a key>`, `HTTPS_PROXY` with the session
alias, the local CA. It talks to `api.openai.com` as it always does; the bridge
terminates TLS with a leaf from the local CA for the claimed host, injects, and
talks to the real host. A tool that does not use the proxy or does not trust the CA
**fails** (a TLS error or a 401 with a placeholder), visibly; it never succeeds
unauthenticated and never sends the key anywhere (there is none). Two gaps found
in the bridge's current code, both small: the proxy environment of a sandboxed
space is set only when an organization has an egress **service**, and the
placeholders a provider returns are not put in the space's environment (the
sandboxed branch returns none). Both change: a local provider alone turns it on,
and the placeholders join the space's environment for an own run.

### 3.6 Credential forms

| Form | v1 | Notes |
|---|---|---|
| API key in a header (`Authorization: Bearer`, `x-api-key`, `x-goog-api-key`) | yes | most providers: OpenAI, Anthropic API, Google AI Studio, OpenRouter, DeepSeek, Moonshot, DashScope (Qwen) |
| API key in a query parameter | yes | some Google endpoints |
| OAuth bearer from a provider's *API* login (not a subscription) | later | the component runs the flow **on the host**, where a browser exists, and refreshes there; the sandbox still gets a placeholder |
| Signed requests (AWS SigV4 for Bedrock) | later | the existing `RawQuery` and `ReplaceBody` allow it; needs the body, so the buffered path |
| **Subscription logins** (Claude Pro/Max, ChatGPT plans, Copilot seats) | **never** | terms; use the hybrid mode |

### 3.7 Secret storage

Checked: the desktop has no OS keychain integration (its own per-launch secrets are
random values in memory; a member's session tokens are files). A member's model key
is the most sensitive value the platform would hold, so the component does **not**
write it to its data directory. The desktop gets a plugin host API **`secrets`**
(`permissions.host: ["secrets"]`, beside `chat`): `PUT|GET|DELETE
{BNBLOC_PLUGIN_HOST_URL}/v1/secrets/<name>`, a value at most 8 KiB, stored in the
macOS Keychain or Windows Credential Manager under a service name derived from the
account and the plugin (never chosen by the plugin), readable only by that plugin's
own launches. On a platform with no keychain (Linux without a Secret Service) the
call is **refused with that reason**, not answered with a file, and the component
shows "this computer has no secure store". An organization that accepts a weaker
store can say so in its own build; the platform does not choose it for them.

### 3.8 Member and organization

- **Member.** Adds a key on the component's page (never in a room); sees which
  agents may use it (default: the agents whose catalog row names the provider);
  sees requests and bytes per day and the last 50 uses (time, agent, room, host,
  status; no body, no key); sets limits; removes the key (effective on the next
  request, in-flight streams finish with what they started).
- **Missing or rejected key.** The request is answered `401` by the bridge with a
  JSON body naming the cause, and a **card** shown only to the requester: "Add your
  OpenAI key" (Open: the page), "OpenAI rejected the key" (Open: the page). Once
  per job and provider.
- **Organization** (component settings, org scope): `providers` (which providers
  members may use), `agents` (which agents may use keys at all), caps on the
  member's limits. An organization that wants **its own** keys used instead runs an
  egress service with its keys; both may exist and a provider host is claimed by the
  first by name (the rule that exists).

## 4. Placement

| Part | Content / component / platform | Why | What an organization would swap |
|---|---|---|---|
| Keys, the page, limits, decisions, last-use log | **Component** `bnbloc-model-credentials` (desktop plugin, own repository) | a policy and a store with a page; the platform names no provider | its own build, or an egress service with org-held keys |
| Provider facts (hosts, header forms, placeholder shapes, key URLs, check requests) | **Component data** (the component's catalog) | facts about the outside world are data, never platform code | its own catalog |
| Which agent uses which provider, and the env variable | **Component data** (the agent manager's catalog) | per-agent fact, beside install and ACP launch | its own catalog |
| The member's choice and its consequence on the row | **Component** (the manager's page) | a page of a component | a different page |
| Local egress providers: the two questions, the attested turn, streaming, the card, the scrub | **Platform** (the bridge) | the member-authenticated proxy, confinement and the attestation of the drive (named platform reasons); it adds a mechanism, no provider | n/a |
| `secrets` host API over the OS keychain | **Platform** (the desktop) | isolation between plugins and the protection of a secret at rest (named platform reasons) | n/a |
| Audit of credential use to the organization (§9, open) | **Platform** (the gateway's audit) | audit is the platform's | n/a |
| Organization-held keys | **Component** (an egress *service*, exists) | no new platform work | its own service |
| Placeholders in the space's environment and the proxy for a local provider | **Platform** (the bridge's existing space environment) | the sandbox's environment is the bridge's | n/a |
| The sandbox | nothing new | placeholders and the CA already reach it | n/a |

Platform tests: *Name* (no provider, agent or key format in platform code),
*Fork* (a different organization patches nothing: its component and catalog are
data), *Replace* (a second provider component, or an egress service, offers the
same), *Reason* (each platform row names its reason above).

## 5. Why not the alternatives

- **A token file from a device-code login in the guest** (the sandbox design's
  third row): the agent can read it, so a prompt-injected agent can send it
  anywhere the sandbox reaches; it lands on the host's disk in a durable home; it
  needs a terminal flow the sandbox does not have. It makes the sandbox no safer
  than the host for credentials. **Dropped.**
- **The Sandbox Protocol's `secrets`** (a placeholder in the guest, the sandbox
  substitutes the value on egress to listed hosts): the key is not readable, but it
  is bound to a **machine** when the machine is created (a new key means a new
  machine), applies to **every process of the machine** (a borrowed run on the same
  machine can use it unless a separate own machine exists), has no per-use decision,
  count or audit, and cannot refuse a request. Kept as a **fallback** for a
  provider the bridge path cannot serve, only on a machine of the member's own,
  never as the first way.
- **Injecting in the sandbox's own proxy or a sidecar**: puts the credential on the
  guest's side of the VM boundary.

## 6. Design review

1. **Actors.** The member (own runs), a borrower, a platform agent, another
   member's space, the organization, a second account on one computer. Every
   decision keys on the **attested drive**, the account's own keychain namespace
   and the agent's (core, provider): a world with two accounts and two agents of one
   provider must give each its own key and refuse the other's (test).
2. **Time.** A key removed mid-stream: that stream finishes, the next request is
   refused. A turn longer than any limit: nothing is capped by time, only by
   inactivity. A bridge or component restart mid-turn: the bridge keeps its claims
   and answers 503 until the component is back (existing rule); nothing goes out
   unauthenticated. A rotated key: the next request. An uninstall: claims vanish,
   the hosts answer 503 with a card.
3. **Scale.** One streaming connection per agent request, no per-turn buffering of
   bodies; a large image upload is streamed. The component's last-use log is capped
   (50 shown, a month kept, counters per day).
4. **Failure.** Provider down: the card, not a retry loop. A 401 from the provider:
   the component marks the key rejected once and shows it; the bridge does not retry
   on its own (a retry would repeat a side effect: a billed call). A TLS or proxy
   failure in an agent: a visible error, never an unauthenticated success.
5. **Boundaries and privacy.** The key crosses nothing but the keychain, a
   loopback call and one TLS connection to the provider. Logs and cards carry the
   credential's name, never its value (tested by a canary key searched for in every
   log, the guest, the room, the gateway's traffic). The last-use log is the
   member's own; whether any of it reaches the organization is §9.
6. **Abuse.** Prompt injection is the assumed case (§2). A request to a claimed
   host with a different `Host` or SNI, a path that tries to reach another host
   (absolute-form URL, a redirect), a header that smuggles a credential, a body
   that makes the provider echo the key: each refused or scrubbed, each tested.
7. **One rule, one owner.** The own-run decision stays the bridge's (one function,
   reused). The egress sender stays one (`sendHere`, with a streaming mode). Provider
   facts live in one catalog. The placeholder shape lives in the provider catalog
   only.
7b. **Identity.** A credential's key is (account, provider, name); the agent that
   may use it is (core, provider of the agent). A request carries the turn it
   belongs to, and a missing key is answered for **that** provider, not "some other
   key of the member's". With one credential per provider in v1 the name is the
   provider; a second key per provider (work, personal) is a field added when
   needed, not a search by "the default".
8. **Defaults, unknowns.** No key: a card, not a fallback to another provider's key
   or to the host's environment. An unknown provider host: not claimed, so not
   injected, tunnelled as any other host. A catalog entry the component does not
   know: the placeholder is not set and the agent's own error shows.
9. **External contracts.** Each provider's accepted headers, error bodies and
   streaming format; each agent's proxy and CA support; HTTP/1.1 only on the
   claimed path today. All **probed against the real thing** before they are
   catalogued (§8); a catalog entry says which version it was checked with.
10. **Load.** Nothing new in prompts or tools: the agent sees an ordinary key and an
    ordinary provider. The member adds a key once.
11. **Release.** Additive: a new capability, a new component, a new host API. An
    older bridge ignores the capability (the component's claims are never asked, the
    agent in the sandbox fails visibly with a placeholder 401 and the card is not
    shown: the manager's row requires the bridge version that has the seam, as the
    sandbox rows require `privateMounts`). Optin rollout first; a rollout while
    someone is mid-turn changes nothing for that turn.
12. **Observability.** The bridge logs `bnbloc.bridge.egress` (existing) with the
    provider, host, status and bytes; the component's page shows the same; a
    canary test is the end-to-end check ("zero credential bytes reached the guest")
    and runs against the real sandbox, not a fake.

## 7. Pre-mortem

It is six months later and this went wrong. The likely ways:

1. **An agent ignores the proxy or the CA** (a compiled binary with its own roots,
   HTTP/2-only, a QUIC client, a WebSocket) and members see 401s and give up.
   → Catalog only agents that passed E1 and E4; mark the rest "not available in the
   sandbox"; the manager says why on the row.
2. **Streaming breaks** (buffers, truncates, cuts at an idle window, mishandles
   cancellation) so completions feel broken or bill twice. → E2 and E3 first;
   report every cancelled stream; a test with a slow stream and a dropped client.
3. **A provider echoes the key in an error** and the agent logs it into the room.
   → Scrub exact bytes in headers and error bodies; E6.
4. **A member's key leaks through the component** (a log line, a crash report, a
   page state, a card). → The key is only ever in the keychain and a request's
   memory; the canary test searches every sink; secrets are a redacted type in the
   component as in the desktop (`secret.rs`).
5. **The keychain prompts the member constantly** or a platform has none. → E8 on
   each platform; a clear refusal, never a silent file.
6. **Subscription tokens end up in it** (a member pastes a Claude Max OAuth token as
   an "API key"). → The provider catalog's key check rejects a token of that shape
   with the reason; the page says the terms in one line.
7. **The organization cannot see or cap spend** and asks to turn it off. → Org
   settings (§3.8) and the audit question (§9) are decided before the first rollout
   beyond optin.
8. **A borrowed run uses the key** (a path where the own-run decision is wrong).
   → The decision is the existing one; a test with an own and a borrowed run in one
   room asserts the second gets no claim; a refusal is covered by a conformance
   case in the local-provider contract.

## 8. Experiments before building

Each is run against the real sandbox (0.1.11), the real agent and, where the code
exists, the real bridge, in a throwaway environment; a failure changes the design as
stated. A fake provider stands in for the model host: a throwaway Go program that
is the proxy (TLS terminated with a CA of its own for the claimed host names, as the
bridge does) and answers like OpenAI chat completions with an SSE stream.

**Method of E1.** A sandbox machine with `egress: none` and `hostPorts` (so the
only way out is the proxy), the proxy's CA and a bundle of the guest's roots plus
it in `NODE_EXTRA_CA_CERTS`, `SSL_CERT_FILE` and the other variables the bridge sets,
`HTTPS_PROXY` with an alias, a placeholder key in the agent's own variable. Each
agent ran as a room runs it, an ACP session (`initialize`, `session/new`,
`session/prompt`), not just `--version`. Pass: the agent's model request reaches the
fake provider through the proxy, TLS accepted, placeholder key in the header the
catalog expects.

| Agent | Result | What it sends | Notes |
|---|---|---|---|
| OpenCode 2 (single compiled binary) | **pass** | `POST /v1/chat/completions` to its free-model host with `Bearer public`; the streamed chunks arrived | its own free models need no key at all; an OpenAI key would go to `api.openai.com` |
| Gemini CLI | **pass** | `POST /v1beta/models/<m>:generateContent` to `generativelanguage.googleapis.com`, `x-goog-api-key` | needs `GEMINI_CLI_TRUST_WORKSPACE=true` for a headless run |
| Qwen Code | **pass** | `POST /v1/chat/completions`, `Bearer`; streamed chunks arrived | its HOME must exist (it crashed on a missing one) |
| Kilo | **pass** (transport) | `POST /v1/responses`, `Bearer`; its own default provider also contacts `api.kilo.ai` and says "You need to sign in to use this model" | only a provider chosen with a key uses this path |
| Codex (ACP adapter) | **pass** | `GET /v1/responses` with `Upgrade: websocket`, then HTTPS | see the WebSocket finding (§3.4) |
| Claude Code (the SDK's native binary, under the ACP adapter) | **pass** | `x-api-key` to `api.anthropic.com` | an API key only: a subscription login is never offered |
| DeepSeek Harness | **pass** | `POST /anthropic/v1/messages`, `x-api-key` | DeepSeek's Anthropic-compatible endpoint |
| pi (ACP adapter) | **pass** | `POST /v1/responses`, `Bearer` | |
| Cline | **no key path** | its ACP offers only OAuth account logins (`cline`, `cline-pass`, `openai-codex`) | not offered in the sandbox |
| GitHub Copilot CLI | **no key path** | `authenticate` is a GitHub login | a subscription: hybrid mode |
| Auggie | **no key path** | "does not currently support authenticating over ACP" | not offered |

Reading it: **the proxy-and-CA path works for every agent that has an API-key way
in** (8 of 8), including the compiled binary (OpenCode 2, Kilo) and the Node ones
(Node 24 honours `NODE_USE_ENV_PROXY`). Direct access was refused throughout (the machine has no
egress), so nothing slipped past the proxy. The three that fail have no key to
inject at all, which is the case the design already refuses; the catalog marks them
`signIn: "login"` and the page says so, instead of "not yet".

| # | Question | Result |
|---|---|---|
| E1 | Does each catalogued agent send its model traffic through the proxy and trust the CA? | **Done, table above.** |
| E2 | Does an SSE completion stream incrementally through the existing send path? | **No (measured):** first bytes at 6.0 s of a 6 s stream (§3.4). A `relay` mode is part of the design. |
| E4 | HTTP/1.1 only on the claimed path (`NextProtos`): does any agent require h2? | **None did.** Eight agents negotiated `http/1.1` (curl, Node, Python offered `h2` and accepted `http/1.1`). |
| E10 | Do the agents accept a placeholder key? | **Yes:** `sk-PLACEHOLDER-x1` was accepted by the OpenAI-, Anthropic- and DeepSeek-style clients, with no local format check. The real shape per provider is still a catalog field (Gemini and others not format-probed). |
| E3 | A client that drops mid-stream, an idle provider, a 10 MiB request body | needs the `relay` mode; slice 2 |
| E5 | The agent's own credential headers are removed and the injected one wins | needs the injection; slice 2 |
| E6 | A provider that echoes the key in an error | needs the scrub; slice 2 |
| E7 | The canary: a known key through a full run, then search every sink | needs the whole path; slice 2 (the gate to any rollout) |
| E8 | The OS keychain on macOS and Windows | needs the `secrets` host API; slice 3 |
| E9 | The own-run decision with an own and a borrowed run in one room, and two accounts | bridge test in slice 2 |

## 9. Slices (each a slice of the end state, none a stopgap)

1. **Experiments** E1, E2, E4 and E10 **done** (§8). E3, E5, E6, E7 and E9 need the
   bridge's injection and `relay` mode and are the first gate of slice 2.
2. **The bridge**: the local-provider contract (conformance cases first: an own run
   claims, a borrowed one does not; a missing key answers a card; between turns 503),
   the `relay` mode of the send path (§3.4), `426` for an `Upgrade`, the scrub, the space
   environment for a local provider alone, the turn built by the bridge. Tested against a
   fake provider component, then E3, E5, E6, E7, E9.
3. **The desktop** `secrets` host API (macOS first, then Windows), with E8.
4. **The component** `bnbloc-model-credentials`: the page, the provider catalog (the
   header-key providers of §3.6), limits, last uses, the card actions.
5. **The manager's catalog** (`sandbox.credentials`, `signIn: "login"` for Cline, Copilot and Auggie), the
   row text, the placeholder into the space's environment, enabling the seven agents
   that passed E1.
6. **Organization**: the settings of §3.8 and the decision on audit.
7. Later: OAuth for a provider's API logins, SigV4, a sample egress service with
   organization-held keys, a second key per provider.

## 10. Decided by the owner (2026-10-27)

1. **Audit to the organization: yes**, for a member's use of a personal key in an
   organization's room: host, path, status, bytes, never the key or a body, named on
   the page so the member knows what is recorded.
2. **The OS keychain is required**, and a platform without one is refused. The
   owner asked for a better and safer answer where there is one: the extra
   hardening that costs little is (a) a keychain item that is **not synchronised**
   to a cloud keychain (no iCloud Keychain, no Windows roaming credentials), so the
   key exists on this one computer; (b) a **per-launch unlock only where the OS
   offers a biometric or login prompt without prompting per request**: the
   component reads the key once per run into the bridge's memory, not per request;
   (c) a key typed into the page is sent to the desktop's `secrets` API and never
   shown again (the page shows the last four characters and a check mark), and the
   page's frame has no network, so the key never leaves through the page. A hardware
   token or an OS-managed secure enclave is the next step up and is OS-specific;
   it is not promised here.
3. **Organization-held keys** (a company's enterprise quota, an internal model
   gateway, the platform's own agents, which have no member): with the existing
   egress service, after the member-key path is proven.
4. **An agent that does not pass E1, or has no key path, is not offered the
   sandbox**, and its row says why (`signIn: "login"`). Security is not traded for
   coverage.

## Sources

- Anthropic's rule on subscription OAuth in third-party tools: [The Register, 2026-02-20](https://www.theregister.com/software/2026/02/20/anthropic-clarifies-ban-on-third-party-tool-access-to-claude/5014546), [WinBuzzer, 2026-02-19](https://winbuzzer.com/2026/02/19/anthropic-bans-claude-subscription-oauth-in-third-party-apps-xcxwbn/).
- The pattern (a proxy on the sandbox's outbound path holds the credential and
  injects it for approved destinations; environment-variable injection leaves the
  secret readable by any code the agent runs): [Blaxel](https://blaxel.ai/blog/egress-proxy-api-credentials-ai-agent-sandboxes), [LangSmith Auth Proxy](https://www.langchain.com/blog/how-auth-proxy-secures-network-access-for-langsmith-agent-sandboxes), [Cloudflare Sandbox as described by W. Zujkowski](https://williamzujkowski.github.io/posts/2026-07-02-agentic-ai-sandbox-secret-proxying-gap/), [the credential-canary test in readme2demo #64](https://github.com/alphacrack/readme2demo/issues/64).
