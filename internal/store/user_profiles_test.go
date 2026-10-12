package store

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
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

func profileCases(t *testing.T, s UserProfileStore, suffix string) {
	t.Helper()
	ctx := context.Background()
	alice, bob, carol := "alice"+suffix+"@x.io", "bob"+suffix, "carol"+suffix
	if got, err := s.DisplayNames(ctx, nil); err != nil || len(got) != 0 {
		t.Fatalf("empty lookup = %v, %v", got, err)
	}
	if err := s.SetDisplayName(ctx, alice, "Alice A", 1); err != nil {
		t.Fatalf("set: %v", err)
	}
	if err := s.SetDisplayName(ctx, bob, "Bob", 1); err != nil {
		t.Fatalf("set: %v", err)
	}
	// Overwrite.
	if err := s.SetDisplayName(ctx, bob, "Bobby", 2); err != nil {
		t.Fatalf("overwrite: %v", err)
	}
	upper := strings.ToUpper(alice)
	got, err := s.DisplayNames(ctx, []string{alice, bob, carol, upper})
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if len(got) != 2 || got[alice] != "Alice A" || got[bob] != "Bobby" {
		t.Fatalf("lookup = %v (want alice and bob; carol and the upper-case variant, another subject, absent)", got)
	}
	if _, ok := got[upper]; ok {
		t.Fatalf("a case variant of alice is another subject, got %v", got)
	}
	if _, ok := got[carol]; ok {
		t.Fatalf("carol has no stored name, got %v", got)
	}
	// Clearing restores the default (no row).
	if err := s.SetDisplayName(ctx, bob, "", 3); err != nil {
		t.Fatalf("clear: %v", err)
	}
	if got, _ := s.DisplayNames(ctx, []string{bob}); len(got) != 0 {
		t.Fatalf("after clear = %v", got)
	}
	// Clearing an id with no name is a no-op.
	if err := s.SetDisplayName(ctx, carol, "", 4); err != nil {
		t.Fatalf("clear absent: %v", err)
	}
}

func TestMemoryUserProfiles(t *testing.T) {
	profileCases(t, NewMemoryUserProfiles(), "")
}

func TestMySQLUserProfiles(t *testing.T) {
	dsn := os.Getenv("BNBLOC_TEST_MYSQL_DSN")
	if dsn == "" {
		t.Skip("set BNBLOC_TEST_MYSQL_DSN to run the MySQL integration test")
	}
	db, err := OpenMySQL(dsn)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer db.Close()
	profileCases(t, NewMySQLUserProfiles(db), strconv.FormatInt(time.Now().UnixNano(), 36))
}
