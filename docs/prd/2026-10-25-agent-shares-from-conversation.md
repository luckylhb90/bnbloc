# The agent knows how a page goes live, and shares it from the conversation (2026-10-25)

Status: building. Owner: the room tools (`services/bridge/roomtools`, `room_setup`),
the session guidance (`internal/roomlog/guidance.tmpl`), the web's feed
(`web/src/views/room`).

## 1. Why

A real room (2026-10-25): a member asked an agent to build a page with an AI
assistant, then spent about 25 turns on "how do I share it with my team", "deploy it
with a live URL", "which model does the page use". Every answer was wrong or beside
the point, because the agent did not know three things the platform does:

1. A page an agent publishes is shared with the **Share** button on its card: a link
   the organization's signed-in members open. That link is the live URL. There is no
   deploy step and no hosting to set up.
2. A page asks the organization's AI with `app.ai` (no key, no server) and keeps data
   with the SDK. So the agent built a Node server with an API key that nobody could
   host or share, and said "I have no public URL".
3. It cannot create the link itself, so it could only tell the member to look
   somewhere it did not know.

A UI-only fix (a visible Share button) helps the next person who looks. It does not
help the member who asks the agent, and members ask the agent first. The owner:
"让 agent 知道这些功能，甚至帮用户对话完成分享操作". The agent is the product's
front door; it has to know what the product does.

## 2. What it is

### 2.1 The agent knows (one line, once per session)

The session guidance's tools section gets one fact in the form it already uses for
schedules ("use room_setup's add_schedule, never a timer in a page"): a published
page is a site; it has no server of its own, keeps data and asks the organization's AI
through the SDK (`.room/app-sdk/`), and `room_setup`'s `share` gives the person its
link. Nothing more is said in prose: the rest is in the tool, which the agent can
read with `room_help`. A capability that exists as a tool action explains itself by
being listed.

### 2.2 The agent can offer the link: `room_setup` `share`

`room_setup` gains the action `share` (`artifactId`): it checks the deliverable is
this room's and puts a **card** in the room that only the requester sees (a notice row
of kind `share-proposal` carrying a `wire.ShareProposal`), exactly as `components
publish` does for a component. Nothing is shared by the tool call. The person's
click on **Create link** calls the existing share route as them (`POST …/artifacts/
{id}/shares`, access `org`, the route's default lifetime), and the card then shows
the link with a Copy button and "More options…" (the Share dialog, to widen who may
open it or set an expiry).

Why a card and not a link in the reply: a share is access for other people. The
review of `publish_component` (2026-10-20) settled that text an agent read must never
share a person's work; the person's click is the authorization. The card keeps that
rule and costs one click, and the link appears where the conversation is.

### 2.3 What stays the same

No new tool (the action joins `room_setup`, whose brief grows by a word, paid for by
trimming elsewhere: the room tools' budgets never rise). No gateway change: the
tool posts a card; the click is the member's own request. A link wider than the
organization (signed-in anyone, anonymous) is the Share dialog's choice and the
organization's policy, as before.

## 3. Design review

| Lens | Answer |
|---|---|
| Actors | The requester sees the card and may click. Other members never see it (`for`). A member who may not create links (a visitor, a policy that refuses) gets the route's own refusal on the card. The agent acts for the requester only. |
| Time | The card stays in the history; clicked later it creates the link then. A deliverable deleted since gets the route's 404 on the card. The click's result is kept on the card while the page is open; a reload shows the card again (a second click makes a second link; links are revocable and listed in Share). |
| Security | The tool creates nothing. Access is `org` (organization members who sign in). Prompt injection in a deliverable can at most put a card in front of the person; the click is theirs. |
| Cost to agents | One guidance line (about +45 tokens once per session, a deliberate rise of the guidance budget) and one action name in the room_setup brief (the tools/list budget does not rise). |
| One owner | Link creation: the existing route and `sharesApi.create` (also what ticks the onboarding "Share a link"). The card is a sibling of `PublishProposalCard`. The guidance text lives only in `guidance.tmpl`. |
| Failure | The tool refuses a deliverable of another room (`not one of this room's`) and an unreadable one; the card shows the route's message. |
| Observability | Link creation is audited by the share route as for the dialog. The card posts a notice line like other proposals. |
| Platform or component | Platform: sharing is a platform capability; the card is generic to it. |

## 4. Not here

- The agent adding people to the room, or to a team: members stay the person's.
- A link for people outside the organization from the card: the dialog and policy.
- Making the Share button more visible on cards (a separate, UI-only change).
- Skills reaching the agent: a skill is the member's or the room's to turn on
  (recommended in the composer, `2026-10-25-suggest-what-to-use.md`).

## 5. How it is checked

Go: the action posts one card for the requester, refuses a foreign deliverable and an
empty id, never calls the share route; the guidance goldens and token budgets; the
room_setup brief test. Web: the proposal is read only whole and shown only to its
requester; a browser test against a mocked gateway clicks Create link and sees the
link; the Create link request is `access: org`. On QA: ask an agent in a fresh room
"how do I share this page", then "deploy it", and see the card both times.
