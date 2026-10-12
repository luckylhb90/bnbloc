package audit

import (
	"context"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// TestAuditThroughOneWriter: the shared audit trail has one writer,
// Record, so a failed append is handled one way (logged, never surfaced;
// it was `_ =` in five places and logged in others) and outcomes come from
// one vocabulary (wire.AuditOutcome*; "ok" vs "allow", "error" vs "failed"
// drifted). The scan fails on an AppendExtAudit call outside this package
// and the store, and on a string-literal Outcome.
func TestAuditThroughOneWriter(t *testing.T) {
	root := filepath.Join("..", "..")
	rules := map[string]*regexp.Regexp{
		"appends to the audit store directly (use audit.Record)": regexp.MustCompile(`\.AppendExtAudit\(`),
		"spells an outcome (use wire.AuditOutcome*)":             regexp.MustCompile(`Outcome:\s*"`),
	}
	var problems []string
	for _, dir := range []string{"services", "internal", "core", "cmd"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			rel = filepath.ToSlash(rel)
			if strings.HasPrefix(rel, "internal/audit/") || strings.HasPrefix(rel, "internal/store/") {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for i, line := range strings.Split(string(raw), "\n") {
				for what, re := range rules {
					if re.MatchString(line) {
						problems = append(problems, rel+":"+strconv.Itoa(i+1)+": "+what)
					}
				}
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(problems)
	if len(problems) > 0 {
		t.Fatalf("audit written outside its one writer:\n  %s", strings.Join(problems, "\n  "))
	}
}

type failingSink struct {
	store.ExtensionAuditStore
	got []store.ExtAuditRecord
	err error
}

func (f *failingSink) AppendExtAudit(_ context.Context, rec store.ExtAuditRecord) error {
	f.got = append(f.got, rec)
	return f.err
}

// Record defaults the outcome and time, tolerates a nil sink, and swallows
// (logs) a failed append.
func TestRecordDefaultsAndBestEffort(t *testing.T) {
	Record(context.Background(), nil, store.ExtAuditRecord{Kind: "x"}) // no panic
	sink := &failingSink{err: errors.New("db down")}
	Record(context.Background(), sink, store.ExtAuditRecord{Kind: "member", Action: "role"})
	if len(sink.got) != 1 || sink.got[0].Outcome != wire.AuditOutcomeOK || sink.got[0].AtMs == 0 {
		t.Fatalf("recorded = %+v", sink.got)
	}
	Record(context.Background(), sink, store.ExtAuditRecord{Kind: "member", Outcome: wire.AuditOutcomeDeny, AtMs: 5})
	if r := sink.got[1]; r.Outcome != wire.AuditOutcomeDeny || r.AtMs != 5 {
		t.Fatalf("explicit fields overwritten: %+v", r)
	}
}
