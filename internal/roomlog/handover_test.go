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

// Several people and several agents speak in one channel, and a person said something early that
// still binds ("deploy to QA only, never prod"). The agent that is handed the conversation must see
// it, however much the others said since: the older part keeps what people said and what was said to
// this agent, one line each; what agents said to one another is condensed to a line per speaker.
// (Condensing every speaker to their last line lost it as soon as that person spoke again.)
func TestHandoverKeepsWhatPeopleSaidBeyondTheWindow(t *testing.T) {
	var entries []Entry
	add := func(speaker string, kind Kind, text, to string) {
		entries = append(entries, Entry{Seq: int64(len(entries) + 1), Speaker: speaker, Kind: kind, Text: text, To: to})
	}
	add("maya", KindHuman, "Constraint: deploy to QA only, never to prod.", "")
	add("maya/hermes", KindAgent, "Understood, QA only.", "")
	add("jack", KindHuman, "Use branch main for the release.", "")
	for i := 0; i < 30; i++ { // chatter that pushes both out of the verbatim window
		add("jack/qwen", KindAgent, strings.Repeat("working on the build ", 30), "")
		add("maya", KindHuman, "ok, thanks", "")
		add("maya/kilo", KindAgent, strings.Repeat("checked the services list ", 30), "")
	}
	add("maya", KindHuman, "@dsh please run the checks", "jack/dsh")
	out := ComposeHandover(entries, "channels/general/", Now{From: "maya", Text: "deploy everything once"}, false, Options{Summarizer: StructuredSummarizer{}})
	for _, want := range []string{"deploy to QA only, never to prod", "Use branch main for the release"} {
		if !strings.Contains(out, want) {
			t.Errorf("the handover lost what a person said earlier (%q):\n%s", want, out)
		}
	}
	// The older part holds one bullet for each chatty agent (a turn count and a last line), and what
	// they said in full is only in the verbatim window after it, which is bounded.
	for _, want := range []string{"- jack/qwen (26 turns):", "- maya/kilo (26 turns):"} {
		if !strings.Contains(out, want) {
			t.Errorf("the older part lacks the bullet %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "(30 turns)") {
		t.Errorf("the verbatim window was condensed as well:\n%s", out)
	}
	if len(out) > handoverChars+4000 {
		t.Errorf("handover is %d bytes, past its bound", len(out))
	}
	// Deterministic: every agent derives the same story from the same record.
	if again := ComposeHandover(entries, "channels/general/", Now{From: "maya", Text: "deploy everything once"}, false, Options{Summarizer: StructuredSummarizer{}}); again != out {
		t.Error("the handover differs between two runs on the same record")
	}
	// A person's long early message is kept, bounded, not dropped.
	long := append([]Entry{{Seq: 1, Speaker: "maya", Kind: KindHuman, Text: "Rule: " + strings.Repeat("never touch prod. ", 200)}}, entries[1:]...)
	got := ComposeHandover(long, "channels/general/", Now{Text: "go"}, false, Options{Summarizer: StructuredSummarizer{}})
	if !strings.Contains(got, "Rule: never touch prod.") {
		t.Error("a long early message of a person was dropped instead of bounded")
	}
}
