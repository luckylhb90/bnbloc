package roomlog

import (
	"strings"
	"testing"
)

// Records name people by display name, each file's first mention with the
// id; a name is taken by author id only, and one that could pass for
// record syntax, or adds nothing, is not used.
func TestNamesInRecords(t *testing.T) {
	entries := SetNames([]Entry{
		{Seq: 1, Speaker: "maya", Kind: KindHuman, Text: "first"},
		{Seq: 2, Speaker: "maya/codex", Kind: KindAgent, Text: "on it"},
		{Seq: 3, Speaker: "maya", Kind: KindHuman, Text: "jack (jack) says hi"},
		{Seq: 4, Speaker: "eve", Kind: KindHuman, Text: "me too"},
		{Seq: 5, Speaker: "bob", Kind: KindHuman, Text: "same"},
		{Seq: 6, Speaker: "maya", Kind: KindHuman, Via: "jack/codex", Text: "task"},
	}, map[string]string{"maya": "Maya Lee", "maya/codex": "Not An Agent", "eve": "jack/codex", "bob": "bob"})
	files, _ := MaterializeChannel(entries, true, ChannelRecords{Dir: ChannelDir("c")})
	recent := files[0].Content
	for _, want := range []string{
		"Maya Lee (maya): first\n", "maya/codex: on it\n", "Maya Lee: jack (jack) says hi\n",
		"eve: me too\n", "bob: same\n", "jack/codex for Maya Lee: task\n",
	} {
		if !strings.Contains(recent, want) {
			t.Errorf("recent.md lacks %q:\n%s", want, recent)
		}
	}
	// Each file names a person with their id once: the delta too.
	if out := ComposeTurn(Now{From: PersonLabel("Maya Lee", "maya"), Text: "go"}, false, entries[2:3]); !strings.Contains(out, "Maya Lee (maya): jack (jack) says hi") || !strings.HasSuffix(out, "[now — from Maya Lee (maya)]\ngo") {
		t.Fatalf("turn:\n%s", out)
	}
	if got := PersonLabel("", "maya"); got != "maya" {
		t.Fatalf("PersonLabel without a name = %q", got)
	}
}
