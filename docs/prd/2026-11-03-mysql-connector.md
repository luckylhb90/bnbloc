# MySQL connector: a member's agent queries the databases that member connected (2026-11-03)

Status: proposed. Not built. Decisions D6-D10 wait for the owner (§4);
nothing below is code until they are answered. Owner: the **MySQL connector
component** (`bnbloc-mysql`, its own repository, an MCP tool of runtime
`stdio`, placement `host`). The platform adds one generic thing: two
environment variables that tell a stdio tool where it is running (§2.5).

A member connects their own MySQL servers once, in conversation, and their
own agent then queries them in their name. The member keeps several named
connections (`prod-ro`, `analytics`). The password never passes through chat,
the agent's context, the room or a log. A read just works. A change is the
member's own click, outside the conversation. In a session that is not
sandboxed the connector refuses by default, because nothing then keeps its
connection file from the agent.

## 1. Why

- connections (docs/prd/2026-10-22-connections.md) gives agents a member's
  access to HTTP systems and says "Not in this round: non-HTTP protocols (a
  database protocol, SSH)". MySQL is one. Egress through a service cannot
  carry it.
- A member's agent cannot reach a database today without the member pasting
  a DSN into the room. That is the incident connections was written for
  (QA 2026-10-21: tokens pasted into a room, stored by the agent, left behind
  in the workspace).
- Host-run MCP tools (docs/prd/2026-10-22-host-run-mcp-tools.md) are the
  existing shape for a tool that holds something the agent must not touch: it
  runs as a host process of the bridge, and the agent reaches it only as an MCP
  server. A tool's declared settings cannot hold a variable number of
  connections (at most 24 fixed settings, a `secret` is one value,
  docs/prd/2026-10-19-component-settings.md §2.1), so the connections are the
  tool's own state under its `privatePaths`.
- Checked against the code on 2026-11-03:
  - `services/bridge/mcp_tools.go` records a host tool only on the member's own
    drive (`own && t.Placement == MCPPlacementHost`, line 153). Whether the
    session is then really sandboxed is decided later, so that spot cannot tell
    the tool "you are hosted".
  - A tool's environment holds its settings, its settings-file path and, with
    `credentials: "room"`, the room relay. Nothing tells it its placement, and
    nothing says whether the run is the member's own.
  - On a drive for someone else the placement is ignored and the tool stays the
    agent's child **on the owner's machine**, with the owner's config directory
    readable (`mcpSettings`: member settings are not lent, but a file the tool
    keeps itself is not a setting).

## 2. What it is (the end state)

### 2.1 Placement table

| Part | Place | Why | What an organization would swap |
|---|---|---|---|
| The MySQL tool (connections store, statement classifier, query runner, confirmation page) | **Component**: stdio MCP tool, its own repository, signed, rolled out like any tool | Own runtime, own external system, own settings | A tool for another database speaks the same contract; this one can be left uninstalled |
| Running the tool outside the agent's tree, read-denying its state | **Platform, already built** (host placement, `privatePaths`, `denyRead`) | Isolation and confinement are platform reasons | Nothing |
| Telling a stdio tool its placement and whether the run is the member's own (§2.5) | **Platform**: two environment variables | Only the bridge knows; it is a fact about isolation and identity, and names no product | Nothing |
| The policy for an unsandboxed session | **Component setting** (org scope, `choice`) | A policy is never platform code; the tool owns it | The organization picks the value |
| Guide for agents (how to query, what the tool refuses) | **Content**: the tool's own descriptions, and a sample skill pack in the content repository if wanted | Guidance is content | Replace the pack |
| An org-wide or per-member allow list for unsandboxed use | **Not built**; later, depends on a platform member signal | See §5 | n/a |

No connection noun, no SQL, no port number enters the platform. Name test:
`git grep -i mysql` in this repository must hit nothing outside docs/ and
this record.

### 2.2 What a member does

1. In any room, tells their own agent "connect my MySQL, alias `analytics`,
   host db.example.com, database sales, user report". The agent calls the tool
   with those non-secret fields only.
2. The tool opens a confirmation page on the member's computer (§2.4). It shows
   the alias, host, port, database, user and TLS mode, and one password field.
   The member types the password there. It is stored by the tool and goes
   nowhere else. The tool then runs a test (`SELECT 1`, the server version) and
   the agent is told only ok or the error class.
