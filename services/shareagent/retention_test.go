package shareagenthttp

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

func retentionService(t *testing.T) (*Service, *store.MemoryExtensionAudit) {
	t.Helper()
	svc, _ := newService(t, "")
	tenants := store.NewMemoryTenant()
	for _, id := range []string{"acme", "globex"} {
		_ = tenants.PutTenant(tctx(id), store.Tenant{TenantID: id, Name: id, Status: store.TenantActive})
	}
	audit := store.NewMemoryExtensionAudit()
	svc.Tenants, svc.Settings, svc.ExtAudit = tenants, store.NewMemoryPlatformSettings(), audit
	return svc, audit
}

// The platform default and a tenant override round-trip; the tenant view
// resolves the effective days (override over default, 0 = keep forever);
// out-of-range days are refused; a tenant override only ever changes the
// request's own tenant; every change is audited.
func TestRetentionPolicyRoutes(t *testing.T) {
	svc, audit := retentionService(t)

	code, body := tenantPolicyReq(t, svc, http.MethodGet, "/v1/platform/retention", "acme", "root", nil)
	if code != http.StatusOK || body["default"].(map[string]any)["messagesDays"] != nil {
		t.Fatalf("empty default: %d %v", code, body)
	}
	if lim := body["limits"].(map[string]any); lim["minDays"] != float64(wire.RetentionMinDays) || lim["maxDays"] != float64(wire.RetentionMaxDays) {
		t.Fatalf("limits = %v", lim)
	}
	for _, bad := range []map[string]any{{"messagesDays": 5}, {"auditDays": -1}, {"agentJobsDays": wire.RetentionMaxDays + 1}} {
		if code, _ := tenantPolicyReq(t, svc, http.MethodPut, "/v1/platform/retention", "acme", "root", bad); code != http.StatusBadRequest {
			t.Fatalf("PUT %v = %d, want 400", bad, code)
		}
		if code, _ := tenantPolicyReq(t, svc, http.MethodPut, "/v1/tenant/retention", "acme", "maya", bad); code != http.StatusBadRequest {
			t.Fatalf("tenant PUT %v = %d, want 400", bad, code)
		}
	}
	code, body = tenantPolicyReq(t, svc, http.MethodPut, "/v1/platform/retention", "acme", "root", map[string]any{"messagesDays": 365, "auditDays": 0})
	if def := body["default"].(map[string]any); code != http.StatusOK || def["messagesDays"] != float64(365) || def["auditDays"] != nil {
		t.Fatalf("set default: %d %v", code, body)
	}

	code, body = tenantPolicyReq(t, svc, http.MethodPut, "/v1/tenant/retention", "acme", "maya", map[string]any{"messagesDays": 0, "agentJobsDays": 90})
	if code != http.StatusOK || body["tenantId"] != "acme" {
		t.Fatalf("set override: %d %v", code, body)
	}
	over, eff := body["override"].(map[string]any), body["effective"].(map[string]any)
	if over["messagesDays"] != float64(0) || over["auditDays"] != nil || eff["messagesDays"] != nil || eff["agentJobsDays"] != float64(90) {
		t.Fatalf("override %v effective %v", over, eff)
	}
	// globex was not touched: it inherits the default.
	code, body = tenantPolicyReq(t, svc, http.MethodGet, "/v1/tenant/retention", "globex", "jack", nil)
	if eff := body["effective"].(map[string]any); code != http.StatusOK || eff["messagesDays"] != float64(365) || eff["agentJobsDays"] != nil {
		t.Fatalf("globex view: %d %v", code, body)
	}
	recs, _ := audit.ExtAuditRecords(context.Background(), wire.AuditKindRetention, 10, 0)
	if len(recs) != 2 {
		t.Fatalf("audit records = %+v", recs)
	}
	for _, r := range recs {
		if r.Name == "acme" && (r.TenantID != "acme" || r.Actor != "maya" || !strings.Contains(r.Detail, "messages=forever") || !strings.Contains(r.Detail, "audit=default")) {
			t.Fatalf("tenant audit record = %+v", r)
		}
		if r.Name == "platform" && (r.TenantID != "" || !strings.Contains(r.Detail, "messages=365") || !strings.Contains(r.Detail, "audit=forever")) {
			t.Fatalf("platform audit record = %+v", r)
		}
	}
}
