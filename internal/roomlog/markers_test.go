package roomlog

import (
	"strings"
	"testing"
)

// attributedEntries are a person's message to an agent, the task an agent
// dispatched on that person's behalf, and the reply.
var attributedEntries = []Entry{
	{Seq: 10, Speaker: "maya", Kind: KindHuman, Text: "jack, review the parser", To: "jack/codex"},
	{Seq: 11, Speaker: "maya", Kind: KindHuman, Text: "check parser.go for races", To: "jill/claude", Via: "jack/codex"},
	{Seq: 12, Speaker: "jill/claude", Kind: KindAgent, Text: "no races found"},
}

// A record line names who said what: a task an agent dispatched reads as
// the agent's, for the person it acted for, never as the person's own words
// (2026-10-13: "jack/codex for maya" read as "maya").
func TestLineAttribution(t *testing.T) {
	want := []string{
		"maya → jack/codex: jack, review the parser\n",
		"jack/codex for maya → jill/claude: check parser.go for races\n",
		"jill/claude: no races found\n",
	}
	for i, e := range attributedEntries {
		if got := Line(e); got != want[i] {
			t.Errorf("Line(%d) = %q, want %q", e.Seq, got, want[i])
		}
	}
	if got := Sender(attributedEntries[1]); got != "jack/codex for maya" {
		t.Errorf("Sender = %q", got)
	}
	if got := Line(Entry{Text: "x"}); got != "unknown: x\n" {
		t.Errorf("Line without a speaker = %q", got)
	}
}

// Every composer and .room/ file renders lines through Line, and the [now]
// marker names the sender.
func TestAttributionEverywhere(t *testing.T) {
	files, _ := Materialize(attributedEntries, true, CtxState{})
	now := Now{From: Sender(attributedEntries[1]), Text: "go"}
	for name, out := range map[string]string{
		"recent.md": files[0].Content,
		"chunk":     files[1].Content,
		"compose":   Compose(attributedEntries, now, Options{}),
		"delta":     ComposeDelta(attributedEntries, now, Options{}),
	} {
		if !strings.Contains(out, "jack/codex for maya → jill/claude: check parser.go") {
			t.Errorf("%s drops the attribution:\n%s", name, out)
		}
	}
	for name, out := range map[string]string{
		"compose": Compose(nil, now, Options{}),
		"delta":   ComposeDelta(nil, now, Options{}),
		"pointer": ComposePointer("jill/claude", now),
	} {
		if !strings.Contains(out, "[now — from jack/codex for maya]\ngo") {
			t.Errorf("%s: the [now] marker does not name the sender:\n%s", name, out)
		}
	}
	if got := nowMarker(""); got != "[now]\n" {
		t.Errorf("nowMarker without a sender = %q", got)
	}
}
