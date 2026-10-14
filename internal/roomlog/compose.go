package roomlog

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/luckylhb90/bnbloc/core"
)

// Options tunes how Compose lays out the context. Zero values are sane
// defaults: no summary, and an unbounded verbatim window.
type Options struct {
	// WindowChars bounds the verbatim recent-turns window (L2) by combined text
	// length. Entries older than the window are handed to the Summarizer (L1).
	// <= 0 keeps every entry verbatim (no summary).
	WindowChars int
	// Summarizer condenses the entries that fall outside the verbatim window.
	// nil means older entries are dropped rather than summarized.
	Summarizer Summarizer
}

// Now is the message a turn answers: its text, who sent it (the Sender of
// its room entry) and the files attached to it. Unlike the history
// sections, the attachments ARE part of the request and the agent must open
// them; they render as an imperative block before the [now] marker. The
// files that could not be put in the workspace are named too, with why, so
// the agent tells the sender instead of answering as if there were none.
// Edit is the room file the message asks to change (nil = none).
type Now struct {
	From        string
	Text        string
	Attachments []Attachment
	Undelivered []UndeliveredAttachment
	Edit        *EditTarget
}

// EditTarget is the room file a message asks the agent to change
// (docs/prd/2026-10-14-ai-document-editing.md rule 3): Source is where the
// version was placed (under .room/edits/, which bnbloc writes), Path where
// saving the agent's version updates the room's file, or Problem why it
// could not be placed.
type EditTarget struct {
	Path    string
	Source  string
	Name    string
	Version int
	Problem string
}

// EditsDir is where a room file to edit is placed: .room/edits/<artifact
// id>/<file name>.
const EditsDir = core.RoomContextDir + "edits/"

// writeEditLine renders the one line that points the agent at the file to
// change: no tool, no rule, just where it is and that saving it there is
// what updates the room's file. When it could not be placed, the line says
// why, for the sender.
func writeEditLine(b *strings.Builder, e *EditTarget) {
	if e == nil {
		return
	}
	if b.Len() > 0 {
		b.WriteString("\n")
	}
	if e.Problem != "" {
		fmt.Fprintf(b, "[the file to edit, %s v%d, is NOT in this workspace: %s — tell the sender]\n", e.Name, e.Version, e.Problem)
		return
	}
	fmt.Fprintf(b, "[edit %s v%d: it is at %s; save your version as %s, which updates the room's file]\n", e.Name, e.Version, e.Source, e.Path)
}

// writeNow renders the turn's message: its attachments, the [now] marker
// naming the sender, and the text. Every prompt ends with it, even one with
// nothing else: the sender tells a person's own words from a task an agent
// dispatched for them.
func writeNow(b *strings.Builder, now Now) {
	writeEditLine(b, now.Edit)
	writeAttachmentsBlock(b, now.Attachments)
	writeUndeliveredBlock(b, now.Undelivered)
	if b.Len() > 0 {
		b.WriteString("\n")
	}
	b.WriteString(nowMarker(now.From))
	b.WriteString(strings.TrimSpace(now.Text))
}

// Attachment names a file attached to the current message: the workspace path
// the agent opens, plus the original name and type for context.
type Attachment struct {
	Path        string
	Filename    string
	ContentType string
}

// writeAttachmentsBlock renders the "attachments on this message" section: an
// imperative pointer to files the agent must open to answer, kept distinct from
// the "records, consult only if needed" history/`.room/` sections. No-op when
// there are no attachments. Shared by every prompt composer (pull + push).
func writeAttachmentsBlock(b *strings.Builder, atts []Attachment) {
	if len(atts) == 0 {
		return
	}
	if b.Len() > 0 {
		b.WriteString("\n")
	}
	b.WriteString("[attachments on this message — OPEN THESE: part of the request, saved in this workspace, not records]\n")
	for _, a := range atts {
		name := strings.TrimSpace(a.Filename)
		ct := strings.TrimSpace(a.ContentType)
		switch {
		case name != "" && ct != "":
			fmt.Fprintf(b, "- %s (original name %q, %s)\n", a.Path, name, ct)
		case name != "":
			fmt.Fprintf(b, "- %s (original name %q)\n", a.Path, name)
		default:
			fmt.Fprintf(b, "- %s\n", a.Path)
		}
	}
}

// UndeliveredAttachment is a file attached to the current message that is
// not in the workspace: its original name, type, size (0 = unknown) and
// why, in words the agent can repeat to the sender.
type UndeliveredAttachment struct {
	Filename    string
	ContentType string
	Size        int64
	Reason      string
}

// writeUndeliveredBlock renders the attachments the agent cannot open. No-op
// when every attachment arrived.
func writeUndeliveredBlock(b *strings.Builder, atts []UndeliveredAttachment) {
	if len(atts) == 0 {
		return
	}
	if b.Len() > 0 {
		b.WriteString("\n")
	}
	b.WriteString("[attachments on this message that did NOT reach this workspace — tell the sender which and why; never say there are none]\n")
	for _, a := range atts {
		name := strings.TrimSpace(a.Filename)
		if name == "" {
			name = "unnamed file"
		}
		var about []string
		if ct := strings.TrimSpace(a.ContentType); ct != "" {
			about = append(about, ct)
		}
		if a.Size > 0 {
			about = append(about, HumanBytes(a.Size))
		}
		fmt.Fprintf(b, "- %q", name)
		if len(about) > 0 {
			fmt.Fprintf(b, " (%s)", strings.Join(about, ", "))
		}
		if r := strings.TrimSpace(a.Reason); r != "" {
			fmt.Fprintf(b, ": %s", r)
		}
		b.WriteString("\n")
	}
}

