package gateway

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestAuditWrittenOnlyByAudit: a gateway audit row is written by s.audit
// (audit.go, over the shared internal/audit.Record), which stamps the
// gateway's time and always takes the row's tenant — hand-built rows
// omitted it (an API token issue, a notifier test), so the tenant's admins
// never saw them.
func TestAuditWrittenOnlyByAudit(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || f == "audit.go" {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), ".AppendExtAudit(") || strings.Contains(string(raw), "audit.Record(") {
			t.Errorf("%s writes an audit row itself; use s.audit(ctx, tenantID, rec)", f)
		}
	}
}

// TestContentOwnershipOneRule: "is this component mine" is ownedBy (owner
// AND the organization the caller acts in); an owner-only comparison
// matched the same subject's rows in another organization.
func TestContentOwnershipOneRule(t *testing.T) {
	ownerOnly := regexp.MustCompile(`\.Owner (==|!=) \w+(\.Claims)?\.Subject\b`)
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for i, line := range strings.Split(string(raw), "\n") {
			if ownerOnly.MatchString(line) && !strings.Contains(line, "TenantID") {
				t.Errorf("%s:%d compares a component owner without its tenant; use ownedBy", f, i+1)
			}
		}
	}
}
