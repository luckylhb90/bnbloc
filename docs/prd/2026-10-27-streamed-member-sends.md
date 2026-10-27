# Streamed sends from a member's computer: git through a connector (2026-10-27)

Status: built 2026-10-27, not released (bridge: desktop; component: bnbloc-connections).

## 1. Why

A connector that sends from members' computers
(docs/prd/2026-10-25-member-sends.md, `EgressResponse.Send`) carries a
request and its answer in memory, each at most `component.MaxEgressBody`
(8 MiB). That is right for an API call and wrong for git over HTTPS: a
`git clone` answer is the repository's pack (hundreds of MB), a `git push`
request is the pack of new objects. So an agent in a sandbox cannot clone
or push a repository of the code host its member connected
(`git.example.com`), although the member's own credential is right there
on the connections page; the only alternatives are worse:

- **Give the sandbox the member's token** (an environment variable, a
  credential helper). The agent reads it and can send it anywhere. This is
  what connections exists to prevent.
- **Stream through the platform** (gateway and the connections service carry
  the bytes). The credential then never leaves the server, but every clone
  crosses the deployment, the deployment must reach every code host
  (internal ones usually it cannot), and the turn proxy's signed JSON
  envelope (a signature over the whole body) would need a streaming
  signature scheme: a new security design for a gain only against a member
  computer that is already compromised (it holds the member's own token).

The member's computer already sends these requests; it only has to stop
holding them in memory.

## 2. What it is

On the member-send path (the bridge sends the request itself with the
credentials the component gave for that one request):

1. **Answers stream.** The bridge reads the system's status, headers and
   the first `MaxEgressReportBody` (64 KiB) of the answer, reports them to
   the component exactly as today, and only after the component's
   `Accept` relays the answer to the command: that head, then the rest as
   it arrives. No size limit; a refusal of the component's own (a
   reconnect card) still replaces the answer, which is then dropped.
2. **Large request bodies stream.** A request whose body is larger than
   `MaxEgressBody` goes to the component **without** its body
   (`EgressRequest.Streamed`), and only to a component that said it reads
   that flag (`EgressHosts.Streams`); any other still gets today's `413`.
   The component decides on the method and the path. On `Send`, the bridge
   sends the body as the command streams it (the part it read, then the
   rest), once.
