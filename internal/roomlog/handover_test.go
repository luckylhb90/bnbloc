package roomlog

import (
	"strings"
	"testing"
)

// A new session of a conversation is handed it over: the older part
// condensed, the newest exchanges verbatim (the agent's own replies
// included) within handoverChars, where the whole of it is, then the
// current message last.
func TestComposeHandover(t *testing.T) {
	var entries []Entry
	for i := 1; i <= 40; i++ {
		entries = append(entries, Entry{Seq: int64(i), Speaker: "jack", Kind: KindHuman, Text: strings.Repeat("old talk ", 40)})
	}
	entries = append(entries,
		Entry{Seq: 41, Speaker: "jack", Kind: KindHuman, Text: "find my Workday courses"},
		Entry{Seq: 42, Speaker: "jack/claude-code", Kind: KindAgent, Text: "Sign in to Workday in the live view, then tell me"},
	)
	out := ComposeHandover(entries, "channels/general/", Now{From: "jack", Text: "try again"}, false, Options{Summarizer: StructuredSummarizer{}})
	for _, want := range []string{
		RecordsHeader("Earlier in this conversation"),
		"you do not remember it",
		".room/channels/general/recent.md",
		"Before the lines below:",
		"find my Workday courses",
		"Sign in to Workday in the live view",
	} {
		if !strings.Contains(out, want) {
			t.Fatalf("handover lacks %q:\n%s", want, out)
		}
	}
	if !strings.HasSuffix(out, "try again") || strings.Index(out, "Sign in to Workday") > strings.Index(out, "try again") {
		t.Fatalf("the current message is not last:\n%s", out)
	}
	if len(out) > handoverChars+3000 {
		t.Fatalf("handover is %d bytes, past its bound", len(out))
	}
	// Nothing before the message: the message alone.
	if got := ComposeHandover(nil, "channels/general/", Now{Text: "hi"}, false, Options{}); strings.Contains(got, "Earlier") {
		t.Fatalf("empty handover = %q", got)
	}
}
