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
	got := Compose(entries, Now{From: "jack", Text: "who wrote the login endpoint?"}, Options{SelfSpeaker: "maya/orbit"})

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
	if !strings.Contains(got, "[now — from jack]\nwho wrote the login endpoint?") {
		t.Fatal("current message must directly follow the now marker")
	}
}

// Empty history is a first turn: Compose returns just the current message,
// under the [now] marker naming its sender.
func TestComposeEmptyIsBareMessage(t *testing.T) {
	if got := Compose(nil, Now{From: "maya", Text: "hello"}, Options{SelfSpeaker: "maya/orbit"}); got != "[now — from maya]\nhello" {
		t.Fatalf("empty history should return the message alone, got %q", got)
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
	got := Compose(entries, Now{Text: "continue"}, Options{
		SelfSpeaker: "maya/orbit",
		WindowChars: 60, // fits only the newest short entry
		Summarizer:  StructuredSummarizer{},
	})
	story, window := RecordsHeader("Story so far"), RecordsHeader("Room conversation")
	if !strings.Contains(got, story) {
		t.Fatalf("overflow must produce a story-so-far section\n---\n%s", got)
	}
	if !strings.Contains(got, window) || !strings.Contains(got, "the newest thing") {
		t.Fatal("newest entry must remain verbatim in the window")
	}
	// The story-so-far section precedes the verbatim window.
	if strings.Index(got, story) > strings.Index(got, window) {
		t.Fatal("summary must precede the verbatim window")
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

// DecodeRow maps chat_message -> human line and agent_messages -> one
// attributed agent line per reply.
func TestDecodeRowSpeakers(t *testing.T) {
	human := DecodeRow(Row{Sequence: 5, Kind: "chat_message", Payload: []byte(`{"userId":"jack","text":"hi","recipient":{"ownerId":"maya","provider":"orbit"},"dispatchedBy":{"jobId":"j1","via":"jack/codex"}}`)})
	if len(human) != 1 || human[0].Speaker != "jack" || human[0].Kind != KindHuman || human[0].To != "maya/orbit" || human[0].Via != "jack/codex" {
		t.Fatalf("bad human entry: %+v", human)
	}
	agent := DecodeRow(Row{Sequence: 6, Kind: "agent_messages", Payload: []byte(
		`{"agentSessionId":"s1","messages":[{"payload":{"role":"assistant","text":"done","ownerUserId":"maya","provider":"orbit"}}]}`)})
	if len(agent) != 1 || agent[0].Speaker != "maya/orbit" || agent[0].Kind != KindAgent || agent[0].Session != "s1" || agent[0].Owner != "maya" {
		t.Fatalf("bad agent entry: %+v", agent)
	}
	if got := DecodeRow(Row{Sequence: 7, Kind: "agent_messages", Payload: []byte(`{"messages":[{"payload":{"text":"  "}}]}`)}); len(got) != 0 {
		t.Fatalf("blank reply should be dropped, got %+v", got)
	}
}

// Notices are never read back to an agent: neither a row carrying
// payload.notice (wire.JobNotice) nor any row of a job's notice session.
func TestDecodeRowSkipsNotices(t *testing.T) {
	for name, payload := range map[string]string{
		"notice field":   `{"agentSessionId":"s1","messages":[{"payload":{"role":"assistant","notice":"turn-failed","text":"The agent could not complete this request","ownerUserId":"maya","provider":"orbit"}}]}`,
		"notice session": `{"agentSessionId":"job-j1","messages":[{"payload":{"role":"assistant","text":"The orbit agent is offline","ownerUserId":"maya","provider":"orbit"}}]}`,
		"progress":       `{"agentSessionId":"job-j1-progress","messages":[{"payload":{"role":"assistant","text":"Phase 2/3","ownerUserId":"maya","provider":"orbit"}}]}`,
	} {
		if got := DecodeRow(Row{Sequence: 8, Kind: "agent_messages", Payload: []byte(payload)}); len(got) != 0 {
			t.Errorf("%s: a notice was decoded as speech: %+v", name, got)
		}
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
	out := Compose(entries, Now{From: "maya", Text: "存到文件里"}, Options{
		SelfSpeaker: "maya/nimbus",
		WindowChars: 1, // push both entries past the verbatim window into the summary
		Summarizer:  StructuredSummarizer{},
	})
	for _, want := range []string{
		"SHARED HISTORY",              // history is labeled as background
		"NOT requests",                // ...and explicitly not instructions
		RecordsHeader("Story so far"), // the summary is a records section
		"respond ONLY to the [now]",   // the closing reminder binds the reply to now
	} {
		if !strings.Contains(out, want) {
			t.Errorf("Compose output missing fence %q:\n%s", want, out)
		}
	}
	if !strings.Contains(out, "[now — from maya]\n存到文件里") {
		t.Errorf("current message must directly follow the [now] marker:\n%s", out)
	}
}
