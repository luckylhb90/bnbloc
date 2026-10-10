package shareagent

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/obs"
)

// Audited decisions are counted by decision and a bounded reason; a release
// is not a borrow decision.
func TestBorrowDecisionMetrics(t *testing.T) {
	m := obs.NewMetrics("bnbloc-shareagent")
	RegisterMetrics(m)
	// An unwired controller fails closed (grant_invalid) and audits nowhere,
	// but the decision is still counted.
	c := &Controller{Metrics: m, Now: func() int64 { return 1 }}
	c.audit(context.Background(), Request{Action: ActionTurn}, "deny", DenyOwnerOffline, "o")
	c.audit(context.Background(), Request{Action: ActionRelease}, "allow", "", "o")
	if d := c.Authorize(context.Background(), Request{Action: ActionTurn}); d.Allowed || d.DenyReason != DenyGrantInvalid {
		t.Fatalf("unwired controller = %+v", d)
	}
	RecordDecision(m, "allow", "")
	RecordDecision(m, "deny", "something-new")
	RecordDecision(m, "maybe", "x")

	rec := httptest.NewRecorder()
	m.Handler()(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()
	for _, want := range []string{
		`bnbloc_borrow_decisions_total{service="bnbloc-shareagent",decision="allow",reason="none"} 1`,
		`bnbloc_borrow_decisions_total{service="bnbloc-shareagent",decision="deny",reason="owner_offline"} 1`,
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