3. **Activity bounds it, not a clock.** A send, its body and its answer go
   on while bytes move; a connection idle for `egressIdle` (2 minutes,
   the proxy's existing idle bound) ends. The fixed two-minute bound on a
   send from this computer is gone.
4. **`Expect: 100-continue`** is answered by the proxy, so git and curl send
   a large body at once instead of after their one-second wait.

The component (connections, `bnbloc-connections`):

- says `streams: true` in its hosts answer;
- refuses a streamed request with `413` and the reason when the connector
  sends from the server (decision 1), when a policy rule that reads the
  body could take it, or when the connector signs the body;
- classifies it by method and path otherwise; a held streamed change,
  once allowed, lets the same method and URL through once in that room
  within the hour (decision 2);
- never asks for a second send of a streamed request: a refused sign-in
  drops the cached credentials and answers the refusal (renewed: "run it
  again"; dead: the connect-again card) (decision 3).

Content: the code host's connector (`git.example.com`) sends from members'
computers and lets git's three smart-HTTP paths through (`info/refs` and
`git-upload-pack` are reads, `git-receive-pack` a change).

```
git clone ─▶ bridge proxy (local CA) ─▶ connections: policy, connection, hold, audit
                │        ◀── Send: the member's credential for this one request
                └──────────────────────────────▶ git.example.com  (bytes stream; nothing kept)
```

## 3. Decisions

Asked and answered 2026-10-27 (the code carries `decision:` comments).

1. **A streamed request to a connector that sends from the server is
   refused** (`413`, "too large to go through the service; an admin can
   switch the connector to send from members' computers"), never sent from
   the member's computer instead: a server-sending connector may carry a
   digital worker's credential or a key bound to the server's address.
2. **Allowing a held streamed change allows the same method and URL once,
   in that room, within the hour.** Its body was never seen, so it cannot be
   in the key; git builds a different pack when the push is run again, so a
   body hash would hold it forever.
3. **A streamed body is sent once and never stored.** No replay after a
   refused sign-in: the cached credentials are dropped, so running the
   command again uses fresh ones; nothing of the member's code is written to
   disk.

## 4. Placement

| Part | Where | Why | What an organization swaps |
|---|---|---|---|
| Streaming the answer and the large body on the member-send path, the idle bound, `100-continue` | platform (bridge egress proxy) | the member-authenticated proxy and confinement: only the platform terminates the command's TLS | nothing |
| `EgressRequest.Streamed`, `EgressHosts.Streams` | platform (the component contract) | the contract itself | a second egress component implements it |
| What a streamed request may do, the hold and its allowance, the refusals | component (connections) | policy is the component's | its own egress component |
| The code host connector's git paths | content (a connector) | per system | its own connector |

Name test: no product, protocol or host is named in the platform; git
appears only in this record and the connector. Fork: a component that wants
another policy for streamed requests changes only itself. Replace: any
egress component may declare `streams`.

## 5. Design review

| Lens | Answer |
|---|---|
| 1 Actors | The command (untrusted) streams bytes; the component decides per request as the member or a worker, as today; a borrowed drive still never carries a worker's credentials to a member's computer (connections' existing rule). The credential reaches the member's own bridge for one request, in memory, never the command. |
| 2 Time and lifecycle | A clone of any size lasts as long as bytes move; idle 2 minutes ends it (the proxy's existing bound, now applied to the answer too). A turn that ends mid-clone: the stream finishes (the decision was made for that turn); a revoked session stops the next request. A server whose push hooks run longer than 2 minutes before answering is cut: seen as an idle connection (noted, not observed). |
| 3 Scale and cost | No buffering: memory per stream is a copy buffer and the 64 KiB head. Nothing crosses the deployment. The component sees one envelope without a body. |
| 4 Failure and recovery | Upstream unreachable: `502` with the reason, as today. A refusal after the head: the answer is dropped and the connection closed. A streamed request refused before sending: the command's body is not read further and its connection is closed (the stream is out of step). A 401 on a streamed push: no replay (decision 3); running it again works. A held push: git prints the `202` answer as a failure; the member allows it, the next push goes through. |
| 5 Boundaries and privacy | Bytes go from the member's computer to the system and back; the component never sees bodies above 64 KiB of answer, the audit carries method and path only, as today. Nothing is written to disk. |
| 6 Abuse and untrusted input | A streamed body never reaches a rule that reads bodies (refused), so a large body cannot be classified as a read by a body pattern that matches an empty body; a component that does not say `streams` never gets one (an older component would read an empty body as the request's). A second send of a streamed request is refused by the bridge whatever the component answers. The URL sent is still the request's own on the claimed host; redirects are not followed. |
| 7 One owner | `exchange` stays the one egress protocol loop for turns and sends; `sendHere` the one sender; streaming is its only mode. |
| 8 Defaults and fallbacks | No fallback from server to member sending (decision 1). A component that does not say `streams`: `413` as today. |
| 9 External contracts | git smart HTTP: `GET …/info/refs?service=…`, `POST …/git-upload-pack` (small request, large answer), `POST …/git-receive-pack` (large request, chunked above `http.postBuffer`). Basic auth with a personal token is what code hosts take for git. |
| 10 Load on users and agents | None new: the agent runs `git clone`; the guide says a push may wait for the member. |
| 11 Release and compatibility | Additive fields. A new bridge with an older component: no `streams`, `413` above 8 MiB as before, answers stream. An older bridge with the new component: it never sends `streamed`. Order: connections first, then the desktop. |
| 12 Observability | The bridge logs each streamed send (service, host, method, status, bytes each way, `streamed`); the component records the call as today. |
| 13 AI capability | Not applicable: a transport change. |
| 14 Component or platform | Section 4. |

Pre-mortem: *a clone hangs forever* — an idle connection ends after 2
minutes either way; *the component's refusal arrives after bytes went to
the command* — the head is reported and decided before anything is
relayed; *a push is allowed for every repository* — the allowance is the
exact URL, once, in that room, for an hour; *a token leaks into the
sandbox* — the credential is set by the bridge on its own request to the
system, after the command's credentials are removed, as on every member
send.

## 6. Acceptance

- Through the proxy, a 20 MB answer and a 20 MB request body (chunked) go
  through a member send, for a component that says `streams`; the
  component's envelope of the large request has no body and `streamed`.
- The same without `streams`: `413`, nothing sent.
- A component answering its own refusal to a report: the command gets the
  refusal, not the system's answer.
- A second `Send` for a streamed request: refused by the bridge.
- connections: a streamed request on a server connector, on a path with a
  body rule, or on a signing connector is `413`; a streamed write is held,
  and once allowed goes through once; a 401 report is answered without a
  second send.

## 7. As built

- Bridge: `services/bridge/roomtools/egress.go` (`sendHere` streams the
  answer after the report and a large body as it arrives; `exchange`
  refuses a second send of a streamed body; `idleConn`, `idleReader`,
  `idleWriter` bound by activity; an answer of unknown length is sent
  chunked, since a close-delimited one would leave the command waiting on a
  connection the proxy keeps open). `EgressHosts.Streams` per service in
  `turn_services.go` and the session.
- Tests: `egress_stream_test.go` (20 MB each way, the `streams` gate, a
  refusal after the report, no second send) and `egress_git_test.go` (real
  `git clone` and `git push` of 12 MiB of random data through the proxy
  against `git http-backend`; no credential in the clone's config).
- bnbloc-connections: `streams: true`; `streamedRefusal` (server send, a
  body rule, a signing connector); `streamedRefused` (no second send;
  renewed: run it again; not renewable: connect again); `Policy.ReadsBody`;
  a held streamed change on a connector switched to the server is not made.
  Test `TestStreamedRequests`.

