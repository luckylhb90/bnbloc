package store

import (
	"errors"
	"strings"
	"testing"
)

func TestDisplayNameDefaultRule(t *testing.T) {
	cases := []struct{ id, stored, want string }{
		{"jane.d@example.com", "", "jane.d"},
		{"alice", "", "alice"},
		{"@weird", "", "@weird"},
		{" bob@x.io ", "", "bob"},
		{"jane.d@example.com", "Maya", "Maya"},
	}
	for _, c := range cases {
		if got := DisplayName(c.id, c.stored); got != c.want {
			t.Errorf("DisplayName(%q, %q) = %q, want %q", c.id, c.stored, got, c.want)
		}
	}
}

func TestNormalizeDisplayName(t *testing.T) {
	ok := map[string]string{
		"  Maya  ":                    "Maya",
		"":                            "",
		"   ":                         "",
		"李小龙":                         "李小龙",
		strings.Repeat("é", 40):       strings.Repeat("é", 40),
		"Ana María · design (she)":    "Ana María · design (she)",
		"\tpadded with tabs\t":        "padded with tabs",
		strings.Repeat("x", 40) + " ": strings.Repeat("x", 40),
	}
	for in, want := range ok {
		got, err := NormalizeDisplayName(in)
		if err != nil || got != want {
			t.Errorf("NormalizeDisplayName(%q) = %q, %v; want %q", in, got, err, want)
		}
	}
	for _, bad := range []string{strings.Repeat("x", 41), "line\nbreak", "bell\a", "del\x7f", "c1\u0085x", "bad\xffutf8"} {
		if _, err := NormalizeDisplayName(bad); !errors.Is(err, ErrInvalidDisplayName) {
			t.Errorf("NormalizeDisplayName(%q) err = %v, want ErrInvalidDisplayName", bad, err)
		}
	}
}
