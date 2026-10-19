package roomlog

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
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
// pull model (materialized .room/ files) uses Channels,
// ReadmeSum and AppsListed: a workspace's, shared by the agent's channel
// sessions (the bridge keeps it with the workspace); the push model (inline
// prompt context) uses PushSeenSeq: a session's.
type CtxState struct {
	// Channels are each agent-readable channel's materialized history
	// (.room/channels/<dir>/), keyed by channel id: a channel is a context
	// boundary, paged on its own (no channel starves another, no cursor
	// skips another's lines).
	Channels map[string]ChannelRecords `json:",omitempty"`
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
	// RevisedMs is the room's newest edit or deletion the workspace's
	// records follow (the revisions listing's time); Revised are the
	// sequences, by channel, whose chunks the next pass rewrites; Resets
	// are when (bridge clock, unix ms) a deletion was found in each channel:
	// a session started before it is reset at its next turn, since the
	// deleted text may be in its memory.
	RevisedMs int64              `json:",omitempty"`
	Revised   map[string][]int64 `json:",omitempty"`
	Resets    map[string]int64   `json:",omitempty"`
	// Expired is, by channel, the retention horizon the records already
	// follow: data retention purged every message before it, and the
	// chunks wholly before it were rewritten empty (ExpireBefore).
	Expired map[string]int64 `json:",omitempty"`
	// StartedMs is when a session started (bridge clock, unix ms): a
	// session's.
	StartedMs int64 `json:",omitempty"`
}

// NamedFile is one materialized .room/ file.
type NamedFile struct {
	Name    string
	Content string
}

// ChannelRecords is one channel's materialized history.
type ChannelRecords struct {
	Name string
	// Dir is where its files live (ChannelDir).
	Dir string
	// OldestSeq is the oldest materialized sequence; 0 means full history.
	OldestSeq int64
	// EndSeq is the newest materialized sequence.
	EndSeq int64
	// Chunks lists the chunk file names, chronological.
	Chunks []string
}

// channelsDir holds every channel's records.
const channelsDir = ".room/channels/"

// ChannelDir is a channel's records directory, by its id: stable when the
// channel is renamed (INDEX.md shows the current name) and path-safe.
func ChannelDir(id string) string {
	var b strings.Builder
	for _, r := range id {
		if r < 128 && (unicode.IsLetter(r) || unicode.IsDigit(r) || r == '-' || r == '_') {
			b.WriteRune(r)
		}
	}
	if b.Len() == 0 {
		return channelsDir + "default"
	}
	return channelsDir + b.String()
}

// MaterializeChannel builds one channel's files in rec.Dir: new chunks for
// entries beyond rec.EndSeq and a fresh recent.md (its newest messages,
// rebuilt every pass). Every path is the channel's own: an agent's channels
// share one workspace and their passes may run at once.
func MaterializeChannel(entries []Entry, complete bool, rec ChannelRecords) (files []NamedFile, next ChannelRecords) {
	next = rec
	tail := entries
	if len(tail) > recentEntries {
		tail = tail[len(tail)-recentEntries:]
	}
	if len(tail) > 0 {
		var b strings.Builder
		b.WriteString(RecordsHeader("Latest room messages"))
		b.WriteString("\n")
		for _, e := range firstMentions(tail) {
			b.WriteString(Line(e))
		}
		files = append(files, NamedFile{Name: rec.Dir + "/recent.md", Content: b.String()})
	}
	var chunk []Entry
	size := 0
	flush := func() {
		if len(chunk) == 0 {
			return
		}
		first, last := chunk[0].Seq, chunk[len(chunk)-1].Seq
		name := fmt.Sprintf("%s/chunk-%010d-%010d.md", rec.Dir, first, last)
		files = append(files, NamedFile{Name: name, Content: chunkContent(chunk)})
		next.Chunks = append(next.Chunks, name)
		next.EndSeq = last
		chunk, size = nil, 0
	}
	for _, e := range entries {
		if e.Seq <= rec.EndSeq {
			continue
		}
		chunk = append(chunk, e)
		size += len(e.Text) + len(e.Speaker) + 2
		if len(chunk) >= chunkMaxEntries || size >= chunkMaxBytes {
			flush()
		}
	}
	flush()
	if next.OldestSeq == 0 && !complete && len(entries) > 0 && rec.EndSeq == 0 {
		// The fetch hit its row cap: the channel's beginning was not reached.
		next.OldestSeq = entries[0].Seq
	}
	return files, next
}

// chunkContent renders one chunk of history.
func chunkContent(chunk []Entry) string {
	return rangeContent(chunk[0].Seq, chunk[len(chunk)-1].Seq, chunk)
}

