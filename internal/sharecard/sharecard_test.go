package sharecard

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/internal/wire"
)

func TestPlainStripsMarkdownAndControls(t *testing.T) {
	in := "## Plan\n> **bold** and `code`\n- [the doc](https://x.example/a) \x00ok\t\n\n  1. two"
	got := Plain(in)
	want := "Plan bold and code the doc ok two"
	if got != want {
		t.Fatalf("Plain = %q, want %q", got, want)
	}
}

// A summary is cut by characters, never inside one, and says it was cut.
func TestSummaryCutsOnRunes(t *testing.T) {
	long := strings.Repeat("字", 300)
	got := Summary(long)
	if n := utf8.RuneCountInString(got); n != wire.MaxShareSummaryRunes {
		t.Fatalf("runes = %d, want %d", n, wire.MaxShareSummaryRunes)
	}
	if !utf8.ValidString(got) || !strings.HasSuffix(got, "…") {
		t.Fatalf("summary = %q", got)
	}
	if short := Summary("short"); short != "short" {
		t.Fatalf("short summary = %q", short)
	}
}

func TestNewBoundsTitleAndSummary(t *testing.T) {
	c := New(wire.ShareItemRoom, strings.Repeat("a", 500), strings.Repeat("b", 500), "https://x.example/#/room/r")
	if utf8.RuneCountInString(c.Title) != wire.MaxShareTitleRunes || utf8.RuneCountInString(c.Summary) != wire.MaxShareSummaryRunes {
		t.Fatalf("card = %+v", c)
	}
}
