# Recovering from a conversation that outgrew its model (2026-10-14)

Status: accepted (product owner, 2026-10-14). Extends
`docs/prd/2026-10-13-live-turns.md`.

## 1. Why

An agent on a desktop core lost its conversation when its core restarted.
In the fresh session it re-read every slide of an 85-page template as an
image to "refresh its memory", the request grew to 1.2 million tokens, and
the model refused it ("the input is longer than the model's context
length"). The room showed the raw provider error, and the session that
overflowed would have been resumed — and refused again — by every
following message. The same member's message also went out without the
model they had chosen, so it ran on a smaller default model.

## 2. Rules

- **One classification.** A turn failed for a context-window overflow
  when its core says so — Core Protocol fail code `context_overflow` — or
  when its failure reads as one (one bridge function; "context window",
  "context length", "ContextWindowExceeded", "prompt is too long",
  "maximum context", "too many tokens"). A core that cannot classify
  reports a normal failure.
- **The core's rule.** A core reporting `context_overflow` starts a fresh
  agent conversation at the session's next turn (the session id stays).
- **The bridge recovers once, by itself.** A drive whose turn overflowed
  drops its session and runs the same request once more in a fresh
  session of the same workspace: the agent gets its session guidance
  again, and the room's history stays in `.room/`. The room gets one calm
  notice ("The conversation grew past <provider>'s context window;
  continuing in a fresh session (the room's history stays in .room/)").
  A second overflow fails the job with a plain sentence; the provider's
  raw text stays in the notice's detail.
- **The guidance prevents it.** The session guidance gains two lines:
  keep working files in the workspace, never only in a temporary
  directory (lost when the sandbox restarts); look at many images or large
  files through a script (a contact sheet, extracted text or colors), not
  by reading each one into the conversation.
- **A chosen model is kept.** The composer never drops the member's saved
  model while the agent's model list is unknown, loading or empty (a core
  restarting); only a loaded, non-empty list without it drops it. A
  message always carries the saved model.

## 3. Tests

- Protocol: `context_overflow` round-trips.
- Bridge: the overflow classifier (code and text, and what it must not
  match); an overflowing turn is retried once in a fresh session with one
  notice; a second overflow fails with a plain notice and the raw detail;
  a normal failure is not retried.
- Guidance: goldens and the token budget.
- Web: a saved model survives an empty or loading catalog and is dropped
  only by a loaded catalog without it; the outgoing message carries it.
