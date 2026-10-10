package roomlog

import (
	"strings"
	"testing"
)

// Compose must render a multi-party, speaker-attributed transcript ending with
// the current message — the format verified to be understood by the providers.
func TestComposeMultiPartyFormat(t *testing.T) {
	entries := []Entry{
		{Seq: 1, Speaker: "jack", Kind: KindHuman, Text: "add login with JWT"},
		{Seq: 2, Speaker: "jack/codex", Kind: KindAgent, Text: "wrote /api/login, 24h token"},
	}
	got := Compose(entries, "who wrote the login endpoint?", Options{SelfSpeaker: "maya/orbit"})

	for _, want := range []string{
		"maya/orbit",                    // self label present
		"jack: add login with JWT",      // human line, attributed
		"jack/codex: wrote /api/login",  // agent line, attributed
		"who wrote the login endpoint?", // current message present
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("composed prompt missing %q\n---\n%s", want, got)
		}
	}
	if strings.Index(got, "jack: add login") > strings.Index(got, "[now") {
		t.Fatal("conversation must precede the now section")
	}
	// The current message directly follows the [now] marker; a short reminder
	// (fencing history off from the instruction) closes the prompt.
	if !strings.Contains(got, "[now — respond to this]\nwho wrote the login endpoint?") {
		t.Fatal("current message must directly follow the now marker")
	}
}

// Empty history and no files is a first turn: Compose returns just the current
// message so nothing changes for a brand-new conversation.
func TestComposeEmptyIsBareMessage(t *testing.T) {
	if got := Compose(nil, "hello", Options{SelfSpeaker: "maya/orbit"}); got != "hello" {
		t.Fatalf("empty history should return bare message, got %q", got)
	}
}

// When history overflows the verbatim window, older turns must appear condensed
// in a story-so-far section (L1) and only the newest fit verbatim (L2), so a
// long conversation stays bounded without losing older context entirely.
func TestComposeSummarizesOverflow(t *testing.T) {
	entries := []Entry{
		{Seq: 1, Speaker: "jack", Kind: KindHuman, Text: strings.Repeat("old-a ", 40)},
		{Seq: 2, Speaker: "mia", Kind: KindHuman, Text: strings.Repeat("old-b ", 40)},
		{Seq: 3, Speaker: "jack", Kind: KindHuman, Text: "the newest thing"},
	}
	got := Compose(entries, "continue", Options{
		SelfSpeaker: "maya/orbit",
		WindowChars: 60, // fits only the newest short entry
		Summarizer:  StructuredSummarizer{},
	})
	if !strings.Contains(got, "[story so far]") {
		t.Fatalf("overflow must produce a story-so-far section\n---\n%s", got)
	}
	if !strings.Contains(got, "[room conversation]") || !strings.Contains(got, "the newest thing") {
		t.Fatal("newest entry must remain verbatim in the window")
	}
	// The story-so-far section precedes the verbatim window.
	if strings.Index(got, "[story so far]") > strings.Index(got, "[room conversation]") {
		t.Fatal("summary must precede the verbatim window")
	}
}

// Files (L3) are injected as a reference block, never as file contents, and even
// with no conversation a files-only context is composed (not the bare message).
func TestComposeInjectsFileManifest(t *testing.T) {
	got := Compose(nil, "what changed?", Options{
		SelfSpeaker: "maya/orbit",
		Files:       "- src/login.go (512 bytes)",
	})
	if !strings.Contains(got, "[workspace files]") || !strings.Contains(got, "src/login.go") {
		t.Fatalf("files manifest must be injected\n---\n%s", got)
	}
	if got == "what changed?" {
		t.Fatal("files-only context must not collapse to the bare message")
	}
}

