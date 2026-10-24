# Connections: who agents act as in a room (2026-10-24)

Extends docs/prd/2026-10-23-connections-digital-workers.md.

## 1. What it is

- **A room's owner** sets, per system, a digital worker for everyone in the
  room:
  - **by default**: every member's agents act as it, unless the member
    chose themselves for this room;
  - **required**: every member's agents act as it, with no exception. A
    required worker that is not connected refuses ("acts as Release bot,
    which is not connected yet: its owners connect it"). It never falls
    back to the person.
- **A member** chooses, per system, for themselves: in this room
  (themselves, or a worker their audience allows), or everywhere (the
  existing choice on Connections).
- **A person's own identity is only ever their own.** Nobody's agent acts
  as another person.
- **The admin's audience bounds everything.** A room's worker acts only for
  the members and the drives its audience allows. For anyone else it is
  skipped: a room never widens a worker's reach.

Who a request acts as, the first that applies (one function, `actingAs`):

1. the room's required worker;
2. the member's choice in this room;
3. the room's default worker;
4. the member's choice everywhere;
5. their own connection (their own drive only);
6. a connected worker that serves them;
7. nobody: a card.

## 2. Where people set it: pages, never the agent

- The Connections page opened **for a room** has an "In this room" table.
  It opens from the room's header (a service lists the page `inRoom`) or
  from a card in the room. For each system, the owner sets the room's
  worker and its mode, and each member sets their own choice.
- A "Connect" card offers "Choose who acts here" when a worker could serve
  the member.
- **Platform:** a page opened with `roomId` names the room. Every call
  under it reads the member's role there again (`Member.Room`,
  `Member.RoomRole`), so only the current owner sets the room's identity
  (docs/architecture/service-components.md, "A room's page").
- **The agent:** no tool, no API and no rule. Its facts line keeps one word
  per system, `jira (as Release bot)`: data, so it can say who a change
  was made as.

## 3. Extension: proposals from an agent or the AI

Every setting is written by one function, `applyIdentity(member,
IdentityChange)`. It checks the attested page member: the owner for a
room's setting, the member for their own, in the room the page was opened
for. A proposal adds a source in front of it, never a second writer:

- **An agent's proposal** ("from now on use Release bot here", said in the
  room) is `POST https://connections.bnbloc/identity` with an
  `IdentityChange`. It stores the change as pending, attested to the turn's
  requester and room, and answers the agent "waiting for the person".
  The requester gets a card; its page shows the change and applies it
  through `applyIdentity` when they confirm. A room's change still needs
  the owner: the card goes to the requester, and the page refuses unless
  they own the room. The agent's answer has the shape held changes already
  use, so agents need nothing new to read.
- **An AI proposal** (the component's own, through the `assist` primitive):
  for example, "five members of this room act as themselves in the issue
  tracker, and a worker serves them all" becomes a pending change for the
  owner, shown on the room's page.
- **A notice to the room** when its identity changes ("Jira in this room
  now acts as Release bot, set by maya") needs a primitive that posts to
  everyone in the room. Today a card reaches only the requester. It would
  come with the agent path.

None of these changes `actingAs`, the stored settings or the pages' writer.

## 4. Design review

| Lens | Answer |
|---|---|
| Actors | The room owner decides for the room; each member decides for themselves; the admin's audience bounds both. |
| Abuse and untrusted input | Nothing an agent sends changes an identity; a setting comes only from a page whose member and room role the platform attests on each call. |
| Load on users and agents | Agents: unchanged (one word in the facts line). Members: one table on the room's page. |
| One owner | `applyIdentity` writes, `identitySteps` reads, `actingAs` decides. |
| Failure and recovery | A required worker not connected refuses with whom to ask. A removed worker is skipped wherever it was set. |
| Release and compatibility | Additive: the platform's room pages, the component's settings. |
| AI capability | §3: proposals through the one writer. |
| Component or platform | The platform adds only the room and role of a page. |
