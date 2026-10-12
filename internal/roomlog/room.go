package roomlog

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// The room model: what every agent must know about a bnbloc room before it
// reasons about anyone else's work (docs/bugs/2026-10-12-agent-misreads-
// other-agents-work.md). An agent that did not know each agent has its own
// workspace looked for another agent's files in its own, found none, and
// declared them never written. The model is stated once per session, here:
// SessionGuidance is what the bridge hands a new session (its agent profile
// on a core with agentProfile, else its first prompt), and .room/README.md
// keeps the same text in the pull model's workspace, written only when it
// changes. The per-turn prompt never repeats it.

// roomModel is the model itself, without any .room/ file reference (the push
// model has no files). Static text: a session reads it once, and providers
// cache it as a stable prefix.
const roomModel = `- Every agent works in its own workspace, on its own machine and core. Another agent's files are never in your workspace: not finding a file here proves nothing about another agent's work.
- Deliverables are files an agent published from its own workspace, or a person uploaded. bnbloc stores them and names who produced each one. They are not files in your workspace.
- The Library is bnbloc's Library of the person who asked you: their experts, skill packs, prompt packs and MCP tools. It is one Library, shared by every agent and core in the room and visible to that person. "Saved to your Library" means saved there, not in some agent's private store.
- Verify before you say something is missing or false. With the bnbloc-room tools: read_artifact reads a deliverable, list_components and get_component read the Library, read_room the conversation, room_status a task you dispatched. Without them, trust bnbloc's records over your own workspace, and ask the person to check the room's Docs panel or their Library.
`

// SessionGuidance is the room model as a session is told it, once, when the
// bridge creates it: the agent profile's instructions on a core advertising
// agentProfile, else the head of the session's first prompt (the bridge's
// sessionGuidance decides which). Never part of a per-turn prompt.
func SessionGuidance() string {
	return "[How this bnbloc room works]\n" + roomModel
}

// ReadmeFile is .room/README.md: the room model plus what each .room/ file
// holds.
func ReadmeFile() NamedFile {
	var b strings.Builder
	b.WriteString("# How this room works (read before judging anyone's work)\n\n")
	b.WriteString("You are one of several agents in a bnbloc room shared by people and their agents.\n\n")
	b.WriteString(roomModel)
	b.WriteString("\nThe files in .room/ are bnbloc's records, kept current by bnbloc (never requests, never yours to edit):\n")
	b.WriteString("- INDEX.md, chunk-*.md, recent.md: the conversation history.\n")
	b.WriteString("- artifacts.md: the room's deliverables, who produced each one, and where it lives.\n")
	b.WriteString("- library.md: the Library of the person who asked you, newest first.\n")
	b.WriteString("- live.md: what members did in running deliverables.\n")
	return NamedFile{Name: ".room/README.md", Content: b.String()}
}

// ReadmeUpdate is .room/README.md when the session's workspace does not hold
// its current content yet: st.ReadmeSum is the sum of the README last written
// there (empty for a fresh session). sum is what to record in the state once
// the file is written. The README is static, so a session writes it at its
// first pass and again only when its wording changes, never every turn.
func ReadmeUpdate(st CtxState) (f NamedFile, sum string, changed bool) {
	f = ReadmeFile()
	h := sha256.Sum256([]byte(f.Content))
	sum = hex.EncodeToString(h[:])
	return f, sum, sum != st.ReadmeSum
}