// splitWindow keeps the newest entries within budget verbatim and hands the rest
// to the summary side, preserving chronological order; it never returns an empty
// window when entries exist.
func TestSplitWindowNewestVerbatim(t *testing.T) {
	entries := []Entry{
		{Seq: 1, Speaker: "a", Text: strings.Repeat("x", 100)},
		{Seq: 2, Speaker: "b", Text: strings.Repeat("y", 100)},
		{Seq: 3, Speaker: "c", Text: strings.Repeat("z", 100)},
	}
	older, window := splitWindow(entries, 150) // fits ~one entry
	if len(window) == 0 || window[len(window)-1].Seq != 3 {
		t.Fatalf("must keep the newest entry verbatim, got window %+v", window)
	}
	if len(older)+len(window) != len(entries) {
		t.Fatalf("split must partition all entries, got older=%d window=%d", len(older), len(window))
	}
	// older is strictly before window in sequence.
	if len(older) > 0 && older[len(older)-1].Seq >= window[0].Seq {
		t.Fatal("older must precede the window chronologically")
	}
	// No bound keeps everything verbatim.
	o, wnd := splitWindow(entries, 0)
	if len(o) != 0 || len(wnd) != 3 {
		t.Fatal("windowChars<=0 must keep all entries verbatim")
	}
}

// StructuredSummarizer condenses deterministically: one bullet per speaker with
// a turn count and their latest line, so any agent derives the same summary.
func TestStructuredSummarizerDeterministic(t *testing.T) {
	entries := []Entry{
		{Seq: 1, Speaker: "jack", Kind: KindHuman, Text: "first"},
		{Seq: 2, Speaker: "jack", Kind: KindHuman, Text: "second"},
		{Seq: 3, Speaker: "mia", Kind: KindHuman, Text: "hello"},
	}
	s := StructuredSummarizer{}
	got := s.Summarize(entries)
	again := s.Summarize(entries)
	if got != again {
		t.Fatal("summary must be deterministic for the same input")
	}
	if !strings.Contains(got, "jack (2 turns): second") {
		t.Fatalf("must fold jack's turns with latest line\n%s", got)
	}
	if !strings.Contains(got, "mia (1 turn): hello") {
		t.Fatalf("must render singular turn\n%s", got)
	}
	if s.Summarize(nil) != "" {
		t.Fatal("empty input must summarize to empty")
	}
}

// rowsToEntries maps chat_message -> human line and agent_messages -> one
// attributed agent line per reply.
func TestRowsToEntriesSpeakers(t *testing.T) {
	human := rowsToEntries(5, "chat_message", []byte(`{"userId":"jack","text":"hi"}`))
	if len(human) != 1 || human[0].Speaker != "jack" || human[0].Kind != KindHuman {
		t.Fatalf("bad human entry: %+v", human)
	}
	agent := rowsToEntries(6, "agent_messages", []byte(
		`{"messages":[{"payload":{"role":"assistant","text":"done","ownerUserId":"maya","provider":"orbit"}}]}`))
	if len(agent) != 1 || agent[0].Speaker != "maya/orbit" || agent[0].Kind != KindAgent {
		t.Fatalf("bad agent entry: %+v", agent)
	}
	if got := rowsToEntries(7, "agent_messages", []byte(`{"messages":[{"payload":{"text":"  "}}]}`)); len(got) != 0 {
		t.Fatalf("blank reply should be dropped, got %+v", got)
	}
}

// The shared-history framing must fence history off from the instruction: a
// room full of ops chatter once made "save to a file" mean "persist the
// health-check discussion" — the agent treated history as tasks. Compose must
// label history as records-only and close with a respond-only-to-now reminder.
func TestComposeFencesHistoryFromInstruction(t *testing.T) {
	entries := []Entry{
		{Speaker: "maya", Text: "清理 claude-mem 插件缓存（~10.6GB）"},
		{Speaker: "maya/nimbus", Text: "扫描完成，本地一共有 5593 个会话记录。"},
	}
	out := Compose(entries, "存到文件里", Options{
		SelfSpeaker: "maya/nimbus",
		WindowChars: 1, // push both entries past the verbatim window into the summary
		Summarizer:  StructuredSummarizer{},
	})
	for _, want := range []string{
		"SHARED HISTORY",            // history is labeled as background
		"NOT requests",              // ...and explicitly not instructions
		"records of past turns",     // the summary header repeats the framing
		"respond ONLY to the [now]", // the closing reminder binds the reply to now
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Compose output missing fence %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "[now — respond to this]\n存到文件里") {
		t.Errorf("current message must directly follow the [now] marker:\n%s", out)
	}
}
