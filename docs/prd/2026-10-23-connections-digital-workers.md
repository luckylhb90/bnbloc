# Connections: agents that act as a digital worker (2026-10-23)

Status: design, for a decision. Extends docs/prd/2026-10-22-connections.md.

## 1. Why

Today a connection is a person's. Their own agent reaches a system as them,
and nobody else's agent, borrowed or the platform's, reaches it at all.
Organizations also have **digital workers**: accounts that are not a
person. The organization's directory and SSO know them; each has owners and
its own rights in the systems (an issue tracker bot, a wiki bot). Two needs
follow:

- **A person may not want to, or cannot, connect.** They lack an account in
  that system, or the work should not carry their name ("file the release
  issues as the release bot").
- **Agents that are nobody's.** The organization's platform agents, and an
  agent borrowed from a colleague, have no identity of their own to act as.
  Today they cannot use any connected system.

## 2. What it is (the end state)

### 2.1 The parts

- **A digital worker** is organization content in the connections
  component: `{id, displayName, purpose, owners, audience, account}`.
  - `owners` are members who connect it and answer for it.
  - `audience` says who may have an agent act as it: members (or every
    member), and which drives (`own`, `borrowed`, `platform`).
  - `account` is its name in the directory (what an SSO provider signs in
    as).
  An admin adds one, names its owners and sets its audience.
- **A worker's connection** is a grant like a member's, kept in the
  organization's scope (`grant:<worker>:<connector>`). An owner makes it:
  - pasting the worker's key (`apiKey`);
  - signing in as the worker (`oauth2`);
  - asking the SSO provider to authorize the worker's account
    (`provider`): its consent goes to the worker's owners, not to the
    requester.

  It has its own reach: reading only, or changes too.
- **Who an agent acts as** is decided per request by the component, from
  the attested turn, never by the agent:
  1. The requester's own drive, and the requester connected the system:
     **as the requester**. The requester may have chosen "as <worker>" for
     that system instead.
  2. Otherwise, a worker whose audience takes this requester and this
     drive, and which is connected to the system: **as the worker**. With
     more than one, the requester's choice wins, else the admin's default
     for the system.
  3. Otherwise: a card. "Connect <system>" for an own drive. With a worker
     available but not connected, "Ask <owner> to connect <worker>" for the
     requester and the worker's owners.

  A person's own connection still serves only their own drive. A borrowed
  or platform drive reaches a system only as a worker.

### 2.2 What people see

- **The member, on Connections**: per system, "as you" or "as <worker>"
  (the workers their audience allows), and which one their agents use when
  they are not connected. The card that asks them to connect also offers
  "Use <worker> instead" when one is available.
- **A worker's owners, on Digital workers** (a component page, audience:
  owners and admins):
  - its connections, and Connect for each system;
  - its reach, reading only or changes too;
  - its recent calls, each with the person who asked, the room and the
    drive.
- **The admin**: adds workers, sets owners, audience and the default
  worker per system. The AI review (docs/prd/2026-10-23-connections-ai.md)
  also reviews a worker: an audience wider than its purpose, or changes
  allowed without asking for every member.

### 2.3 What the agent sees: one more word

The facts line names how the agent acts in each system: `jira (as
release-bot), wiki (as you)`. INDEX.md says the same per system. There is
no tool, no rule and no choice for the agent to make.

### 2.4 Changes made as a worker

- A held change still asks **the requester**, the person in the room. A
  worker's write reach "changes too" skips the ask, as a member's does.
- The audit row names both the worker (who the system saw) and the
  requester (who asked), with the room and the drive. ACTIVITY.md and the
  worker's page show the requester.
- A connector may name how the system takes the requester
  (`onBehalfOf: {header | query, name}`), for systems that record it. The
  component sets it from the attested requester, never from the agent.

## 3. Platform: nothing new

The turn already attests the requester, the agent's owner and the drive;
pages already attest the member and whether they are an admin. Workers,
owners, audiences and choices are the component's. The SSO provider's
contract (`provider.Call`) names a principal instead of only a member:
`{kind: "member" | "worker", id, account}`.

## 4. Design review

| Lens | Answer |
|---|---|
| Actors | The admin creates workers and decides audiences. Owners connect them and set their reach. Members choose who their agents act as. The requester decides held changes. |
| Time and lifecycle | A worker's grant lives until an owner disconnects it, the admin removes the worker, or it is refused upstream. Removing a worker drops its grants, choices and waiting changes. An owner who leaves the organization leaves the worker ownerless; the admin sees that. |
| Scale and cost | One grant per worker and system; one rate per worker and system on top of the requester's (a shared identity hits the system's own limits first). |
| Failure and recovery | A refused worker grant asks its owners to connect again; the requester's card says who was asked. |
| Boundaries and privacy | A worker's rights may exceed the requester's: that is the point, and the risk. The audience is the admin's decision, the review flags a wide one, and every call names the requester. A person's grant never serves another person's or the platform's agent. |
| Abuse and untrusted input | A member outside the audience never acts as the worker, whatever the agent sends; acting identity comes only from the attested turn and the stored choice. |
| One owner | The identity decision is one function (`actingAs`) used by serving, sending from the member's computer, the facts and the cards. |
| Defaults and fallbacks | No worker until an admin adds one. A new worker has an empty audience and reads only. Members keep acting as themselves until they choose. |
| External contracts | `provider.Call.Principal`; `onBehalfOf` per connector. |
| Load on users and agents | Agents: one word per system. Members: one choice per system, offered on the card when it matters. |
| Release and compatibility | Additive in the component. The provider contract gains a field providers ignore until they serve workers. |
| Observability | The audit row and the activity record name the worker and the requester. |
| AI capability | The review checks a worker's audience and reach against its purpose. When requests from many members are refused for want of a connection, the proposal suggests a worker for that system to the admin. |
| Component or platform | Component. The platform adds nothing. |

**Pre-mortem.**
1. *Members use a powerful bot to read what they may not.* The audience is
   narrow by default, and the review flags a wide one. Every call names
   the requester. The worker's reach is reading only until an owner
   allows changes.
2. *Nobody knows who filed an issue.* `onBehalfOf` where the system takes
   it; otherwise the audit row and the worker's page answer it.
3. *A platform agent spams a system as the bot.* The worker has its own
   rate per system, and its owners see its calls.
4. *An owner leaves.* The worker keeps working; the admin page shows it
   ownerless and asks for a new owner.

## 5. Build order

1. Workers, audiences, a worker's grants (`apiKey`, `oauth2`), `actingAs`
   in serving, cards and facts; borrowed and platform drives as workers.
2. The provider principal and the SSO provider's worker sign-in.
3. `onBehalfOf`, and the AI checks of §4.

## 6. Open questions

- Does the directory say who owns a digital worker, so owners need not be
  typed in again?
- May an agent borrowed from a colleague act as a worker, or only the
  requester's own agent and the platform's? The default proposed here: an
  audience lists the drives it allows; none but `own` until the admin adds
  them.
