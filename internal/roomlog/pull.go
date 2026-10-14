package roomlog

import (
	"fmt"
	"strconv"
	"strings"
)

// Pull-based room context.
//
// The push model injected a summary plus a verbatim window into every drive
// prompt, which (a) repeated what the CLI's own session memory already held,
// (b) diluted the actual instruction, and (c) only ever covered the default
// channel. The pull model materializes the room's shared history once per
// session into .room/ files inside the agent's workspace — chunked, indexed,
// searchable with the agent's own tools — and the prompt carries only a
// pointer. History becomes something the agent consults on demand, not
// something that competes with the instruction for attention.

const (
	// recentEntries is how many newest messages recent.md carries.
	recentEntries = 40
	// chunkMaxEntries / chunkMaxBytes pack history into grep-friendly chunks.
	chunkMaxEntries = 200
	chunkMaxBytes   = 12 * 1024
)

// CtxState tracks shared-context coverage for BOTH delivery models. The
// pull model (materialized .room/ files) uses OldestSeq/EndSeq/Chunks,
// ReadmeSum and AppsListed: a workspace's, shared by the agent's channel
// sessions (the bridge keeps it with the workspace); the push model (inline
// prompt context) uses PushSeenSeq: a session's.
type CtxState struct {
	// OldestSeq is the oldest materialized sequence; 0 means full history.
	OldestSeq int64
	// EndSeq is the newest materialized sequence.
	EndSeq int64
	// Chunks lists the chunk file names, chronological.
	Chunks []string
	// PushSeenSeq is the newest room-message seq the push model has delivered
	// into this session's prompt (the current instruction counts: it is
	// delivered as the [now] section). 0 means nothing pushed yet; the next
	// push turn delivers a full window before switching to deltas.
	PushSeenSeq int64
	// ReadmeSum is the sum of the .room/README.md last written into the
	// workspace (ReadmeUpdate); empty until the first pull pass.
	ReadmeSum string
	// AppsListed: the workspace holds a .room/apps.md (the room
	// had an app), so every pass rewrites it (AppsUpdate).
	AppsListed bool
	// AppSDKSum is the sum of the demo-mode app SDK last written into the
	// workspace (AppSDKUpdate); empty until the first pull pass.
	AppSDKSum string
	// Attachments are the room's attachments the workspace holds a copy of
	// (id → path; Mirrored).
	Attachments map[string]string `json:",omitempty"`
}

// NamedFile is one materialized .room/ file.
type NamedFile struct {
	Name    string
	Content string
}

// Materialize builds the .room/ file set: new chunks for entries beyond
// st.EndSeq, a fresh recent.md, and an INDEX.md describing the whole set
// (written last by convention — the caller orders writes chunks → recent →
// index so a partial materialization never advertises files that do not exist).
func Materialize(entries []Entry, complete bool, st CtxState) (files []NamedFile, newState CtxState) {
	newState = st
	// The newest N entries always rebuild recent.md, even when nothing new
	// arrived, so the file reflects the current fetch.
	var recent []Entry
	if len(entries) > recentEntries {
		recent = entries[len(entries)-recentEntries:]
	} else {
		recent = entries
	}
	if len(recent) > 0 {
		var b strings.Builder
		b.WriteString(RecordsHeader("Latest room messages"))
		b.WriteString("\n")
		for _, e := range recent {
			b.WriteString(Line(e))
		}
		files = append(files, NamedFile{Name: ".room/recent.md", Content: b.String()})
	}

	var fresh []Entry
	for _, e := range entries {
		if e.Seq > st.EndSeq {
			fresh = append(fresh, e)
		}
	}
	if len(fresh) == 0 {
		files = append(files, indexFile(newState))
		return files, newState
	}
	// Pack new entries into chunks.
	var chunk []Entry
	size := 0
	flush := func() {
		if len(chunk) == 0 {
			return
		}
		name := fmt.Sprintf(".room/chunk-%010d-%010d.md", chunk[0].Seq, chunk[len(chunk)-1].Seq)
		var b strings.Builder
		b.WriteString(RecordsHeader(fmt.Sprintf("Room history %d–%d", chunk[0].Seq, chunk[len(chunk)-1].Seq)))
		b.WriteString("\n")
		for _, e := range chunk {
			b.WriteString(Line(e))
		}
		files = append(files, NamedFile{Name: name, Content: b.String()})
		newState.Chunks = append(newState.Chunks, name)
		newState.EndSeq = chunk[len(chunk)-1].Seq
		chunk = nil
		size = 0
	}
	for _, e := range fresh {
		chunk = append(chunk, e)
		size += len(e.Text) + len(e.Speaker) + 2
		if len(chunk) >= chunkMaxEntries || size >= chunkMaxBytes {
			flush()
		}
	}
	flush()
	if newState.OldestSeq == 0 && !complete && len(entries) > 0 {
		// The fetch hit its row cap: the room's beginning was not reached.
		newState.OldestSeq = entries[0].Seq
	}
	files = append(files, indexFile(newState))
	return files, newState
}

