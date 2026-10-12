package store

import (
	"regexp"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/archtest"
)

// TestJobRequeueOwned: a job goes back on the queue, with its attempt and
// its retry delay recorded, only through AgentJob.requeued and its SQL twin
// sqlRequeue, and the retry backoff table is read only by its owner
// functions (bridge_jobs.go). A second requeue path would put a job back
// without the record or the delay, and the run detail would again show
// "attempt 2" with no reason, or a booting sandbox would burn every attempt
// in seconds (docs/bugs/2026-10-12-retried-job-hides-why.md).
func TestJobRequeueOwned(t *testing.T) {
	const owner = "internal/store/bridge_jobs.go"
	rules := []struct {
		what string
		re   *regexp.Regexp
		max  int // occurrences allowed in the owner; -1: any
	}{
		{"writes attempts_json", regexp.MustCompile(`attempts_json\s*=`), 1},
		{"assigns a job's Attempts", regexp.MustCompile(`\.Attempts\s*=[^=]`), 1},
		{"writes not_before_ms", regexp.MustCompile(`not_before_ms\s*=`), 1},
		{"assigns a job's NotBeforeMs", regexp.MustCompile(`\.NotBeforeMs\s*=[^=]`), 1},
		// normalize (a new job) and requeued.
		{"sets a job queued", regexp.MustCompile(`\.Status\s*=\s*AgentJobQueued\b`), 2},
		// The backoff table and its readers (Retryable, retryDelayMs,
		// sqlRetryDelay) stay in the owner.
		{"reads the retry backoff table", regexp.MustCompile(`\bretryBackoffs\b`), -1},
		{"computes a retry delay", regexp.MustCompile(`\bretryDelayMs\(`), -1},
		{"spells the SQL retry delay", regexp.MustCompile(`\bsqlRetryDelay\(`), -1},
	}
	archtest.Scan(t, archtest.Opts{Lang: archtest.Go, Roots: []string{"internal", "services", "cmd"}}, func(rel string, src []byte) {
		for _, r := range rules {
			n := len(r.re.FindAll(src, -1))
			switch {
			case rel == owner && r.max >= 0 && n > r.max:
				t.Errorf("%s %s %d times; the one requeue is AgentJob.requeued / sqlRequeue", rel, r.what, n)
			case rel != owner && n > 0:
				t.Errorf("%s %s; requeue a job and decide its retry delay only in %s (AgentJob.requeued / sqlRequeue, retryBackoffs)", rel, r.what, owner)
			}
		}
	})
}
