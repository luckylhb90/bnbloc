# Handover to an agent that joins a conversation (2026-10-30)

Status: built (bridge `drivePrompt`, `roomlog.ComposeHandover`, `roomlog.StructuredSummarizer`).
Follows [`2026-10-22-session-continuation.md`](2026-10-22-session-continuation.md), which handed the
conversation to an agent whose session was lost.

## 1. What happened

A member changed the room's agent to one that had not spoken in the channel. It answered the message
alone: the previous agent's work and what had been decided were in `.room/`, and the prompt said only
"room records: .room/INDEX.md". The member had to say it again. The rule was stated in the code: *an
agent new to the channel gets the pointer alone, and reads .room/ when it needs to*.

## 2. The rule now

A new session in a channel that already has a conversation is handed it, whoever it is: an agent that
took part before (its session was replaced) or one that never did (chosen mid-conversation, the room's
agent changed). The channel has a conversation once anyone spoke in it besides the message that started
this turn. An empty channel gets the pointer alone, as before. One function decides (`drivePrompt`); one
function composes it (`ComposeHandover`).

## 3. Several people and several agents in one channel

The handover is the channel's lines, each headed by who said it (a person by name, an agent as
`owner/provider`), so the new agent can tell A from B from the other agents. The newest ~6000 characters
are verbatim; the older part is condensed, deterministically (every agent derives the same story from the
same record, no one writes a shared summary):

- **What people said is kept**, message by message, bounded (600 bytes each, the newest 12): an earlier
  instruction ("QA only, never prod") binds the work whoever speaks after, and the old condensing kept only
  each speaker's last line, so "ok" from the same person erased it. A short acknowledgement and a repeat
  of the same words carry nothing and are left out; earlier messages beyond the 12 are counted and found in
  the records.
- **What agents said is one bullet per speaker**: a turn count and their latest line. Their words are work
  product, in full in the records the handover points to.

## 4. Design review

1. **Actors.** People and agents of one channel; the new agent is the reader.
2. **Lifecycle.** Once per new session; a reused session keeps hearing only what others said since its last
   turn (unchanged).
3. **Scale.** Bounded by the window (6000 characters), 12 kept people messages at 600 bytes, and one bullet
   per agent speaker: under ~10 KB.
4. **Failure.** The records could not be materialised: unchanged, the pushed context.
5. **Boundaries and privacy.** The channel the turn was driven in only; another channel's history stays in
   the records. A handover reads what the agent could already read in `.room/`.
6. **One rule, one owner.** `drivePrompt` decides who is handed it; `StructuredSummarizer` decides what the
   older part keeps.
7. **Load on agents.** A new agent's first prompt is longer, only in a channel that has a conversation; no
   new tool or rule.

## 5. Acceptance

`TestAnAgentNewToAChannelWithAConversationIsHandedIt` (bridge): an agent that never spoke in a channel with
two people and another agent gets the person's early constraint, the other person's instruction and the
pointer to the whole record, with the current message last, and a channel with only the current message
still gets the pointer alone. `TestHandoverKeepsWhatPeopleSaidBeyondTheWindow` (roomlog): the same
channel with chatter that pushes both lines out of the window. Both fail with the previous rule (checked).