func rangeContent(first, last int64, entries []Entry) string {
	var b strings.Builder
	b.WriteString(RecordsHeader(fmt.Sprintf("Room history %d–%d", first, last)))
	b.WriteString("\n")
	for _, e := range firstMentions(entries) {
		b.WriteString(Line(e))
	}
	return b.String()
}

// ChunkRange is the sequence range a chunk file holds, from its name.
func ChunkRange(name string) (first, last int64, ok bool) {
	base := name[strings.LastIndexByte(name, '/')+1:]
	if _, err := fmt.Sscanf(base, "chunk-%d-%d.md", &first, &last); err != nil {
		return 0, 0, false
	}
	return first, last, true
}

// ChunksHolding are the chunks of rec holding any of seqs.
func ChunksHolding(rec ChannelRecords, seqs []int64) []string {
	var out []string
	for _, name := range rec.Chunks {
		first, last, ok := ChunkRange(name)
		if !ok {
			continue
		}
		for _, s := range seqs {
			if s >= first && s <= last {
				out = append(out, name)
				break
			}
		}
	}
	return out
}

// RewriteChunk renders chunk name again from entries (its range as the
// room holds it now: edits applied, deletions gone), under the same name,
// so no file keeps the old text.
func RewriteChunk(name string, entries []Entry) NamedFile {
	first, last, _ := ChunkRange(name)
	var kept []Entry
	for _, e := range entries {
		if e.Seq >= first && e.Seq <= last {
			kept = append(kept, e)
		}
	}
	return NamedFile{Name: name, Content: rangeContent(first, last, kept)}
}

// GuidanceChannel is how the session guidance names a channel's records
// (Guidance.Channel): its directory under .room/.
func GuidanceChannel(id string) string {
	return strings.TrimPrefix(ChannelDir(id), ".room/") + "/"
}

// IndexFile describes the records so the agent can navigate them: the
// room's channels, one line each, whichever session's pass writes it (an
// agent's channels share the workspace; the session guidance names its
// own). Written last: it never names files that do not exist.
func IndexFile(channels []ChannelRecords) NamedFile {
	var b strings.Builder
	b.WriteString(RecordsHeader("Room records index"))
	b.WriteString("\n")
	b.WriteString("Channels (each directory: recent.md, its latest messages, and chronological chunks, chunk-<start>-<end>.md by seq):\n")
	for _, c := range channels {
		b.WriteString("- " + indexLine(c) + "\n")
	}
	b.WriteString("Room deliverables: artifacts.md. The Library: library.md. Live deliverables: live.md.\n")
	b.WriteString("Interactive apps and how much data each holds: apps.md, once the room has one.\n")
	b.WriteString("How this room works (every agent has its own workspace): README.md.\n")
	return NamedFile{Name: ".room/INDEX.md", Content: b.String()}
}

func indexLine(rec ChannelRecords) string {
	name := strings.TrimSpace(rec.Name)
	if name == "" {
		name = "the room's channel"
	} else {
		name = "#" + name
	}
	if rec.EndSeq == 0 {
		return name + " (no messages yet)"
	}
	coverage := "from the beginning"
	if rec.OldestSeq > 0 {
		coverage = "from seq " + strconv.FormatInt(rec.OldestSeq, 10)
	}
	return fmt.Sprintf("%s — %s/ (%s through seq %d, %d chunks)", name, strings.TrimPrefix(rec.Dir, ".room/"), coverage, rec.EndSeq, len(rec.Chunks))
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
	return ComposeTurn(now, recordsHint, nil)
}

// ComposeTurn is the pull-model prompt of a reused session's turn: the
// pointer prompt, with what others said in the session's channel since its
// last turn (delta) as conversation lines before the message — bounded
// (writeDelta), the rest in the channel's recent.md.
func ComposeTurn(now Now, recordsHint bool, delta []Entry) string {
	var b strings.Builder
	if recordsHint {
		b.WriteString("(room records: .room/INDEX.md)\n")
	}
	writeDelta(&b, delta, 0)
	writeNow(&b, now)
	return b.String()
}

