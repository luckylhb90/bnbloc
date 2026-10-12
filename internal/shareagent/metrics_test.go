package shareagent

import (
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/obs"
)

// Audited decisions are counted by decision and a bounded reason.
func TestBorrowDecisionMetrics(t *testing.T) {
	m := obs.NewMetrics("bnbloc-shareagent")
	RegisterMetrics(m)
	RecordDecision(m, "deny", DenyOwnerNotAuthorized)
	RecordDecision(m, "deny", DenyGrantInvalid)
	RecordDecision(m, "allow", "")
	RecordDecision(m, "deny", "something-new")
	RecordDecision(m, "maybe", "x")

	rec := httptest.NewRecorder()
	m.Handler()(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()
	for _, want := range []string{
		`bnbloc_borrow_decisions_total{service="bnbloc-shareagent",decision="allow",reason="none"} 1`,
		`bnbloc_borrow_decisions_total{service="bnbloc-shareagent",decision="deny",reason="owner_not_authorized"} 1`,
		`bnbloc_borrow_decisions_total{service="bnbloc-shareagent",decision="deny",reason="grant_invalid"} 1`,
		`bnbloc_borrow_decisions_total{service="bnbloc-shareagent",decision="deny",reason="other"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
	if strings.Contains(body, `decision="maybe"`) {
		t.Fatal("unknown decision counted")
	}
}
