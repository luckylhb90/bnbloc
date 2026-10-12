package roomlog

import (
	"fmt"
	"sort"
	"strings"
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
// them; they render as an imperative block before the [now] marker.
type Now struct {
	From        string
	Text        string
	Attachments []Attachment
}

// writeNow renders the turn's message: its attachments, the [now] marker
// naming the sender, and the text. Every prompt ends with it, even one with
// nothing else: the sender tells a person's own words from a task an agent
// dispatched for them.
func writeNow(b *strings.Builder, now Now) {
	writeAttachmentsBlock(b, now.Attachments)
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
	fmt.Fprintf(b, "[attachments on this message — OPEN THESE]\nThe current message includes %d attachment(s), saved in this workspace. They are part of the request: open/read each one to answer.\n", len(atts))
	for _, a := range atts {
		name := strings.TrimSpace(a.Filename)
		ct := strings.TrimSpace(a.ContentType)
		switch {
		case name != "" && ct != "":
			fmt.Fprintf(b, "- %s  (original name: %q, %s)\n", a.Path, name, ct)
		case name != "":
			fmt.Fprintf(b, "- %s  (original name: %q)\n", a.Path, name)
		default:
			fmt.Fprintf(b, "- %s\n", a.Path)
		}
	}
	b.WriteString("These are NOT the background record files; they are the files this message is about.\n")
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
		for _, e := range window {
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
	// MaxLineChars truncates each speaker's quoted line. 0 uses a default.
	MaxLineChars int
}

// Summarize implements Summarizer.
func (s StructuredSummarizer) Summarize(entries []Entry) string {
	if len(entries) == 0 {
		return ""
	}
	maxLine := s.MaxLineChars
	if maxLine <= 0 {
		maxLine = 160
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
		fmt.Fprintf(&b, "- %s (%s): %s\n", sp, turns(a.count), truncate(a.lastText, maxLine))
	}
	return strings.TrimRight(b.String(), "\n")
}

func turns(n int) string {
	if n == 1 {
		return "1 turn"
	}
	return fmt.Sprintf("%d turns", n)
}

// truncate shortens s to at most n runes, appending an ellipsis when cut.
func truncate(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	if n <= 1 {
		return string(r[:n])
	}
	return string(r[:n-1]) + "…"
}
