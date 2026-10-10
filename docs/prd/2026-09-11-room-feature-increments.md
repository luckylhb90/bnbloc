# Room feature increments (P1/P2 additions)

Status: done

A set of room product/UX increments layered on the participants + borrow model
(`2026-09-11-room-participants-and-borrow-design.md`). Grouped here; each has its
own commit.

## Borrow accountability — usage counter + recent borrowers (`5b00507`)

Intent: an owner should see how often and by whom their agent was borrowed.
Design: the bridge best-effort POSTs `.../authorization/record` after an
authorized borrow drive (never blocks it); `GET .../shared-agents-usage`
projects the owner's live authorizations with `runsUsed`, `runQuota`, and recent
borrowers (a borrow = audit row where actor != owner; the audit action enum has
no borrow value so it reuses `turn`). The authorization panel shows
"Borrowed N times · by X". Also fixed a real bug found while verifying:
`PutGrant`'s upsert did not clear `revoked_at_ms`/usage, so re-authorizing a
previously revoked provider stayed revoked — the upsert now resets them.

## Room-scoped borrow-authorization panel for all clients (`b264284`)

Intent: an owner (including one signed in from the browser with no local core)
needs to authorize/revoke borrowing of their own agents, scoped to a room.
Design: open the panel from the in-room handshake icon (visible to every
member) so it carries room context and works on any client; do not depend on
local-agent discovery (seed a common provider set + an "add a provider" input);
authorize/revoke call the per-room `authorization` endpoints. Authorization is
per-room in the backend (grant id keys on roomId), so authorizing in one room
does not expose the agent elsewhere.

## Single-host mode toggle (`11227c5`, `c298ee8`)

Intent: testing multi-user borrowing on one machine required the
`BNBLOC_BRIDGE_SERVE_ALL_OWNERS` env var, easy to forget. Add a persisted
"Single-host mode" setting (Switch in the bridge Configuration panel) that
enables the flag; `env_for` turns it on when the setting OR the env is set (env
kept as a launch-time escape hatch). Saving restarts the bridge, applying it
immediately. Off by default and in real deployments.

## Room feed history + pagination (`11a4bd6`)

Intent: chat lived only in realtime's in-memory replay buffer (256, lost on
restart), so a reload showed only the recent tail and no "load earlier".
Design: persist `chat_message` + `agent_messages` to a `room_messages` table
(best-effort; nil store = in-memory dev); `GET .../history?before=<seq>` pages
older than a sequence cursor; the client loads the latest page on room open and
a "Load earlier" control pages by the oldest loaded sequence.

## Agents in the members roster (`accb56d`)

Intent: agents were visible only in the composer @-list. Add an "Agents" section
to the members panel listing each in-room agent by `owner/provider`, with an
owner-online dot and a Borrow tag on other members' agents. Presentation-only.

## Reconnecting status in the header (`b8cc4aa`)

Intent: the SSE connection state was only a subtle dot with a tooltip. Show an
explicit "Reconnecting…" label, debounced 1.5s so an initial connect or a brief
blip does not flash it.

## Copy-message affordance (`7ed0efe`)

Intent: no message actions existed. Add an unobtrusive copy button in each
message's meta row. Edit/delete are intentionally deferred: they need durable
message mutation (endpoints, store updates, a mutation broadcast, feed/history
reconcile) and permission rules — a real feature, not polish.

## Per-message reply delivery (no token streaming) (`53abe15`)

Investigation outcome, not a fake: the local core exposes no token-level stream
(no session events endpoint; `/messages` REST returns only completed messages).
`reflow` already posts each completed assistant message incrementally; tightened
its poll from 1s to 400ms so multi-message turns feel live. Sub-message
streaming would need a new core capability (agent-runtime concern, not bnbloc).
