package store

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestFailureTextOwned: a failed job's reason becomes a member-facing
// sentence only in AgentJobFailureReason.Text (2026-10-12 review: notify,
// activity's sweeper and its budget refusal each had their own table, and
// the notification read "The agent failed (budget_exhausted).").
func TestFailureTextOwned(t *testing.T) {
	re := regexp.MustCompile(`(?m)^func \w*[Ff]ailure[A-Z]?\w*Text\(`)
	root := filepath.Join("..", "..")
	for _, dir := range []string{"services", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			if strings.HasPrefix(filepath.ToSlash(path), "../../internal/store/") {
				return nil
			}
			raw, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			if re.Match(raw) {
				t.Errorf("%s turns a failure reason into text itself; use AgentJobFailureReason.Text", path)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
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
