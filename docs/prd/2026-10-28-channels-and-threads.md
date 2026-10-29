# Channels and threads: a conversation that holds one subject, set up by talking (2026-10-28)

Status: approved (2026-10-28): the owner accepted every recommendation of §7
(C1 to C22) and asked for all of it to be built; the code cites them as
`decision: channels PRD C<n>`. Nothing is built yet; slices in §8. The companion record of
`docs/prd/2026-10-26-rooms-work-together.md` (it relies on threads, one item per
thread, and per-thread delivery). Owner: the channel store and routes
(`internal/store/channel.go`, `services/room/channels.go`), the room stream
(`services/realtime/service.go`), the chat payload (`internal/wire/payloads.go`),
the agent's records (`internal/roomlog`, `services/bridge/roomctx.go`), notification
preferences (`internal/store/notifications.go`), the room tools
(`services/bridge/roomtools/room_setup.go`), the web room view (`web/src/views`).
Supersedes the non-goal list of `docs/specs/2026-09-15-room-channels-design.md`
where it conflicts (rename and delete in the UI, which that spec required, are
built here).

A room is where a team and its agents work; a channel is one subject in it; a
thread is one piece of that subject (a question, a decision, a work item) that
gets its own conversation, its own agent session, and ends with a summary. A
member sets channels up by talking ("make a #releases channel, only admins post,
the release expert answers there") as they set the room up today. A person who
was away asks "catch me up" and gets what happened.

## 1. Why

- The owner, 2026-10-26: "room 里的 channel 能力，现在还是有点偏弱、以及配置能力也
  偏弱 … 组织能力我希望可以参考 discord、slack 等等平台"; 2026-10-28: strengthen
  channels and conversation first ("先把我们的 channel 和对话能力再加强些").
- The owner, the same day: welcome pages and channel rules as in Discord
  (examples, not the list: "我只是想到了欢迎 rule channel，不代表我们只有这些，你可以
  发散思考下"; §2.1c is that wider look) ("像 dc
  设置欢迎页面，频道规则介绍"), and, most important, **groups**: many members can
  create channels, so let people form groups whose channels are used only inside
  the group, to keep channels manageable as the room grows ("做一个分组区，可以让不同
  人分组，只在自己的组里使用 … 为了管理频道，不至于之后没办法维护").
- Step 0 of the flows record measured what a weak channel costs
  (`docs/prd/2026-10-26-rooms-work-together.md` §11):
  - every item of a station ran in one agent session: items mixed and a run's
    cost grew five times as the session grew (§11 item 9, §11d item 3);
  - downstream stations lacked the item's earlier context (§11b item 4);
  - a message to a busy agent was steered into its running turn (§11e);
  - one room at about 100,000 deliveries a second collapsed into a reconnect
    storm, because every client receives every channel's messages (§11c).

### What exists (checked against the code, 2026-10-28)

| Area | Today | Gap |
|---|---|---|
| Channel model | Name, default flag, kind (`""` or `people`), creator, soft delete (`internal/store/channel.go:19`) | purpose, rules, section, position, archive, owner, settings |
| Manage channels | Create in the rail; rename and delete exist as routes (`services/room/channels.go:168,227`) and in `web/src/lib/api.ts:646,658`, with **no control that calls them** | the controls; archive; order |
| Agents in a channel | One session per channel (`services/bridge/drive.go:503`); a member's agent choice per channel (`internal/store/agent_prefs.go:18`); agents excluded only from the people chat (`ChannelAdmitsAgents`, `channel.go:110`) | a channel's default agent and expert; who may post |
| Threads | None: a message has no parent (`ChatMessagePayload`, `internal/wire/payloads.go:12`); a quote is pasted text (`web/src/views/Composer.tsx:147`); "by request" grouping is the nearest (`web/src/views/room/feedTurns.ts`) | threads; structured replies |
| Messages | Edit, delete (author), mentions of people and agents, attachments, search, unread per channel, typing, scheduled messages, message → task | reactions, pins, saved, forward, copy link, Markdown for people's messages (`web/src/views/room/Message.tsx:478` renders it for agents only), drafts, @here / @channel |
| Notifications | Per type, and per room mute (`QuietRooms`, `notifications.go:31`) | per channel and per thread level |
| Delivery | The web room view subscribes with `allChannels=1` (`web/src/lib/sse.ts:97`): job rows and artifact updates are room-wide, so every client takes every channel's messages | per-channel delivery |
| By talking | `room_setup add_channel` only (`services/bridge/roomtools/room_setup.go:192`) | describe, settings, section, archive, threads |
| Known limit | An agent's channel sessions share one workspace; a core that writes its instructions as files makes two experts on one agent overwrite each other (`docs/prd/2026-10-13-sessions-per-channel.md`, "Known limits") | decide how a channel's expert is worn (C9) |

## 2. What it is (the end state)

### 2.1 A channel holds its own setup

A channel gains, beside its name:

- **purpose**: a line shown under its name, read by agents as part of the
  channel's records header (the AI drafts it, §4);
- **section** and **position**: channels sit in sections ("Releases",
  "Support", "Design team") in the order the section's owners set (§2.1a);
- **settings**, set by the room's owners and admins (and the channel's creator
  for a channel they made):
  - **default agent** (owner, core, provider) and **expert**: what the composer
    picks in this channel for a member who has made no choice of their own
    (precedence C8), and what an unaddressed request to "the channel's agent"
    means;
  - **who posts**: everyone (default) or admins (an announcements channel;
    agents post there when an admin asks them);
  - **agents**: admitted (default) or not: a channel agents never read or
    answer in, generalizing today's people chat rule (`ChannelAdmitsAgents`
    stays the one owner);
- **archived**: an archived channel is read-only, leaves the rail, stays in
  search and in an "Archived" list, and can be restored. Delete stays as today
  (soft, owners and admins) for what should not be kept.

### 2.1a Sections: groups of channels, open or for their members only

A **section** is a named group of channels with its own owners, members and
rules. It keeps a room with many people and many channels in order: people form
a section for their team or subject and create channels inside it.

- **Open** sections organize: every room member sees their channels (today's
  behaviour, now grouped). A member may collapse or hide a section for
  themselves.
- **Members-only** sections are for their members: only the section's members
  see that its channels exist, read them, post in them, are notified from them
  and find them in search; their agents read them only when working for a
  member (below). Everyone else sees nothing of them (C11).
- **Members**: a section's owners add and remove members; a room member asks to
  join (a card to the owners) or joins at once when the section allows it; the
  room's owners and admins manage every section's owners and membership and see
  that every section exists, and they read a members-only section only by joining
  it, which the section's members see (C12).
- **One rule decides who may read a channel** (`MayReadChannel(member,
  channel)`): room membership, the channel's section (open or the member's),
  and for agents also `ChannelAdmitsAgents`. Every path that shows a channel or
  its content asks it: the channel listing, the stream (full and activity
  lanes), history, search, mention notifications, scheduled messages, message
  tasks, forwards and links, the files of the channel (C13), and the agent's
  records (`.room/`).
