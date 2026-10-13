package shareagenthttp

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/store"
)

func tenantPolicyReq(t *testing.T, svc *Service, method, path, ctxTenant, caller string, body any) (int, map[string]any) {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		_ = json.NewEncoder(&buf).Encode(body)
	}
	req := httptest.NewRequest(method, path, &buf).WithContext(tctx(ctxTenant))
	req.Header.Set("X-Bnbloc-User", caller)
	rec := httptest.NewRecorder()
	svc.Handler().ServeHTTP(rec, req)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// The org admin's policy switches round-trip through the tenant settings and
// are readable by members (the owner's bridge, the web) for any tenant they
// belong to.
func TestTenantPolicy_settingsAndMemberRead(t *testing.T) {
	svc, _ := newService(t)
	tenants := store.NewMemoryTenant()
	for _, id := range []string{"acme", "globex"} {
		_ = tenants.PutTenant(tctx(id), store.Tenant{TenantID: id, Name: id, Status: store.TenantActive})
	}
	_ = tenants.PutMember(tctx("acme"), store.TenantMember{TenantID: "acme", UserID: "alice"})
	_ = tenants.PutMember(tctx("globex"), store.TenantMember{TenantID: "globex", UserID: "alice"})
	svc.Tenants = tenants

	if code, body := tenantPolicyReq(t, svc, http.MethodGet, "/v1/my/tenant-policy", "acme", "alice", nil); code != http.StatusOK || body["requireSandboxForBorrow"] != false || body["tenantId"] != "acme" {
		t.Fatalf("default policy: %d %v", code, body)
	}
	code, body := tenantPolicyReq(t, svc, http.MethodPut, "/v1/tenant/settings", "globex", "admin", map[string]any{"requireSandboxForBorrow": true})
	if code != http.StatusOK || body["requireSandboxForBorrow"] != true || body["name"] != "globex" {
		t.Fatalf("set policy: %d %v", code, body)
	}
	if code, body := tenantPolicyReq(t, svc, http.MethodGet, "/v1/tenant/settings", "globex", "admin", nil); code != http.StatusOK || body["requireSandboxForBorrow"] != true {
		t.Fatalf("settings view: %d %v", code, body)
	}
	// A rename leaves the policy alone.
	tenantPolicyReq(t, svc, http.MethodPut, "/v1/tenant/settings", "globex", "admin", map[string]any{"name": "Globex"})
	if code, body := tenantPolicyReq(t, svc, http.MethodGet, "/v1/my/tenant-policy?tenant=globex", "acme", "alice", nil); code != http.StatusOK || body["requireSandboxForBorrow"] != true || fmt.Sprint(body["shareAccess"]) != "[org signed_in anyone]" || body["tenantId"] != "globex" {
		t.Fatalf("member read of another tenant: %d %v", code, body)
	}
	// The share link switches are independent of the sandbox switch, and a
	// member reads their answer (store.Tenant.ShareAccessAllowed), never
	// the switches: anonymous links off leaves signed_in; outside links off
	// leaves org only, whatever the anonymous switch says.
	for _, c := range []struct {
		set  map[string]any
		want string
	}{
		{map[string]any{"anonymousLinksDisabled": true}, "[org signed_in]"},
		{map[string]any{"outsideLinksDisabled": true}, "[org]"},
		{map[string]any{"anonymousLinksDisabled": false}, "[org]"},
		{map[string]any{"outsideLinksDisabled": false}, "[org signed_in anyone]"},
	} {
		code, body = tenantPolicyReq(t, svc, http.MethodPut, "/v1/tenant/settings", "globex", "admin", c.set)
		if code != http.StatusOK || body["requireSandboxForBorrow"] != true || body["name"] != "Globex" {
			t.Fatalf("set %v: %d %v", c.set, code, body)
		}
		for k, v := range c.set {
			if body[k] != v {
				t.Fatalf("set %v: the settings say %s=%v", c.set, k, body[k])
			}
		}
		if code, body := tenantPolicyReq(t, svc, http.MethodGet, "/v1/my/tenant-policy?tenant=globex", "acme", "alice", nil); code != http.StatusOK || fmt.Sprint(body["shareAccess"]) != c.want || body["requireSandboxForBorrow"] != true || body["anonymousLinksDisabled"] != nil {
			t.Fatalf("member read after %v: %d %v, want shareAccess %s", c.set, code, body, c.want)
		}
	}
	tenantPolicyReq(t, svc, http.MethodPut, "/v1/tenant/settings", "globex", "admin", map[string]any{"outsideLinksDisabled": true})
	if code, body := tenantPolicyReq(t, svc, http.MethodGet, "/v1/my/tenant-policy", "acme", "alice", nil); code != http.StatusOK || fmt.Sprint(body["shareAccess"]) != "[org signed_in anyone]" {
		t.Fatalf("the other tenant is unaffected: %d %v", code, body)
	}
	// The old read is gone (clean cutover).
	if code, _ := tenantPolicyReq(t, svc, http.MethodGet, "/v1/my/sandbox-policy", "acme", "alice", nil); code != http.StatusNotFound && code != http.StatusMethodNotAllowed {
		t.Fatalf("old sandbox-policy route answered %d", code)
	}
	if code, _ := tenantPolicyReq(t, svc, http.MethodGet, "/v1/my/tenant-policy?tenant=globex", "acme", "mallory", nil); code != http.StatusForbidden {
		t.Fatalf("non-member read: %d, want 403", code)
	}
	if code, _ := tenantPolicyReq(t, svc, http.MethodGet, "/v1/my/tenant-policy", "acme", "", nil); code != http.StatusUnauthorized {
		t.Fatalf("anonymous read: %d, want 401", code)
	}
}