// indexFile describes the materialized set so the agent can navigate it.
func indexFile(st CtxState) NamedFile {
	var b strings.Builder
	b.WriteString(RecordsHeader("Shared room history index"))
	b.WriteString("\n")
	coverage := "from the beginning"
	if st.OldestSeq > 0 {
		coverage = "from seq " + strconv.FormatInt(st.OldestSeq, 10)
	}
	fmt.Fprintf(&b, "Coverage: %s through seq %d, %d chunks.\n", coverage, st.EndSeq, len(st.Chunks))
	b.WriteString("Chunks are chronological; filenames carry their seq ranges (chunk-<start>-<end>.md).\n")
	b.WriteString("The latest messages are in recent.md. Room deliverables: artifacts.md. The Library: library.md. Live deliverables: live.md.\n")
	b.WriteString("Interactive apps and how much data each holds: apps.md, once the room has one.\n")
	b.WriteString("How this room works (every agent has its own workspace): README.md.\n")
	return NamedFile{Name: ".room/INDEX.md", Content: b.String()}
}

// ComposePointer is the pull-model prompt of every turn: the current
// message (its attachments, the [now] marker naming its sender, its text)
// and nothing else. The instruction is never diluted by history, which the
// agent consults in .room/ on demand, nor by static guidance: who the agent
// is, what the records are and what .room/ holds are stated once per
// session (SessionGuidance). recordsHint adds one line pointing at the
// records, for a session whose guidance rode its first prompt (a core
// without agentProfile), which a harness may compact away.
// testdata/golden/pointer*.txt pin it and TestPromptBudgets bounds it: it
// rides every turn.
func ComposePointer(now Now, recordsHint bool) string {
	var b strings.Builder
	if recordsHint {
		b.WriteString("(room records: .room/INDEX.md)\n")
	}
	writeNow(&b, now)
	return b.String()
}

// ComposeDelta renders a follow-up prompt under the push model: only the
// messages that arrived since the session's last turn, then the current
// message. The session's first-turn prompt and the CLI's own memory hold the
// older context, so repeating a summary or window here would re-inject what
// the agent already knows. An empty delta yields the current message alone
// (its sender and attachments): a follow-up with nothing new needs no
// framing at all.
func ComposeDelta(entries []Entry, now Now, opts Options) string {
	if len(entries) == 0 {
		var b strings.Builder
		writeNow(&b, now)
		return b.String()
	}
	// Bound the verbatim delta by the window budget, newest kept first.
	kept := entries
	omitted := 0
	if opts.WindowChars > 0 {
		cost := 0
		cut := 0
		for i := len(entries) - 1; i >= 0; i-- {
			cost += entryCost(entries[i])
			if cost > opts.WindowChars {
				cut = i + 1
				break
			}
		}
		if cut > 0 {
			kept = entries[cut:]
			omitted = cut
			if len(kept) == 0 { // never drop the newest line entirely
				kept = entries[len(entries)-1:]
				omitted = len(entries) - 1
			}
		}
	}
	var b strings.Builder
	b.WriteString(RecordsHeader("New room messages since your last turn"))
	if omitted > 0 {
		fmt.Fprintf(&b, "(%d older new messages omitted)\n", omitted)
	}
	for _, e := range kept {
		b.WriteString(Line(e))
	}
	writeNow(&b, now)
	return b.String()
}

// AgentSpeaker renders an agent's speaker label as "owner/provider", or just
// the provider when the owner is unknown.
func AgentSpeaker(owner, provider string) string {
	owner = strings.TrimSpace(owner)
	provider = strings.TrimSpace(provider)
	if owner == "" {
		return provider
	}
	return owner + "/" + provider
}