- **Agents follow the person they work for**: a session's records, files and
  reads cover the channels **its requester** may read. An owner's agent working
  for a non-member (a borrow) sees no members-only channel the borrower is not
  in; a station or AI task reads as its runner (the flows record). An agent is
  never a member of a section by itself (C14).
- **Leaving** a section re-scopes the member's streams at once; their sessions'
  records lose the section's channels on the next turn, and a session that read
  them for that member is not handed to anyone else.

### 2.1b Keeping channels maintainable

- **Who creates channels** is a room setting: anyone at the room's top level,
  only inside a section the member belongs to, or only owners and admins (C15).
  A section's owners decide the same for their section.
- **Every channel has an owner** (its creator by default) who answers for its
  purpose and settings; when the owner leaves, ownership moves to the section's
  owners, or the room's owners for the top level, and the room says so.
- **Personal order**: each member stars, hides and folds channels and sections
  for themselves; it changes nothing for others.
- **Two shared levels, never more** (C23): a room has sections and channels;
  a channel's own work goes into threads (§2.2). A section is never nested in
  a section: every level a reader passes would carry rules, owners and read
  access (`MayReadChannel`) of its own, and a member, an agent's records and
  the directory would all have to walk it. What people ask a deeper level for
  is met elsewhere:
  - **"My own corner of the team's channels"** is the member's **personal
    groups** (C24): folders of the rail that only the member sees, named and
    filled by them (any channel they joined, from any section), folded and
    ordered by them. Slack's custom sections; nothing changes for anyone else.
  - **"Somewhere each colleague works"** is a channel per person in the team's
    section, owned by that person (a channel owner, §2.1b), its pieces of work
    as threads.
  - **"My agent's private work"** is the member's own room, linked to the team
    room (the flows record's links), not a level inside the team room.
- **Tidying, by AI, decided by people**: on request, or as a room's scheduled
  task (content), the AI lists channels with no activity for a long time, with
  no purpose, outside any section, or overlapping another, and proposes
  archiving, moving or describing each; a section's owners or the room's admins
  accept each proposal. Nothing is archived by itself.

### 2.1c What a channel can be: a few switches, many kinds (presets are content)

Slack and Discord ship kinds of channels (text, forum, announcement, Q&A, voice,
stage). bnbloc ships the **switches** those kinds are made of; a kind is a
**channel preset**, content in the Library that any member applies, copies and
changes (a room package carries its channels' presets too). No kind is named in
the platform.

The switches a channel gains, beyond §2.1:

| Switch | What it does | A kind it makes, as a preset |
|---|---|---|
| **Posting style**: messages, or every post opens a thread | A post is a titled thread; the channel lists its threads (open first, by activity) | a **forum**: requests, bugs, ideas, questions; a flow station (one item, one thread) |
| **Thread tags and statuses** the channel's owners define | Tags filter the list; a status moves (open, in progress, done) and can show as a board | a **tracker** of a team's items, with a board view |
| **Post template** | A forum post starts from the channel's template (fields to fill) | a **bug report**, a **purchase request** |
| **Agent participation**: when mentioned (default), to every question, to every thread opened, or never | The channel's default agent answers by this rule; a person still steers or stops it | a **help desk** answered first by an agent; an **assistant** channel |
| **Mark answered** and **save to knowledge** | A thread's best reply is marked; the AI proposes a knowledge entry (the room knowledge component) | a **Q&A** channel whose answers build its FAQ |
| **Followable**: other rooms may follow it | A message posted here appears in the following rooms' chosen channel, with a link back (a link of the flows record, consented by this room) | an **announcement** channel across a team's rooms |
| **Tabs**: the channel's pinned app, document, board or files | The channel opens on its tab when set (an interactive app or a file previewer) | a **dashboard**, a **spec**, a **runbook** channel |
| **Feed of a source**: a webhook, a room event, a schedule posts here | The channel is where an outside system's or a task's messages land | an **alerts** or **deploys** log channel |
| **Slow mode**: at most one post per member per interval the owners set | Calms a busy channel | a large **general** channel |
| **Retention** within the organization's policy | Messages older than the channel's setting are removed (the existing retention mechanism, per channel) | an **ephemeral** chat |
| **Live** (the live-channel record) and **voice** (the voice record) | A low-latency session for a shared file or app; a voice room with an AI transcript | a **co-editing** or **huddle** channel |

- A **channel preset** (`bnbloc.channel-preset.package/v1`, a content kind) holds the
  switches, the purpose and rules text, the default expert, the tabs (by app
  package), the post template, the tags and statuses, and its scheduled tasks;
  applying one sets a channel up in one step and is undoable. Samples (forum,
  help desk, announcements, tracker, log, Q&A) live in their own repository.
  **The preset and the switches meet at the settings keys**: a preset's
  `channel.settings` is the channel route's settings document by key
  (`whoPosts`, `noAgents`, `slowModeSeconds`, `agentParticipation`,
  `postingStyle`, `tags`, `statuses`, `postTemplate`), applied by one PATCH,
  so the room service stays the one owner of what a setting means and a key
  it learns needs no change to the kind.
- **Channel membership and a directory**: a member's rail shows the channels
  they **joined**; every open channel can be found in the room's channel
  directory (search by name and purpose, with activity and owner) and joined in
  one click (C18). Joining is following, not access: open channels stay
  readable by every room member.
- **Shared across rooms**: a channel followed by another room (above) is the
  first cross-room channel; a channel two rooms both post in (a shared channel)
  is the organization record's question, because it mixes two rooms' members.

### 2.2 Threads: one piece of a subject, its own conversation

- A **thread** opens from any message ("Reply in thread") or is made by a
  station for a work item (the flows record, D27). It is a channel of kind
  `thread` with its **parent channel** and **root message** (one model, C1):
  history, unread, typing, read state, search, scheduled messages and agent
  jobs work in it unchanged, because they already work per channel.
- **Its own agent session**: the session key is per channel, so a thread is a
  session of its own; the agent's records for it hold the root message and the
  thread's messages, and the parent channel is a file it may read
  (`.room/channels/<id>/`), as other channels are today.
- **Shown under its parent**: the root message shows the thread's reply count,
  last activity and participants; a thread opens in a side panel; the rail lists
  the threads a member follows with unread ones first. Threads never appear as
  top-level channels.
- **Followers**: who wrote in it, was mentioned, or chose to follow; a follower
  gets the thread's unread and its notifications by their level (§2.5).
- **Title**: the AI names it from its root and first replies; anyone who may
  post edits it.
- **Done**: a person, or the flow whose item it holds, archives it; the AI
  writes a summary that is posted to the parent channel as a reply to the root
  ("decided: …; open: …"), and the thread becomes read-only. Nothing archives a
  thread on a clock (C3).

### 2.3 Messages

- **Structured replies**: a message may name the message it answers (`replyTo`,
  its sequence); the reply shows the quoted line and jumps to it. The text quote
  of today becomes this.
- **Copy link** to any message (the existing deep link, `#/room/<id>?seq=&channel=`).
- **People's messages render Markdown** with the same sanitized renderer agents'
  messages use (C7).
- **Drafts** are kept per member, room and channel or thread, across devices,
  and cleared on send (C6).
- **Reactions**, **pins** (per channel, shown in its header), **saved**
  messages (per member), and **forward** to another channel or room the member
  can post in (a new message that links its source; the source's access is not
  widened: a reader who cannot see the source sees "a message from #x").
- **@here** (followers or members active in the channel) and **@channel**
  (every member of the room who can read the channel), within the member's chat
  budget, by notification level (C5). Mentions of groups come with the
  organization record.

### 2.4 Delivery: a member receives what they look at

- The room stream carries three lanes:
  1. **room-wide** events (presence, roster, agent jobs' state, artifacts,
     room setup) for every subscriber, as today;
  2. **full** events (messages, edits, typing) only for the channels and threads
     the subscriber has open (one channel and at most one thread for the web
     room view);
  3. **activity** for every other channel and followed thread the member may
     read: one small `channel_activity` event per channel (its last sequence and
     whether it mentions the member), coalesced to at most one a second, which
     drives unread badges and the rail.
- Switching channels re-scopes the stream without reopening it (a subscription
  update), so no history replay is triggered by navigation.
- A slow receiver loses its own stream only; it never holds the room's
  delivery (the live-channel record owns the hub's per-subscriber buffers).
- The bridge keeps reading the channels its agents may read, as today.

### 2.5 Notifications by channel and thread

- Each member sets, per channel: **all** (default for a channel they post in),
  **mentions** (default otherwise), or **muted**; per followed thread: all or
  muted. Room mute stays and wins. Quiet hours per member (no notification
  between two times in their zone, except a person's direct mention when they
  allow it).

### 2.5a Welcome, rules and onboarding

- **The room's welcome page**, shown to a member the first time they open the
  room and from the room's header afterwards: the welcome text (today's), the
  room's **rules**, **recommended channels** with a line each on what they are
  for, the **sections** they may join, and the quick prompts (today's). Set by
  the room's owners and admins, or by talking (§2.6).
- **Rules** exist for the room and for each section and channel. A channel's
  header shows its purpose and a link to its rules; agents read the room's,
  section's and channel's rules in the channel's records header, as part of the
  channel they answer in (no new tool).
- **Agreeing to the rules** may be required by a room before a member's first
  post (C16): the member reads them and agrees once, which is recorded; a change
  to the rules asks again. Visitors read without agreeing.
- **Onboarding**: the welcome page asks the new member which sections they want
  (open ones join at once, members-only ones send a request) and points them to
  the recommended channels; the AI answers "where do I ask about X?" from the
  channels' purposes.

### 2.5b A new room starts with its channels: room templates

Creating a room offers to start from a **room template** (C25): a room package
(docs/prd/2026-10-20-rooms-as-components.md) that carries a whole channel
layout, not only channel names:

- the package's `channels` become entries with a name, purpose, rules, section
  and settings (who posts, agents admitted, slow mode), and the package gains
  `sections` (name, rules, visibility; members-only ones with slice 2). The
  channel list is objects only: no package carries channels yet, so the
  name-only shape is replaced, not kept beside it;
- the create dialog lists the templates the member may use (their own, the
  organization's, the platform's samples) with a preview of the rail it makes;
  "Plan with AI" (the `plan-room` assist) proposes the sections and channels
  for the room's stated goal, from a template or from scratch, shown as that
  same preview, and nothing is made until the member creates the room;
- with no template and no plan the room has its default channel alone, as
  today: the platform names no layout;
- samples (a team, a project, a release, a support desk, an end-to-end testing
  room) are content in the default content repository, copied and changed
  like any package; "Save as template" captures a room's layout back into a
  package (the existing room capture, now with sections and settings).

### 2.6 Set up by talking

`room_setup` gains one action, `channel` (one sentence brief; rules in
`room_help`), taking a channel id and the fields to change: name, purpose,
rules, section, position, default agent, expert, who posts, agents admitted,
archive or restore; and the `set` action takes the room's rules and recommended
channels. Sections are made and their members managed by people (a section's
membership is consent about who reads what: never an agent's act). As today, the agent proposes the change, the member agrees,
it acts as the member on the routes people use, the room shows the line, and the
change is undoable. Deleting a channel stays a person's click. The tool budget
holds (`TestPromptBudgets`): the action fits by shortening.

## 3. Placement

| Part | Place | Why (platform: the named reason) | What an organization would swap |
|---|---|---|---|
| Channel purpose, rules, section, position, archive, owner | Platform | The room's data model; who may change it (roles) | Nothing |
| Sections (open or members-only), their members and owners | Platform | Isolation and consent: who may read what | Nothing |
| `MayReadChannel` and its use on every read path | Platform | Isolation: one rule, every consumer | Nothing |
| Creation policy, personal order | Platform | Authorization; a member's own preferences | The policy's value |
| Welcome page, rules, agreement, onboarding | Platform (data, gate) | The gate before posting is consent; the rest is the room's data | The texts (content) |
| Tidying proposals, rules and welcome drafting, "where do I ask" | Assists (AI) | Proposals a person accepts | The prompts (content) |
| Switches: posting style, tags and statuses, post template, agent participation, mark answered, followable, tabs, feed of a source, slow mode, retention | Platform | The channel's data and the rules the platform enforces (who posts, how often, what is kept, what crosses rooms with consent) | The values |
| Channel kinds (forum, help desk, announcements, tracker, log, Q&A, dashboard) | Content (channel presets, samples) | Use cases | Everything |
| Channel preset kind, membership and directory | Platform | The content kind contract; a member's own following | Nothing |
| Channel settings (default agent, expert, who posts, agents admitted) | Platform | Authorization (who posts), isolation (agents admitted) | The values |
| Threads (a channel kind with parent and root) | Platform | The one channel model; sessions; read state | Nothing |
| Followers, notification levels, quiet hours | Platform | Who is told: notification preferences are the platform's | Defaults |
| Per-channel delivery lanes | Platform | Isolation and the realtime contract | Nothing |
| replyTo, reactions, pins, saved, forward, drafts | Platform | The message contract; forward keeps the source's access (consent) | Nothing |
| Markdown for people's messages | Platform (web) | The one renderer and its sanitizer | Nothing |
| `room_setup channel` | Platform | The agent door: acts as the member | Nothing |
| Purpose drafting, thread titles, archive summaries, catch-up, split and section suggestions | Assists (AI) | Proposals a person accepts | The prompts (content) |
| Conventions (when to thread, what a release channel is) | Content | A use case | Everything |
| Personal groups of the rail (C24) | Platform (web, member's own data) | A member's own preferences | Nothing |
| Room template layout: sections and channel setup in a room package (C25) | Platform (the package contract) | The content kind contract | Nothing |
| Template samples (team, project, release, support, E2E) and the layout a plan proposes | Content; Assist (AI) | Use cases; proposals a person accepts | Everything; the prompt |

Name test: the platform's new nouns are purpose, rules, section, owner,
thread, follower, reaction, pin, draft, lane, posting style, tag, status,
template, participation, tab, preset. No forum, help desk, announcement, team,
release or other use case.

## 4. AI capability

- **Draft a channel's purpose** from its name and last messages; a member
  accepts or edits it.
- **Suggest a thread**: when two subjects interleave in one channel ("three
  people are discussing the login bug inside the release thread"), the channel
  shows a suggestion to move the replies into a thread; a person clicks.
- **Name a thread** from its root and first replies.
- **Summarize on archive**: the summary posted to the parent (decided, open,
  who owns what), which also feeds the flows record's item dossier.
- **Catch me up**: for a channel or the whole room, what happened since the
  member last read it, with links; on the member's assist budget.
- **Suggest sections** when a room has many channels, and **tidy** (§2.1b):
  idle, undescribed, unsectioned or overlapping channels, each with a proposal.
- **Draft the rules and the welcome page** from the room's purpose and its
  channels; the owners edit and publish them.
- **"Where do I ask about X?"**: answered from the channels' purposes and
  rules, with the channel to open.
- **Make a channel from words**: "a channel where the team files bugs, the
  tester agent triages each one" becomes a preset applied (forum, post
  template, tags, agent participation) shown before it is made.
- **Route a message**: when a post belongs to another channel or an existing
  thread ("this looks like the login bug thread"), the AI suggests moving or
  linking it; a person clicks.
- **Answer and capture**: in a channel whose agent answers questions, the AI
  proposes saving an answered thread to the room's knowledge.
- **Digest**: a member may receive a daily or weekly digest of the channels
  they joined (decisions, open questions, mentions), as a scheduled AI task of
  their own.
- **Translate**: a member reads a channel in their language on request (an
  assist; the original stays).
- All of it through the assist seam; with the model off, every control works
  by hand.

## 5. Design review

| Lens | Answer |
|---|---|
| Actors | Section owners and members (membership, the section's channels and rules), owners and admins (settings, sections, archive), a channel's creator (its own channel), members (threads, reactions, drafts, levels), visitors (read only: no thread, reaction, draft), agents (post where admitted; never set settings except through `room_setup` as a member), followers. |
| Time and lifecycle | An archived thread keeps its session's records; a later message in it is refused (read-only) until restored. A deleted parent channel archives its threads. Drafts survive devices and restarts. A thread's agent session restarts like any channel session after a bridge restart. |
| Scale and cost | Threads multiply channels: listing a room's channels excludes threads (they are listed per parent and per follower), so the rail stays small; the activity lane is coalesced per channel; the full lane is per open channel (load test target below). Summaries and catch-up spend the member's assist budget. |
| Failure and recovery | A summary that fails leaves the thread archived without a summary and says so; a stream re-scope that fails reopens the stream once and says so if that fails. |
| Boundaries and privacy | Members-only sections are the first channel-level read restriction for people: one rule (`MayReadChannel`) is asked by every read path (listing, both stream lanes, history, search, notifications, schedules, tasks, forwards, links, files, agents' records), each with a test as a non-member; agents read as their requester; room admins see that a section exists and read it only by joining, visibly; agents-not-admitted channels are never mirrored to agents; forward never widens a source's audience. |
| Abuse and untrusted input | @channel within the chat budget and by level; reactions bounded per message; forwarded and quoted text fenced for agents; Markdown sanitized as agents' is. |
| One owner | `ChannelAdmitsAgents` (agents admitted), one channel store for threads, one notification-level resolver, one renderer, the stream's lane filter in one place. |
| Identity (7b) | Threads by channel id (never by title), replies by sequence, a channel's default agent by (owner, core, provider). |
| Defaults and fallbacks | A missing default agent: the composer asks (no guess); a missing purpose: none shown; a member with no level: mentions, or all where they posted. |
| External contracts | None new; the stream's lanes are an extension of today's `allChannels` (older clients keep working while both exist during rollout, then the old mode is removed, pre-launch). |
| Load on users and agents | Agents: a thread is a channel they already understand; the purpose line in the records header; one `room_setup` action. People: one settings dialog per channel. |
| Release and compatibility | Additive columns (purpose, rules, section, position, owner, archived, settings, parent, root), `replyTo` on the payload, new tables (reactions, pins, saved, drafts, follows, channel levels). |
| Observability | Per-lane delivery counts; summary and catch-up runs in assist audit; `bnbloc-loadtest` gains a per-channel mode. |
| AI capability | §4. |
| Placement | §3. |

**Acceptance for delivery** (the load test of §11c of the flows record, extended):
one room, 1,024 members spread over 16 channels, 100 messages a second across
them, each member with one channel open: no reconnect storm, unread badges
correct in every channel, delivery p99 under 100 ms on the same laptop.

## 6. Pre-mortem

1. **Threads scatter the conversation** so nobody finds anything. Threads show
   under their root with counts, follow lists rank them, archive summaries land
   in the parent, catch-up reads across them.
2. **Per-channel delivery breaks unread or job rows** (the reason `allChannels`
   exists). Room-wide kinds stay on the room lane; the activity lane drives
   unread; the web keeps a test that every channel's badge moves while another
   is open.
3. **Two experts on one agent overwrite each other's instructions** in the
   shared workspace (the known limit). C9 decides before any per-channel expert
   ships.
4. **A members-only section leaks through a side door**: search, a mention
   notification, a forwarded message, a file in the Files panel, a link
   preview, or an agent's records for a borrower. One rule, asked everywhere,
   with a non-member test per path; a new read path cannot land without it (an
   architecture test lists the routes that return channel content).
5. **Channels sprawl anyway.** Creation policy, owners, sections, personal
   order and the AI's tidying proposals; the measure is channels without a
   purpose or activity, shown to the room's admins.

## 7. Decisions (accepted by the owner, 2026-10-28: every recommendation)

**C1. A thread is a channel** of kind `thread` with a parent channel and a root
message, not a separate table. *Why:* sessions, history, unread, read state,
search, schedules and jobs already work per channel. *Alternative:* a thread id
on messages (every per-channel feature rebuilt for threads). *Recommended.*

**C2. Who opens a thread.** Any member who may post in the parent; an agent opens
one only for a station's item or `ask_agent` with a new thread (as its runner or
requester). Visitors never. *Recommended.*

**C3. When a thread ends.** Only when a person archives it or the flow item it
holds is done; the AI may suggest archiving a thread quiet for long, never does
it. *Alternative:* auto-archive after N days (a fixed time a member did not
give). *Recommended.*

**C4. Default notification level.** All for a channel the member has posted in,
mentions otherwise; followed threads all; room mute wins. *Recommended.*

**C5. @here and @channel.** Any member who may post; counted against their chat
budget; @channel reaches members at "all" or "mentions" level, never muted ones;
@here reaches followers and members who read the channel in the last hour.
*Alternative:* admins only in rooms over a size (a number nobody gave).
*Recommended:* the proposal; an organization may restrict it later by role
(the organization record).

**C6. Drafts.** Kept on the server per (member, room, channel or thread),
latest write wins, cleared on send, at most one per channel. *Alternative:* the
browser only (lost across devices). *Recommended.*

**C7. Markdown for people's messages.** The agents' sanitized renderer, with no
remote images (as for agents) and code blocks; a member who wants plain text
escapes it. *Recommended.*

**C8. Which agent the composer picks in a channel.** The member's own choice for
that channel; else the channel's default agent if the member may drive it (own,
authorized borrow, or a platform agent of the room); else the room's default as
today; else ask. Nothing is chosen by "first" or "online". *Recommended.*

**C9. A channel's expert on a shared workspace.** A channel (or thread) may name
an expert; it is worn through the session's own profile on cores that take
instructions per session. On a core that reads them from workspace files, a
channel expert different from the room's runs in a workspace of its own for that
channel (its files apart from the room's other channels), and the channel's
settings say so before it is saved. *Alternatives:* refuse channel experts on
such cores; accept the overwrite (today's known limit). *Recommended:* the
proposal.

**C11. What a non-member sees of a members-only section.** Nothing: not its
name, channels, counts or activity; a link to one of its messages opens "you
cannot see this". *Alternative:* the section's name with a "request to join"
button (discoverable, but it reveals that the group and its subject exist).
*Recommended:* nothing by default; a section's owners may make its name
visible for joining.

**C12. Room owners and admins and members-only sections.** They see every
section's name and members and manage its owners and membership; they read its
channels only by joining, and joining is shown to the section's members and
audited. *Alternative:* admins read everything (simpler, no privacy inside a
room); admins see nothing (a room with sections nobody can maintain).
*Recommended.*

**C13. Files of a members-only channel.** A deliverable or attachment made in a
members-only channel is readable only by those who may read that channel; the
Files panel, search and links follow the same rule; publishing or sharing it
further is its author's act as today. *Alternative:* files stay room-wide (a
leak). *Recommended.*

**C14. Agents and sections.** An agent reads a members-only channel only in a
session whose requester (or runner) may read it; an agent is never a section
member itself; a borrowed agent working for a non-member sees none of it.
*Recommended.*

**C15. Who creates channels, by default.** Any member, inside a section they
belong to or at the room's top level; owners and admins may restrict the top
level to themselves (then members create channels only in their sections).
*Alternative:* admins only (orderly, but blocks teams). *Recommended.*

**C16. Agreeing to the rules.** Off by default; a room's owners may require it
before a member's first post; a change to the rules asks once more; visitors
never asked. *Recommended.*

**C17. Joining a section.** A section is "ask to join" (owners approve) or
"open to join" (any room member joins at once), its owners' choice; the default
for a members-only section is "ask", for an open section joining is only
following. *Recommended.*

**C18. What a member's rail shows.** The channels they joined (posting or being
mentioned joins them; a new member joins the recommended channels and the
sections they chose); every open channel is in the directory. *Alternative:*
every channel of the room in every rail (today; it sprawls). *Recommended.*

**C19. A channel's agent participation, by default.** "When mentioned" (today's
behaviour); a channel's owners choose "every question" or "every new thread";
an answer under "every question" says it was automatic and a person can stop it.
Whether a message is a question is the agent's own reading, never a platform
rule. *Recommended.*

**C20. Who defines tags, statuses and templates.** The channel's owners (and
section owners for their section); any member who may post sets a thread's tags
and status, and the flow that holds the thread's item may set its status.
*Recommended.*

**C21. Following a channel from another room.** The followed channel's room
consents (its owners, as a link of the flows record: its messages leave the
room); the following room chooses the channel they land in; only messages are
carried (no threads, no files beyond those the message names, D9 of the flows
record). *Recommended.*

**C22. Slow mode and retention values.** Set by the channel's owners within
the organization's retention policy; no platform default value. *Recommended.*

**C10. Who archives and restores a channel.** Owners, admins and the channel's
creator; agents through `room_setup` as such a member; the default channel and
the people chat never. *Recommended.*

**C23. How deep sections go.** Two shared levels: sections and channels, then
threads. No section inside a section (§2.1b). *Recommended; accepted by the
owner 2026-10-28.*

**C24. Personal groups.** A member's own folders of the rail, kept on the
server with their other channel marks (so every device shows them), never
shown to anyone else, never changing a channel's section. *Recommended;
accepted by the owner 2026-10-28.*

**C25. A new room's channels.** From a room template (a room package with
sections and channel setup) or an AI plan the member reviews; none by default
beyond the default channel. *Recommended; accepted by the owner 2026-10-28.*

## 8. Build order (each a slice of §2, nothing replaced later)

1. **Manage, configure, welcome**: rename, delete and archive controls; purpose,
   rules, owner, position; open sections; personal order; joining and the
   channel directory; the channel settings dialog (default agent, who posts,
   agents admitted, agent participation, slow mode); the creation policy; the
   welcome page with rules, recommended channels and the optional agreement;
   `room_setup channel`; the drafting and "where do I ask" assists.
2. **Members-only sections**: membership and owners, `MayReadChannel` on every
   read path with its non-member tests and the architecture test, agents as
   their requester, files by channel; tidying proposals.
3. **Threads**: the kind, reply in thread, the side panel, follows, thread
   unread, titles, archive with summary; `replyTo`; then the posting style
   (every post a thread), tags, statuses with a board view, post templates,
   mark answered; channel presets and their samples; "make a channel from
   words".
4. **Delivery lanes** and the per-channel load test; notification levels, quiet
   hours.
5. **Messages**: Markdown for people, copy link, drafts, reactions, pins, saved,
   forward, @here and @channel; catch-up, digest, routing and thread
   suggestions, translation.
6. **Channel experts** (C9); tabs; feed of a source; retention per channel;
   following a channel from another room (with the flows record's links).
7. **Templates and personal groups** (C23–C25): personal groups of the rail;
   sections and channel setup in room packages, the create dialog's template
   picker and preview, "Plan with AI" proposing the layout, capture with
   sections; samples in the default content repository. Can run before
   slice 2: it touches neither read access nor threads.

## 9. Verification

- Two members in one room: one archives a channel while the other has it open
  (read-only, said); a thread opened by one shows its count to the other; a
  follower is notified, a non-follower at "mentions" is not.
- An agent answering in a thread sees the root and the thread, not the parent's
  later messages (except as its file), and its session is not the parent's.
- Two channels named alike (ids decide); a thread titled like a channel (never
  listed as one).
- The delivery acceptance above, run with `bnbloc-loadtest` before and after.
- `scripts/two-member-check.sh`; a resumed session across the release (a
  channel's purpose added to the records header is proven on a resumed session).

## 10. Progress

**Slice 1, server half** (813b7edc, 408b9df2): channel setup, sections (open),
follows, room rules, the post gate (`docs/architecture/room-channels.md`).

**Slice 1, tools and web** (this branch):

- `room_setup` action `channel` (make, set up, archive, restore) and `set`'s
  room rules and recommended channels, through the agent door; deleting a
  channel, its owner, sections, the agreement switch and the creation policy
  stay people's acts. `TestPromptBudgets` holds without a raise.
- The web: the rail by section with per-member folding, the channel menu,
  settings dialog, header and Rules sheet, the directory, the archived list,
  sections, the welcome page, room settings' rules and channels, the
  composer's gate (rules agreement, slow mode, archived, admins only) and C8.
- Assist contracts `draft-channel-purpose`, `draft-room-rules`,
  `find-channel`; their prompts belong in the default content repository and
  are **not written yet** (until then the controls say the AI is unavailable
  and work by hand).

Not in it: a channel's purpose in the agents' records header (§2.5a) and
the resumed-session proof that goes with it; the tidying proposals (§2.1b,
slice 2).

**Decisions pending owner, added in this half** (each marked
`decision: pending owner` in the code):

1. An agent's room patch is the description alone, or the rules and
   recommended channels alone; the agreement switch and the channel
   creation policy stay the person's (`roomPatchAllowed`, agent_door.go).
2. An agent never changes a channel's owner (the door refuses `ownerId`).
3. The welcome page opens once per member per room in this browser (not
   stored on the server), and only for a room with a welcome, rules or
   recommended channels.
4. The channel's default agent, while it is the composer's pick, is not
   saved as the member's own choice for the channel.
5. "Join its channels" on the welcome page follows every channel of an open
   section (C17 makes joining an open section following; the PRD does not
   say whether that is all its channels).

**Slice 7a, personal groups (C24)**: `channel_groups` and
`channel_follows.group_id` (migration `2026_10_channel_groups`); the
caller's groups come with the channel list, made, renamed and deleted at
`/v1/rooms/{id}/channel-groups`, and a channel is put in one through its
follow (`groupId`, only the caller's own group). Deleting a group returns
its channels to their sections; leaving a channel drops its group. The rail
lists the top level, then the member's groups, then the room's sections,
each foldable by one rule (`foldedChannels`: a folded one still shows the
open channel and those with unread); a channel's menu offers "Move to …",
"New group…" and "Remove from my group", a group's menu rename and delete.
The agent door does not open the routes (a member's rail is theirs).

**Decisions pending owner, added in slice 7a:**

1. Groups are kept by any human member, a visitor included, as stars and
   following are (the same rule as following).
2. A member keeps at most 50 groups per room, each name up to 64
   characters (`MaxChannelGroups`, `MaxChannelGroupName`).
3. A member's groups are listed between the top level and the room's
   sections (Slack lists custom sections above channels; the top level here
   holds the default channel).

**Slice 7b, templates carry the layout** (C25):

- The room package's `channels` are objects (name, purpose, rules, section,
  settings: who posts, agents admitted, slow mode) and the package has
  `sections` (name, rules; open). The name-only shape is gone (no package
  carried channels). The validator holds them to the room service's bounds
  (`wire.MaxChannelPurpose`, `wire.MaxChannelRules`,
  `wire.IsChannelWhoPosts`); `content-limits.md` lists them.
- Capture (`draftLayout`) takes the open sections and live channels with
  their setup; archived channels, members-only sections, a channel's
  default agent and follows are said in `left`. Install (`installLayout`)
  makes the sections, then each channel in its section, then its position,
  rules and settings, through the room service's routes as the installer.
- The new-room dialog: "Start from" lists the room templates the member may
  use, shows the rail the picked one makes (`RoomLayoutPreview`) and
  creates the room by its install, with the dialog's id, name,
  description, visibility and preview switch (`RoomInstallRequest` gained
  `description` and `previewable`). "Plan with AI" proposes sections and
  channels too (the plan-room contract's `sections` and `channels`),
  shown in the same preview, made through the channel routes on Create
  (`web/src/lib/roomLayout.ts`, one owner of the layout's preview, plan
  parse and creation). Nothing is made before Create; neither gives the
  default channel alone.
- Samples (team, project, release, support desk, end-to-end testing) and
  the plan-room prompt are in the default content repository.

**Decision pending owner, added in this slice** (marked in the code):

1. A package's sections are at most 10 and its channels' names share the
   80-character bound (`maxRoomSections`, `maxRoomChannelName`): the room
   service bounds neither.

**Slice 2, members-only sections** (C11-C14, C17):

- `section_members` and `section_join_requests` (migration
  `2026_10_section_members`, new tables only). A members-only section's
  creator is its first owner and member; an owner added later becomes a
  member. Membership routes:
  - join (`open` at once, `ask` a request, the room's owner or admin at
    once);
  - leave;
  - add/approve and remove (its owners and the room's owner and admins);
  - list and decline requests.

  Every change publishes `section_membership` and is audited
  (`AuditKindSection`). A room admin's join is marked `admin` for the
  section's members (C12).
- One rule, `store.MayReadChannel` (`internal/store/channel_access.go`): the
  reader (the person; an agent reads as the person it works for) may read a
  channel of an open section or the top level, or of a members-only section
  they belong to, and an agent also only where `ChannelAdmitsAgents`. Each
  service has one owner of asking it: room `readerAccess`/`roomArtifact`,
  realtime `channelAccess`, activity `channelReaders`. Every read path asks
  it; `TestChannelReadsAskTheRule` (`internal/archtest`) fails a new one that
  does not. The paths are listed in docs/architecture/room-channels.md.
- Agents (C14): the bridge reads its channel listing and history with the
  turn's job (`?job=`, `roomlog.AgentChannelList`), which the gateway's agent
  door proxies as the job's requester. A borrowed agent working for someone
  outside a section sees none of it.
- Web:
  - the section dialog has "Members only" (at creation), "Who may join",
    members (add, remove) and requests (approve, decline);
  - the section menu has join, ask and leave;
  - the rail, directory and welcome page show what the server lists. A
    members-only section the member is not in appears by name only, never
    its channels.

  The channels e2e covers asking, approving and making an invite-only
  section.

**Decisions pending owner, added in slice 2:**

1. A members-only section is **invite-only by default** (`joinPolicy`
   `invite`: its owners add members; the rest of the room sees nothing of
   it). C17 names "ask" the default, but asking needs the section's name
   shown, which C11 leaves to its owners. `ask` and `open` show the name for
   joining, never its channels or members.
2. A section's visibility is fixed at creation: opening a members-only
   section would show its history to the room, and closing an open one
   would take it from members mid-conversation.
3. A visitor joins no members-only section (visitors are read-only).
4. A section's only owner cannot leave it (hand it to another owner first);
   an owner is removed by changing the owners, not as a member.
5. A channel goes into a members-only section only from inside it: the
   person who moves or creates it must belong to the section.
6. A share link to a deliverable of a members-only channel, once its maker
   published it, keeps working for its holders (sharing further is the
   author's act, C13); attachments (unguessable ids, no channel on the row)
   are read by any room member who holds the id.
7. Chat attachments carry no channel on their row: whoever holds an
   attachment id (unguessable) and is in the room can fetch it. Binding an
   attachment to its channel is a later change.

**Design review (lenses 5, 1, 11), slice 2:**

- Lens 5 (one rule, one owner): one predicate, `store.MayReadChannel`; one
  asker per service; `TestChannelReadsAskTheRule` names every function that
  reads channel content without asking it, each exemption with its reason
  (content the caller already holds, the author's own act, or aggregates
  without content).
- Lens 1 (who sees what): a non-member gets 404, never 403, on a channel or
  an invite-only section, so neither name leaks; deliverable events reach
  other channels' streams only for readers; search, unread, mentions,
  notifications, plugin hooks, run listings and the handoff export are
  filtered. Tests per path for a non-member and a member
  (`services/room/section_members_test.go`,
  `services/realtime/members_only_test.go`,
  `services/activity/members_only_test.go`,
  `internal/notify/replytoken_test.go`, `internal/roomlog`,
  `services/gateway/agent_door_test.go`).
- Lens 11 (rollout): the migration only adds tables; old pods ignore them.
  During a mixed rollout an old pod still shows a members-only section's
  channels to every member, so **create members-only sections only after
  every room, realtime and activity pod runs this release.** Nothing on the
  open-section path changes.

**Slice 3a, threads** (C1-C4; posting style, tags, statuses, templates,
mark answered and presets are slice 3b):

- A thread is a channel of kind `thread` with its parent, root message and
  activity (migration `2026_10_channel_threads`, only adds; a unique key
  holds one thread per root). Realtime opens it (`POST .../threads`, it
  holds the root; opening again returns it), anyone who may post in the
  parent opens one; a visitor does not. Its readers are its parent's: the
  rule (`MayReadChannel`) reads the parent's section, so a thread of a
  members-only channel is the section's. `store.Listed` is the one rule of
  what a room's channels are: never the people chat, never a thread (rail,
  directory, agents' listing, templates).
- A post in a thread answers its root unless it names another message of
  the thread (`replyTo`, quoted as it read); a reply counts on the root
  (replies, last reply, participants) and the parent's stream is told
  (`thread_updated`). History, unread, typing, search and agent jobs work
  in it as in any channel (tested).
- Followers (C4): the opener and the root's author, then whoever posts or
  is mentioned; a thread's unread is its followers' only; the rail's
  Threads entry lists the followed ones, unread first.
- An agent answering in a thread has a session of its own (the session key
  is per channel) whose records hold the root first, then the thread; the
  parent stays a separate file (§9's check, `roomctx` tests).
- The web: "Reply in thread" on a message, the root's thread line, the side
  panel (the root above the replies, the thread's own feed, stream and
  composer; one side panel at a time with the people chat), renaming,
  following and closing from its header. Closing (C3) posts the
  `summarize-thread` summary to the parent as a reply to the root, then the
  thread is read-only; `draft-thread-title` suggests a title. Both prompts
  are content (`bnbloc-default-content`, packages `draft-thread-title`,
  `summarize-thread`); with no model the controls say the AI is
  unavailable and a thread closes without a summary.
- Tests: room and realtime handler tests (a non-member of a members-only
  parent sees none of its threads), the store on MySQL, `web/e2e/threads.mjs`.
- Rollout (lens 11): an old room pod lists a thread among the channels until
  every pod runs this release (it does not know the kind): open threads only
  after the rollout, as for members-only sections.

**Slice 3b, channel presets** (§2.1c; the switches themselves are their own
change, and the two meet at the settings keys):

- The content kind `bnbloc.channel-preset.package/v1`
  (`contentpkg/channel_preset.go`): `component.json` holds `channel`
  (purpose, rules, settings) and optionally `requires.expert`, beside an
  optional README. The settings the room service knows today are checked by
  its own rules (`wire.IsChannelWhoPosts`, `IsChannelAgentParticipation`,
  slow mode within six hours); the coming switches (`postingStyle`, `tags`,
  `statuses`, `postTemplate`) by shape and bound only; a default agent and
  unknown keys are refused. The limits table lists its bounds. A preset is no
  content use: nothing in it reaches an agent until it is applied.
- Applying is one PATCH of the channel route (`lib/channelPresets.ts`
  `applyPatch`: the preset's keys over the channel's settings, so its default
  agent and keys the preset does not name stay), shown first as a diff of
  what changes, undone by a second PATCH back (the toast's Undo). From a
  channel's menu ("Apply a preset…", "Save as preset", its managers) and from
  New channel ("Start from a preset": made, then applied by the same apply).
  Save as preset captures purpose, rules and settings, never the default
  agent (a person's), into the member's Library through the content save
  route.
- AI: `draft-channel-preset` (contract: purpose, rules, settings) drafts a
  preset from a sentence, shown in the same preview; its prompt and the six
  samples (forum, help desk, announcements, tracker, log, Q&A) are content in
  `bnbloc-default-content`.
- Tests: the validator (`channel_preset_test.go`), the gateway save and list
  (`channel_presets_test.go`), the web's diff, apply, undo and capture
  (`channelPresets.test.ts`), `web/e2e/channel-presets.mjs`.
- Design review: lens 7, one owner (the room service decides a setting; the
  kind checks only what it can without it, and the web's apply and undo are
  one hook); lens 9, external contracts (the kind's manifest is a public
  shape, additive from here on); lens 14, placement (the kind and its
  validator are platform; every preset, including the six samples and the
  assist's prompt, is content).

**Decisions pending owner, slice 3b:**

1. A preset never carries a default agent (a person's agent), and Save as
   preset drops it.
2. Slow mode in a preset is at most six hours; at most 30 tags and 12
   statuses; a post template at most 4000 characters (the room service
   bounds none of them yet).
3. Applying and saving presets are offered to a channel's managers only (the
   ones who may change its setup).
4. A preset's `requires.expert` is validated but not applied yet: a
   channel's expert (C9) is slice 6.