3. The agent queries by alias. A read runs. A change is held: the agent is told
   "held, the member was asked", the page shows the exact statement, and the
   member clicks Allow or Deny. The agent never sees the click.
4. Any number of connections, per member. The agent must name the alias on every
   call; with one connection it is still named (D4).
5. The member lists, edits and deletes their connections from the same page.
   Deleting removes the stored password.

### 2.3 What the agent sees

Tools, one domain, few names (a tool's size in `tools/list` is paid every
turn):

- `connections`: `list` (alias, host, database, user, mode; never a password),
  `add` (non-secret fields), `remove` (alias), `test` (alias).
- `describe`: tables and columns of one alias, bounded.
- `query`: `{alias, sql}`. One statement. Refusals are plain text with the
  reason: not connected, held for the member, not a single statement, refused
  because the session is not sandboxed.

The agent gets no password, no DSN and no file path of the connections.

### 2.4 Confirmation outside the conversation

Anything that needs the member (a password, an Allow) goes through a page the
tool serves on `127.0.0.1` on a random port, for one pending action, expiring
(D9). Both the page and the action id are the tool's; the agent is told only
that the member was asked.

A page's address must not be something the agent can read, or the agent could
approve its own change. So the tool does not return the address and does not
pass a token in a command line (`ps` shows it). It writes a one-time HTML file
under its `privatePaths` (read-denied to the confined agent) holding the form
and the token, and asks the operating system to open that file in the member's
browser. The form posts to the tool's loopback port. The file is deleted on
use or expiry. This is the same family as the desktop's MCP OAuth loopback
(`internal_ext/mcp_oauth.rs`: 127.0.0.1, random port, bounded wait, state).

This is claimed only as far as it is proven (§3, pre-mortem 2): on the
platforms where `denyRead` holds. The record of host-run tools says the deny is
built for macOS; on Linux (bubblewrap) it is not built yet.

### 2.5 The platform addition: where am I running

The bridge sets two environment variables on a stdio tool it starts, and they
are the whole platform change. Both are named in `core/pkgmeta` beside
`ComponentSettingsFileEnv`, added to the reserved list so a package cannot
declare them as its own `deliver.env`, and documented in
docs/architecture/content-components.md.