// ComposeHandover is a new session's first prompt in a channel that already
// has a conversation (pull model): the session remembers none of it (a
// session replaced for new content, a deleted message, an outgrown context
// or one that could not be continued), so the prompt hands it over before
// the current message: the older part condensed by opts.Summarizer, the
// newest exchanges verbatim within handoverChars, the agent's own replies
// included, and where the whole of it is (records: the channel's records
// directory under .room/, GuidanceChannel's). entries are the channel's
// messages before the current one, oldest first.
func ComposeHandover(entries []Entry, records string, now Now, recordsHint bool, opts Options) string {
	var b strings.Builder
	if recordsHint {
		b.WriteString("(room records: .room/INDEX.md)\n")
	}
	if len(entries) > 0 {
		older, window := splitWindow(entries, handoverChars)
		b.WriteString(RecordsHeader("Earlier in this conversation"))
		b.WriteString("This is a new session of the conversation: you do not remember it. What was said is below; all of it is in .room/" + records + "recent.md and the chunks before it. Read them when this message builds on more than this.\n")
		if opts.Summarizer != nil && len(older) > 0 {
			if summary := strings.TrimSpace(opts.Summarizer.Summarize(older)); summary != "" {
				b.WriteString("Before the lines below:\n" + summary + "\n")
			}
		}
		for _, e := range firstMentions(window) {
			b.WriteString(Line(e))
		}
		b.WriteString("\n")
	}
	writeNow(&b, now)
	return b.String()
}

// handoverChars bounds the verbatim part of a handover (ComposeHandover).
const handoverChars = 6000

// ComposeDelta renders a follow-up prompt under the push model: only the
// messages that arrived since the session's last turn, then the current
// message. The session's first-turn prompt and the CLI's own memory hold the
// older context, so repeating a summary or window here would re-inject what
// the agent already knows. An empty delta yields the current message alone
// (its sender and attachments): a follow-up with nothing new needs no
// framing at all.
func ComposeDelta(entries []Entry, now Now, opts Options) string {
	var b strings.Builder
	writeDelta(&b, entries, opts.WindowChars)
	writeNow(&b, now)
	return b.String()
}

// Turn-input delta bounds: the most lines, characters and characters of
// one line the messages since the session's last turn bring into a turn's
// input; the rest stay in the channel's recent.md.
const (
	maxDeltaLines     = 20
	maxDeltaChars     = 4000
	maxDeltaLineBytes = 500
)

// writeDelta renders the messages since the session's last turn under the
// records header: the newest within maxDeltaLines and windowChars (0 or
// more than maxDeltaChars = maxDeltaChars), each line cut to
// maxDeltaLineBytes; nothing when there are none.
func writeDelta(b *strings.Builder, entries []Entry, windowChars int) {
	if len(entries) == 0 {
		return
	}
	if windowChars <= 0 || windowChars > maxDeltaChars {
		windowChars = maxDeltaChars
	}
	cut := make([]Entry, len(entries))
	for i, e := range entries {
		e.Text = Truncate(strings.TrimSpace(e.Text), maxDeltaLineBytes)
		cut[i] = e
	}
	entries = cut
	kept := entries
	omitted := 0
	if len(kept) > maxDeltaLines {
		omitted = len(kept) - maxDeltaLines
		kept = kept[omitted:]
		entries = kept
	}
	if windowChars > 0 {
		cost := 0
		cut := 0
		for i := len(entries) - 1; i >= 0; i-- {
			cost += entryCost(entries[i])
			if cost > windowChars {
				cut = i + 1
				break
			}
		}
		if cut > 0 {
			kept = entries[cut:]
			dropped := cut
			if len(kept) == 0 { // never drop the newest line entirely
				kept = entries[len(entries)-1:]
				dropped = len(entries) - 1
			}
			omitted += dropped
		}
	}
	b.WriteString(RecordsHeader("New room messages since your last turn"))
	if omitted > 0 {
		fmt.Fprintf(b, "(%d older new messages omitted)\n", omitted)
	}
	for _, e := range firstMentions(kept) {
		b.WriteString(Line(e))
	}
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

// ExpireBefore marks for rewriting (Revised) every chunk of each channel
// lying wholly before its retention horizon (retainedFrom: the oldest
// stored sequence; a channel the room no longer holds any message of is
// wholly purged), once per horizon (Expired): the records drop what data
// retention purged.
func ExpireBefore(st *CtxState, retainedFrom map[string]int64) {
	for ch, rec := range st.Channels {
		horizon, ok := retainedFrom[ch]
		if !ok {
			horizon = rec.EndSeq + 1
		}
		if horizon <= st.Expired[ch] {
			continue
		}
		var marks []int64
		for _, name := range rec.Chunks {
			if first, last, ok := ChunkRange(name); ok && last < horizon {
				marks = append(marks, first)
			}
		}
		if st.Revised == nil {
			st.Revised = map[string][]int64{}
		}
		if st.Expired == nil {
			st.Expired = map[string]int64{}
		}
		st.Revised[ch] = append(marks, st.Revised[ch]...)
		st.Expired[ch] = horizon
	}
}
