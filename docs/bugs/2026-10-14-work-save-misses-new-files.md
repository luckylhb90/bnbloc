# Saving a deck as a gallery work took three tries (2026-10-14)

Status: fixed (bridge, room tools, gateway, smoke; the desktop core's half
is in its own repository).

## What happened

A member asked an agent to publish the two slide decks it had made to the
organization's gallery. It took three messages and still went wrong:

1. "Not registered." The agent replied that the decks were not deliverables
   of the room, so it could not save them as works, and asked the member to
   say "publish" again next turn.
2. On the next turn it saved them, but each work's "how it was made" was
   the member's "publish the two decks too", not anything that made them.
3. Asked to fix that, the agent said its room tools had disconnected, and
   they stayed gone after a retry.

## Causes

1. **Files under `out/` never published.** The publisher skipped every file
   under a `dist/`, `build/` or `out/` directory as web build output. The
   agent had built the decks in `work/out/`: they were silently never
   deliverables. It then copied them to `work/gallery/`, which published
   during that turn, but the room records (`.room/artifacts.md`) are
   written at turn start, so the agent still saw no ids and gave up.
2. **The save took a deliverable id only.** `save_component` needed an
   `artifactId`, which an agent learns from the room records; a file
   written in the current turn has none there yet.
3. **The recipe's request is the producing turn's message.** The decks'
   versions were published by the copy turn, whose message was "publish the
   two decks too": that became the work's "how it was made". The agent had
   no way to write a better one.
4. **A core restart stripped sessions of their room tools.** A desktop core
   saves its sessions across restarts but, by design, never the profile's
   MCP servers or env (they hold secrets). After the core updated at 14:28,
   the session came back without them; the bridge, finding the session
   alive, kept using it, and every later turn ran without room tools. No
   release check looked at room tools across an update.

## Fixes

1. Under `dist/`, `build/` and `out/` only build intermediates (scripts,
   styles, maps, fonts, manifests, HTML) are skipped; anything else an agent
   writes there (a deck, a report, a chart, an archive) is a deliverable. A
   built static web app still publishes as one site snapshot.
2. `save_component` takes `file`: a workspace file is published at once if
   it is not yet, and saved through its deliverable. A file written this
   turn needs no room record.
3. `save_component` takes a work's `description` and `request`, the
   recipe's "how it was made" (`WorkPublishRequest.RecipePrompt` replaces
   only the request text; the expert, packs and references stay as
   captured).
4. The core now brings such a session back as unknown to the bridge, which
   creates a new session in the same workspace with a fresh profile; the
   new session continues the old one's agent conversation, so nothing it
   knew is lost and its tools are back.
5. `bnbloc smoke --scenario long-task` asks the agent to use a room tool
   before and after `--between`. An agent that had tools before and has
   none after fails the run ("room tools").

## Lessons

- A silent skip rule turned into a missing feature: the publisher should
  skip only what it can name as noise. When it does skip, the agent sees
  nothing, so the platform has to own the path from "file" to "deliverable"
  instead of leaving it to the agent's reading of the records.
- "Restored" has to mean "restored whole". A session that comes back
  missing part of what it was created with is worse than a session that
  does not come back.
