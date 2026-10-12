package store

import (
	"regexp"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/archtest"
)

// TestFailureTextOwned: a failed job's reason becomes a member-facing
// sentence only in AgentJobFailureReason.Text (2026-10-12 review: notify,
// activity's sweeper and its budget refusal each had their own table, and
// the notification read "The agent failed (budget_exhausted).").
func TestFailureTextOwned(t *testing.T) {
	re := regexp.MustCompile(`(?m)^func \w*[Ff]ailure[A-Z]?\w*Text\(`)
	archtest.Scan(t, archtest.Opts{Lang: archtest.Go, Roots: []string{"services", "internal"}}, func(rel string, src []byte) {
		if strings.HasPrefix(rel, "internal/store/") {
			return
		}
		if re.Match(src) {
			t.Errorf("%s turns a failure reason into text itself; use AgentJobFailureReason.Text", rel)
		}
	})
}

func TestFailureText(t *testing.T) {
	if got := FailureNoBridgeOnline.Text("codex"); got != "The codex agent is offline: no bridge picked up this request." {
		t.Errorf("offline = %q", got)
	}
	if got := FailureRunnerFailed.Text(""); got != "The agent could not complete this request." {
		t.Errorf("generic = %q", got)
	}
	for _, r := range []AgentJobFailureReason{FailureBudgetExhausted, FailureSandboxRequired, FailureBorrowNotAuthorized, FailureReplyTimeout} {
		if strings.Contains(r.Text("codex"), string(r)) {
			t.Errorf("%s text shows the raw reason: %q", r, r.Text("codex"))
		}
	}
}
