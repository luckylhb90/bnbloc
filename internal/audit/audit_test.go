package audit

import (
	"context"
	"errors"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/archtest"
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
	rules := map[string]*regexp.Regexp{
		"appends to the audit store directly (use audit.Record)": regexp.MustCompile(`\.AppendExtAudit\(`),
		"spells an outcome (use wire.AuditOutcome*)":             regexp.MustCompile(`Outcome:\s*"`),
	}
	var problems []string
	archtest.Scan(t, archtest.Opts{Lang: archtest.Go, Roots: []string{"services", "internal", "core", "cmd"}}, func(rel string, src []byte) {
		if strings.HasPrefix(rel, "internal/audit/") || strings.HasPrefix(rel, "internal/store/") {
			return
		}
		for i, line := range strings.Split(string(src), "\n") {
			for what, re := range rules {
				if re.MatchString(line) {
					problems = append(problems, rel+":"+strconv.Itoa(i+1)+": "+what)
				}
			}
		}
	})
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