// Publish approval (docs/prd/2026-10-13-app-gallery.md §11) is an org
// admin's setting: required by default, "none" or "required" only, kept by
// the other switches, readable by members.
func TestTenantPolicy_publishApproval(t *testing.T) {
	svc, _ := newService(t)
	tenants := store.NewMemoryTenant()
	_ = tenants.PutTenant(tctx("acme"), store.Tenant{TenantID: "acme", Name: "Acme", Status: store.TenantActive})
	_ = tenants.PutMember(tctx("acme"), store.TenantMember{TenantID: "acme", UserID: "alice"})
	svc.Tenants = tenants
	if code, body := tenantPolicyReq(t, svc, http.MethodGet, "/v1/tenant/settings", "acme", "admin", nil); code != http.StatusOK || body["publishApproval"] != "required" {
		t.Fatalf("default: %d %v", code, body)
	}
	if code, body := tenantPolicyReq(t, svc, http.MethodPut, "/v1/tenant/settings", "acme", "admin", map[string]any{"publishApproval": "sometimes"}); code != http.StatusBadRequest {
		t.Fatalf("unknown value: %d %v", code, body)
	}
	if code, body := tenantPolicyReq(t, svc, http.MethodPut, "/v1/tenant/settings", "acme", "admin", map[string]any{"publishApproval": "none"}); code != http.StatusOK || body["publishApproval"] != "none" {
		t.Fatalf("set none: %d %v", code, body)
	}
	if code, body := tenantPolicyReq(t, svc, http.MethodPut, "/v1/tenant/settings", "acme", "admin", map[string]any{"anonymousLinksDisabled": true}); code != http.StatusOK || body["publishApproval"] != "none" {
		t.Fatalf("another switch kept it: %d %v", code, body)
	}
	if code, body := tenantPolicyReq(t, svc, http.MethodGet, "/v1/my/tenant-policy", "acme", "alice", nil); code != http.StatusOK || body["publishApproval"] != "none" {
		t.Fatalf("member read: %d %v", code, body)
	}
}