// HumanBytes renders a size the way members read it (812 KB, 1.7 MB).
func HumanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return strconv.FormatFloat(float64(n)/(1<<20), 'f', 1, 64) + " MB"
	case n >= 1<<10:
		return strconv.FormatInt((n+512)>>10, 10) + " KB"
	default:
		return strconv.FormatInt(n, 10) + " bytes"
	}
}

// Compose renders the layered room context plus the current message into a
// single prompt for a stateless drive, in the multi-party format verified to be
// understood by the local agent providers:
//
//	# Story so far      (L1, optional — older conversation, condensed)
//	# Room conversation (L2 — recent turns verbatim, Line)
//	[now — from …]      (L3 — the current message)
//
// When there is no history, it returns just the current message (and its
// attachments). This is the single place the injected context
// format is defined; a CRDT-backed Source feeds the same Entry slice here, so
// the format is stable across storage implementations. Who the agent is and
// what the records are is not here: the bridge states it once per session
// (SessionGuidance), in the agent profile or ahead of this first prompt.
func Compose(entries []Entry, now Now, opts Options) string {
	older, window := splitWindow(entries, opts.WindowChars)

	var summary string
	if opts.Summarizer != nil && len(older) > 0 {
		summary = strings.TrimSpace(opts.Summarizer.Summarize(older))
	}

	if len(window) == 0 && summary == "" {
		var b strings.Builder
		writeNow(&b, now)
		return b.String()
	}

	var b strings.Builder
	if summary != "" {
		b.WriteString(RecordsHeader("Story so far"))
		b.WriteString(summary)
		b.WriteString("\n")
	}
	if len(window) > 0 {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
		b.WriteString(RecordsHeader("Room conversation"))
		for _, e := range firstMentions(window) {
			b.WriteString(Line(e))
		}
	}
	writeNow(&b, now)
	return b.String()
}

// splitWindow divides chronological entries into (older, window): the newest
// entries whose combined cost fits windowChars form the verbatim window, and
// everything before them is older (summary input). windowChars <= 0 keeps every
// entry in the window (older is empty). At least the newest entry is always kept
// verbatim, even if it alone exceeds the budget.
func splitWindow(entries []Entry, windowChars int) (older, window []Entry) {
	if windowChars <= 0 || len(entries) == 0 {
		return nil, entries
	}
	total := 0
	start := len(entries)
	for i := len(entries) - 1; i >= 0; i-- {
		cost := entryCost(entries[i])
		if total+cost > windowChars && start != len(entries) {
			break
		}
		total += cost
		start = i
	}
	return entries[:start], entries[start:]
}

// entryCost approximates the rendered size of an entry line.
func entryCost(e Entry) int { return len(e.Text) + len(e.Speaker) + 2 }

// StructuredSummarizer is the default, model-free Summarizer. It condenses the
// older conversation deterministically: one bullet per speaker with their turn
// count and most recent line, newest speakers last. Being deterministic, every
// agent in the room derives the same story-so-far from the same feed, so no
// shared persistence or single-writer coordination is required. A configurable
// LLM-backed Summarizer can replace it later behind the same interface.
type StructuredSummarizer struct {
	// MaxLineBytes bounds each speaker's quoted line (Truncate). 0 uses a
	// default.
	MaxLineBytes int
}

// Summarize implements Summarizer.
func (s StructuredSummarizer) Summarize(entries []Entry) string {
	if len(entries) == 0 {
		return ""
	}
	maxLine := s.MaxLineBytes
	if maxLine <= 0 {
		maxLine = defaultSummaryLineBytes
	}
	type agg struct {
		order    int
		count    int
		lastText string
	}
	bySpeaker := map[string]*agg{}
	order := 0
	for _, e := range entries {
		sp := e.Speaker
		if sp == "" {
			sp = "unknown"
		}
		text := strings.TrimSpace(e.Text)
		if text == "" {
			continue
		}
		a := bySpeaker[sp]
		if a == nil {
			a = &agg{order: order}
			bySpeaker[sp] = a
			order++
		}
		a.count++
		a.lastText = text
	}
	if len(bySpeaker) == 0 {
		return ""
	}
	speakers := make([]string, 0, len(bySpeaker))
	for sp := range bySpeaker {
		speakers = append(speakers, sp)
	}
	sort.Slice(speakers, func(i, j int) bool {
		return bySpeaker[speakers[i]].order < bySpeaker[speakers[j]].order
	})

	var b strings.Builder
	fmt.Fprintf(&b, "Earlier in this room (%d turns condensed):\n", len(entries))
	for _, sp := range speakers {
		a := bySpeaker[sp]
		fmt.Fprintf(&b, "- %s (%s): %s\n", sp, turns(a.count), Truncate(a.lastText, maxLine))
	}
	return strings.TrimRight(b.String(), "\n")
}

func turns(n int) string {
	if n == 1 {
		return "1 turn"
	}
	return fmt.Sprintf("%d turns", n)
}

// defaultSummaryLineBytes bounds a speaker's quoted line in the summary.
const defaultSummaryLineBytes = 400