- `BNBLOC_TOOL_PLACEMENT=host`: set **only** where the bridge really starts the
  tool as a host process (`services/bridge/hosttool`, the spec's `Env`). It is
  not set in `resolveMCP`: that code knows `own`, not whether the session ended
  up sandboxed. Absent means the tool is the agent's child.
- `BNBLOC_TOOL_OWN_RUN=1`: set only when the run is the member's own
  (`own`, today's `Drive`/`OwnRun` rule). Absent means borrowed, platform, or an
  older bridge.

A tool that sees neither variable behaves as the least trusted case. An older
bridge sets neither, so the connector refuses: fail closed.

**Where they are set (found while building slice 1, replaces what D6 first
said).** Both in one place, `hostToolEnv` (`services/bridge/roomtools_contrib.go`),
the one function that builds a host-run tool's environment. It already strips
the bridge's own `BNBLOC_*` from the inherited environment, so an operator's
variable can never leak through, and it appends the platform's values last.
`OWN_RUN` comes from `SessionInfo.OwnRun`, which is `drive.ownRun()`
(`drive.go:147`: the member's own drive, and not one wearing someone else's
scripts, and not a platform agent). It does **not** come from `resolveMCP`'s
`own` (`content.go:274`: `RequesterUserID == owner`), which is a weaker rule: it
would call a platform agent's run or a run with foreign scripts "own". A tool
that is the agent's child (not host-run) gets neither variable, because the
environment its server entry carries is the profile's and the agent could read
it. A value a package tries to deliver under either name is refused at install
(`platformEnv`) and, as a second line, dropped by `hostToolEnv`, so "present"
only ever means "the bridge said so".

### 2.6 When the connector serves

| Run | `PLACEMENT=host` | `OWN_RUN=1` | Result |
|---|---|---|---|
| Own, sandboxed | yes | yes | Serves |
| Own, not sandboxed | no | yes | Org policy `refuse` (default): refuses and says why. `allow-with-warning`: serves and the tool's first answer says the connection file is readable by the agent in this session |
| Borrowed or platform drive | no | no | **Always refuses**, whatever the policy |
| Older bridge | no | no | Refuses |

The policy is one org-scope setting of the tool, `unsandboxedPolicy`, a
`choice` of `refuse` and `allow-with-warning`, default `refuse`
(D2). It is set by an organization admin in Organization → Component settings,
and a member cannot loosen it.

## 3. Design review

| Lens | Answer |
|---|---|
| Actors | The member (owns connections, decides changes); their agent (queries, cannot approve or read a password); a borrower or platform drive (never served, §2.6); an org admin (sets the unsandboxed policy); another process on the machine holding no token (refused by the page). Two members on one machine each have their own member directory and connections |
| Time and lifecycle | A connection lives until the member deletes it. A pending page expires (D9) and is deleted. A query that outlives its turn is cancelled with the turn. A host tool lives with the session binding (existing). A dropped server connection is reopened on the next call. A changed policy applies at the tool's next start (the form says so) |
| Scale and cost | Per member: a bounded number of connections (D7) and a pool of at most 2 server connections each. A result is cut by rows and bytes (D7) and the cut is stated to the agent. One host process per session and tool (existing) |
| Failure and recovery | Server unreachable, auth refused, TLS failed, timeout: each is its own reason in plain words, never one generic error. A refused password leaves the connection saved with state "needs password" and says so. A held change that expires is reported as expired, never run later. Retries never repeat a write: a write is one confirmed execution, keyed by the confirmation |
| Boundaries and privacy | Passwords live only in the tool's private state, 0600, denied to the agent's tree (macOS proven, Linux not yet). Never in settings, a log, an error, a tool result or the room. Query text is not logged beyond its class and length; result rows are not logged. The member sees what leaves their machine: the page shows the host and statement |
| Abuse and untrusted input | The SQL is the agent's, which may have read hostile text. Classification is the tool's, deterministic, and never trusts a comment or a name: one statement only, first keyword allowlist for reads, everything else is a change; the read connection also runs in a read-only session so a misclassified write fails at the server. A change is never run without the member's click on the exact statement (hash bound to the alias). The agent cannot name a host: only an alias the member made |
| One owner | One classifier function, one connection store, one confirmation path, all in the tool's repository. The two variables have one spelling in `core/pkgmeta` and one setter each in the bridge. An architecture test fails if the bridge sets them elsewhere |
| Defaults and fallbacks | Read-only by default; a change always asks; unsandboxed refuses; borrowed always refuses; no variable means refuse; an unnamed alias is refused even with one connection (D4); a missing alias says so and does nothing; TLS verified by default (D8). No silent retry on another connection |
| External contracts | MCP over stdio as the host tool front serves it; MySQL wire protocol and server versions (8.x, and MariaDB, verified against real servers, not a fake); the platform contract of §2.5. Needs a conformance probe with a real MySQL in a throwaway container |
| Load on users and agents | Agents: one tool, no prompt rule, no password in context. Members: one page for a password, one click per change |
| Release and compatibility | Additive. Order: the two variables and docs in the platform first (an older tool ignores them; the new tool refuses without them), then the component. An older bridge makes the connector refuse, visibly. No schema change |
| Observability | The tool logs start, connect result class, refusal reason, held, allowed, denied, expired, never values. The bridge logs which placement and own-run it set per start. An audit line per executed change (alias, statement class, length, outcome; not the statement text) goes to the tool's own log; platform audit rows would need a primitive MCP tools do not have, so none is claimed |
| AI capability | None added. The agent writes SQL; a model in the request path would be a step with no decision of its own (LESSONS.md, take the model out of a step that has no decision in it). Fallback is not applicable: nothing depends on a model |
| Component or platform | §2.1. The platform adds two facts; everything MySQL is the component's |

**Pre-mortem.** It shipped and a month later it failed badly:

1. *A borrowed agent read the owner's databases.* The connection file is in
   the owner's config directory, the borrowed tool runs as the owner's user.
   Defence: the connector serves only with `OWN_RUN=1`; no variable refuses; a
   test runs the tool with each combination of the two variables, and a real
   borrowed drive asks a real agent to query the owner's alias (LESSONS.md:
   prove a protection by asking the real agent to cross it).
   **What the variables are not (found while building slice 1):** they are not
   a security boundary. An agent that can run a program can run the tool's
   binary itself with both variables set, so a tool that believes them serves
   whoever sets them. They tell an honest tool where it runs and make the
   default safe (an older bridge, a borrowed drive and an unsandboxed run all
   refuse without effort). What actually keeps a hostile agent from the
   connection file is the confinement: the sandbox and `denyRead` on the tool's
   `privatePaths`, and for a borrowed drive the organization's
   `requireSandboxForBorrow`. Where neither holds (an unsandboxed agent on the
   host, any platform where `denyRead` is not built), the agent can read the
   file as the same user whatever the tool refuses, and the connector's
   refusal is a statement of that, not a defence against it. The setting's help
   text says so, because `allow-with-warning` and `refuse` otherwise read as a
   difference in safety they do not fully have.
2. *The agent approved its own change.* If the confirmation token reached the
   agent (a command line, the tool's result, the page address) it could post
   the Allow. Defence: §2.4, no address or token to the agent; a real confined
   agent is asked to read the pending file and find the port, and fails. Where
   `denyRead` is not built (Linux), the connector refuses unless `allow-with-
   warning`, and the PRD does not claim the protection. This is the weakest
   point of the design: if the real-agent test shows a way, the confirmation
   moves to a channel only the desktop owns, which is a platform item.
3. *A write got through as a read.* A comment, a CTE, `SELECT ... INTO
   OUTFILE`, a stored function with side effects, a multi-statement string.
   Defence: allowlist not denylist; reject unless exactly one statement; reads
   run in a read-only session and the member is told to connect with a
   read-only database user for production; a test corpus of known tricks,
   each of which must fail red first.
4. *The tool's password file leaked through a backup or a synced folder.*
   Defence: 0600, under the app's config directory which the desktop already
   treats as private; the page states where it is kept. Not encrypted at rest in
   v1 (D10).
5. *A huge result filled the agent's context.* Defence: row and byte cuts that
   say what was cut and how to narrow (D7).

## 4. Decisions

Answered by the owner (2026-11-03):

- **D1. Placement.** An MCP tool of runtime `stdio`, placement `host`, in its
  own repository, not a service component and not a platform primitive.
  Rejected: a service with a non-HTTP egress primitive (a separate PRD, slow).
- **D2. Unsandboxed policy.** Refuse by default, an org-level setting
  (`refuse`, `allow-with-warning`), set by the organization admin, one value
  for the whole organization. Allowing one member only is **not in v1** (§5).
- **D3. Signal.** The platform tells the tool its placement through an
  environment variable; the policy is the tool's. Refined in §2.5 so it is set
  only where the tool is really hosted.
- **D4. Several connections.** Each member keeps any number of named
  connections; every call names its alias, even when there is one.
- **D5. Read and write.** Read by default; a change is held for the member's
  own click, outside the conversation.
- **D1b. Configuration in conversation.** The agent starts it with non-secret
  fields only; the secret is typed on a page on the member's computer.

**Answered 2026-11-03: the owner accepted every recommendation below
(D6-D11), so the proposals are the decisions.** The text is kept as it was
asked, so a reader sees what was weighed. Code that makes one of these choices
carries `decision: docs/prd/2026-11-03-mysql-connector.md D<n>`.

- D6-D11, recommended option of each: accepted.

**Was open, decided before code** (AGENTS.md Decision gate; each is a
number or a choice the code would otherwise invent):

- **D6. A second signal, `BNBLOC_TOOL_OWN_RUN`.** Not asked before: found in
  this review (§1, last bullet). Without it `allow-with-warning` would admit a
  borrowed drive to the owner's connections. *What is chosen*: the bridge sets
  `BNBLOC_TOOL_OWN_RUN=1` on a tool of the member's own run only
  (`resolveMCP`, where `own` is known). *Acts on*: one tool start; the key is
  the bridge's `own`. *Missing*: the connector refuses. *Two candidates*:
  same member, own run (serves) and a borrower on that member's agent
  (refuses). *Alternatives*: (a, recommended) this variable; (b) drop
  `allow-with-warning` entirely so unsandboxed always refuses, no second
  variable, loses the configurability you asked for; (c) ask the tool to
  guess from its file's owner, which cannot tell a borrower.
- **D7. Limits, none given.** Proposed: at most 16 connections per member; a
  result returns at most 200 rows and 256 KiB, cut and said; a statement times
  out at 30 s (cancelled on the server with `KILL QUERY`, not only dropped); a
  pool of at most 2 server connections per alias. *If you want other numbers or
  want any of these configurable*, say so; the rows and the timeout are the
  likeliest to matter. Rule of the project: bound by activity and the
  product's caps, not a fixed clock, so the timeout could instead be "no output
  for 30 s while the server runs it"; for a SQL statement the server gives no
  progress, so a clock is proposed, and that is the argument to check.
- **D8. TLS.** Proposed: verify the server certificate and name by default; a
  connection may be saved as "no verification" only on the page, shown in red
  and listed by `connections list`. Alternative: preferred TLS, no verification
  by default (works with self-signed servers, weaker).
- **D9. How long a pending page lives, and what a click binds.** Proposed: 5
  minutes for both a password page and an Allow; an Allow is bound to
  (alias, hash of the exact statement) and runs once; a second Allow of the
  same statement needs a new click. *If it expires or the alias is deleted
  first*: reported expired or gone, nothing runs.
- **D10. Password storage.** Proposed: a 0600 file under the tool's
  `privatePaths`, as the desktop stores MCP OAuth tokens. Alternative: the
  operating system keychain (stronger at rest; per-platform code and a
  keychain prompt that can block a headless run).
- **D12. Whose connections: the store's key (found while building slice 2).**
  The tool sees the operating-system user's config directory, which every
  account of a multi-account desktop shares (docs/prd/2026-10-22-multi-account.md),
  so a store kept only under `<config>/bnbloc-mysql` would show one account's
  databases to another. *What is chosen*: the store lives in
  `<config>/bnbloc-mysql/<first 12 hex of sha256(BNBLOC_COMPONENT_SETTINGS_FILE)>/`.
  That path is the bridge's own, one per account and per tenant variant, and
  the tool treats it as an opaque string it hashes, never parses (lens 11:
  nothing relies on the bridge's layout). *Acts on*: one store directory; the key
  is that opaque path. *Missing*: with no settings file the tool has no key, so
  it refuses and says so; it never falls back to a shared directory.
  `BNBLOC_MYSQL_STATE_DIR` overrides it for tests and operators. *Two
  candidates*: one member's two accounts (different state dirs, so different
  stores) and the same account in two tenants (different settings file names,
  so different stores). *Alternatives*: (a, chosen) this hash; (b) a third
  platform variable naming the member, cleaner but another platform primitive
  and Decision; (c) one store per OS user, which leaks across accounts and is
  refused. *Cost accepted*: switching tenant shows a different set of
  connections; the same database must be connected again there.
- **D11. Repository.** Proposed: a new sibling repository
  `/Users/user/workspace/github.com/ai/bnbloc-mysql` beside the other
  components, public-first neutral names (`maya`, `acme`, hosts under
  `example.com`), signed and published like `bnbloc-browser`.

## 5. Not in v1

- Allowing one member in an unsandboxed session. It needs the tool to know the
  member (a third platform signal, `BNBLOC_TOOL_MEMBER`, with its own Decision:
  exact `==` match on the subject, an empty or missing subject never matches,
  who may edit the list, and an audit of who was let in) and a `lines` setting
  of subjects. Until then: sandbox the member (the right fix) or temporarily
  set `allow-with-warning` for the whole organization.
- Organization-shared connections held on the server (a service component and
  a non-HTTP egress primitive; its own PRD).
- SSH tunnels, other databases, stored procedures with `OUT` parameters,
  streaming a result to a file.

## 6. Build order

1. Platform (this repository), one commit: the two names in `core/pkgmeta`
   and the reserved list, the bridge's two setters (`hosttool` spec and
   `resolveMCP`), `TestToolPlacementEnvSetOnlyByBridge`, the owner doc.
   Red check: each variable's test fails with its setter reverted.
2. The component (`bnbloc-mysql`): classifier with its trick corpus first,
   then the store and the page, then the tools. A conformance step runs
   against a real MySQL 8 and a MariaDB container.
3. Real-path verification on a member's desktop (not run until built): a
   sandboxed own session (serves); an unsandboxed own session on `refuse`
   (refuses, says why) and on `allow-with-warning` (serves, warns); a
   borrowed session asking for the owner's alias (refuses); a confined real
   agent trying to read the pending file and to approve its own change
   (fails); the long-task smoke across a tool update (the session keeps its
   conversation and the connections survive).

## 7. Why it was not caught earlier

This is a PRD, so no bug record. The two gaps found while writing it came
from reading the code against the draft: lens 1 (actors: a borrower runs the
tool on the owner's machine) and lens 9 (the signal the draft assumed did
not exist where it was assumed). Both are in §1.

## 8. As built (2026-11-03)

**Platform (this repository), uncommitted:** `pkgmeta.ToolPlacementEnv` and
`ToolOwnRunEnv`, reserved in `platformEnv`; `hostToolEnv(tool, relayURL, token,
ownRun)` is the one setter (`services/bridge/roomtools_contrib.go`);
`TestHostToolIsToldWhereItRunsAndWhoseRunItIs`,
`TestAnAgentsChildIsNeverToldItIsHostedOrOwn` and
`TestToolPlacementFactsSetOnlyByHostToolEnv`; the owner doc in
`docs/architecture/content-components.md`. Each test was seen failing with its
change reverted, for the reason it names.

**Component (`bnbloc-mysql`, a new repository, one local `git init`, nothing
committed):** `classify.go` (allowlist classifier with a trick corpus),
`gate.go` (§2.6 as one function), `store.go` (0600, one directory per account's
settings file, D12), `pending.go` and `page.go` (the loopback confirmation page),
`db.go` (read-only transaction, row and byte caps, `KILL QUERY` on timeout),
`tools.go` and `main.go` (three tools over stdio). `package.json` is accepted by
the platform's own `PrepareMCP`, and a setting that tries to deliver either
bridge variable is refused (`"... is set by the platform"`).

**Where the build departs from the text above, and why:**

- **The page address is not a path the agent is told, and the token is in the
  address's fragment.** A GET of the page serves the form without the token; the
  browser fills it from `#token` and removes it from the address bar
  (`/a.js`). Reaching the port is not enough to answer. The page also refuses a
  non-loopback `Host`, a `Sec-Fetch-Site` that is not same-origin, and a foreign
  `Origin`.
- **A change is two calls.** The first holds it and returns a request id; after
  the member answers, the agent calls again with the same alias, sql and
  `request`. The tool remembers which alias and statement a request was made
  for, so an Allow for one statement cannot be used for another, and the
  allowance is consumed under one lock (a test runs 32 callers for 300 rounds:
  a check-then-set `Consume` let up to 14 through).
- **More than one statement is refused, not held.** The member is never asked to
  approve something the connector will not run.
- **Strings are read both ways.** Whether a backslash escapes a quote depends on
  the server's `sql_mode`, which the tool does not know; a string whose end differs
  between the two readings is refused as ambiguous.
- **TLS config is in the connection config, not the driver's global registry.**
  The first version registered one config per connection: a leak and a data race
  that `-race` found.
- **The organization setting is delivered as `BNBLOC_MYSQL_UNSANDBOXED_POLICY`.**
  It is a normal org-scope `choice` setting; the platform changed nothing for it.

**Verified, on what:** `go vet`, `gofmt`, `go test -race -count=5` for
`bnbloc-mysql` against a real MySQL 8.0 in a container (the database tests skip
without one); the bridge started the real binary as a host tool and the real
`hostToolEnv` made it serve an own run and refuse a borrowed one; every
protection above has a mutation that its test catches. `go test
./core/... ./internal/archtest/... ./services/bridge/...` passes.

## 9. Not run (stated, not assumed)

- **A real confined agent trying to read the page file, find the port, or read
  the connections file.** The whole §2.4 claim rests on `denyRead`, and this has
  not been tried. It is the weakest point, and the first thing to run before any
  rollout. On Linux the deny is not built, so there the claim does not hold.
- A real sandboxed room session on a member's desktop; the long-task smoke
  across a tool update (`bnbloc smoke --scenario long-task`); the two-member
  check. The component has not been installed into a gateway.
- MariaDB and MySQL 8.4 (only 8.0.46 was run), a server with
  `NO_BACKSLASH_ESCAPES`, a server that really verifies (only the self-signed
  refusal and the explicit skip were exercised), and Windows (cross-compiled only).
- `scripts/ci.sh` for this repository (`go`, `web`, `desktop`, `secrets`), the
  desktop's Rust tests, and a secret scan (gitleaks is not installed here).
- `bnbloc cores publish-mcp` and signing: not done; nothing is published.
- A model reading the tool descriptions: nobody has seen a real agent use these
  three tools, so whether they are worded well enough is unknown.
