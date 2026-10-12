package store

import (
	"regexp"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/archtest"
)

// TestJobRequeueOwned: a job goes back on the queue, with its attempt
// recorded, only through AgentJob.requeued and its SQL twin sqlRequeue
// (bridge_jobs.go). A second requeue path would put a job back without the
// record, and the run detail would again show "attempt 2" with no reason
// (docs/bugs/2026-10-12-retried-job-hides-why.md).
func TestJobRequeueOwned(t *testing.T) {
	const owner = "internal/store/bridge_jobs.go"
	rules := []struct {
		what string
		re   *regexp.Regexp
		max  int // occurrences allowed in the owner
	}{
		{"writes attempts_json", regexp.MustCompile(`attempts_json\s*=`), 1},
		{"assigns a job's Attempts", regexp.MustCompile(`\.Attempts\s*=[^=]`), 1},
		// normalize (a new job) and requeued.
		{"sets a job queued", regexp.MustCompile(`\.Status\s*=\s*AgentJobQueued\b`), 2},
	}
	archtest.Scan(t, archtest.Opts{Lang: archtest.Go, Roots: []string{"internal", "services", "cmd"}}, func(rel string, src []byte) {
		for _, r := range rules {
			n := len(r.re.FindAll(src, -1))
			switch {
			case rel == owner && n > r.max:
				t.Errorf("%s %s %d times; the one requeue is AgentJob.requeued / sqlRequeue", rel, r.what, n)
			case rel != owner && n > 0:
				t.Errorf("%s %s; requeue a job only through AgentJob.requeued / sqlRequeue (%s)", rel, r.what, owner)
			}
		}
	})
}
