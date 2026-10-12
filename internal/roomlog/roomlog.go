// Package roomlog is the seam that supplies an agent with the room's shared
// conversation as context. A room is a multi-party space (humans + multiple
// agents); an agent driven to reply must see the ordered, speaker-attributed
// history of what everyone said, not just the single message addressed to it.
//
// History is delivered to the agent by one of two models, both defined here:
//
//	pull — materialized into .room/ files inside the agent's workspace
//	       (Materialize + ComposePointer); the agent consults on demand.
//	push — delivered inline in the prompt (Compose for a session's first
//	       turn, ComposeDelta for follow-ups: only what arrived since the
//	       session's last turn).
//
// Both models share the same semantics: every channel an agent may read
// (never the people chat: History), never the agent's own past replies (its
// session memory already holds them), never the current instruction twice
// (it is the [now] section), and every history section is labeled RECORDS,
// NOT REQUESTS so background never competes with the instruction. The underlying log is a single-writer, monotonic-seq feed
// (History); if it ever becomes a decentralized log, a CRDT-backed fetch
// can replace it without touching the composition above.
package roomlog

// Entry is one room-log line, attributed to its speaker in chronological order.
// Kind distinguishes a human from an agent so the assembled context can label
// each line and the driven agent can tell people, peer agents, and itself apart.
type Entry struct {
	Seq     int64
	Speaker string // "jack" (human) or "jack/codex" (agent: owner/provider)
	Kind    Kind
	Text    string
	// AtMs is when the row was recorded (0 when unknown).
	AtMs int64
	// To is the agent a person's message addressed (AgentSpeaker form; ""
	// = people only); Via the agent that dispatched it on their behalf.
	To  string
	Via string
	// Owner, Provider, Session and Role describe an agent entry: its agent,
	// the activity session it was reported under, and its message role.
	Owner    string
	Provider string
	Session  string
	Role     string
}

// Kind is the speaker category of an Entry.
type Kind string

const (
	KindHuman Kind = "human"
	KindAgent Kind = "agent"
)

// Summarizer condenses the older part of a conversation into a compact "story
// so far" that preserves who-said-what without the full transcript. The default
// implementation is deterministic and model-free (StructuredSummarizer); a
// configurable LLM-backed summarizer can satisfy the same interface later
// without changing Compose.
type Summarizer interface {
	// Summarize renders older entries (chronological) into a condensed block.
	// An empty result means "nothing worth prepending".
	Summarize(entries []Entry) string
}
