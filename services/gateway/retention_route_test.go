package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/auth"
)

// Data retention authorization at the edge: the platform default is
// super-admin only (manage_platform); a tenant override needs manage_org
// and an org admin is always bound to their own tenant (?tenantId= is a
// super-admin scope only). Both route to shareagent.
func TestRetentionRoutesAuthorization(t *testing.T) {
	var gotPath, gotTenant string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotTenant = r.URL.Path, r.Header.Get("X-Bnbloc-Tenant")
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	svc := newGateway(t, "")
	svc.Backends.ShareAgent = backend.URL
	role := "member"
	svc.TenantResolver = func(auth.Identity, string) (string, string, error) { return "home", role, nil }
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()

	call := func(method, path, acting string) int {
		t.Helper()
		gotPath, gotTenant = "", ""
		token, _ := login(t, srv, "good")
		var body *strings.Reader
		if method == http.MethodPut {
			body = strings.NewReader(`{"messagesDays":90}`)
		} else {
			body = strings.NewReader("")
		}
		req, _ := http.NewRequest(method, srv.URL+path, body)
		req.Header.Set("Authorization", "Bearer "+token)
		if acting != "" {
			req.Header.Set(actingTenantHeader, acting)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}

	for _, c := range []struct {
		role, method, path string
		want               int
		tenant             string
		acting             string
	}{
		{"member", http.MethodGet, "/v1/tenant/retention", http.StatusForbidden, "", ""},
		{"member", http.MethodPut, "/v1/tenant/retention", http.StatusForbidden, "", ""},
		{"member", http.MethodGet, "/v1/platform/retention", http.StatusForbidden, "", ""},
		{"org_admin", http.MethodGet, "/v1/tenant/retention", http.StatusOK, "home", ""},
		{"org_admin", http.MethodPut, "/v1/tenant/retention", http.StatusOK, "home", "globex"},
		{"org_admin", http.MethodGet, "/v1/platform/retention", http.StatusForbidden, "", ""},
		{"org_admin", http.MethodPut, "/v1/platform/retention", http.StatusForbidden, "", ""},
		{"super_admin", http.MethodPut, "/v1/platform/retention", http.StatusOK, "home", ""},
		{"super_admin", http.MethodPut, "/v1/tenant/retention", http.StatusOK, "acme", "acme"},
	} {
		role = c.role
		if got := call(c.method, c.path, c.acting); got != c.want {
			t.Fatalf("%s %s %s = %d, want %d", c.role, c.method, c.path, got, c.want)
		}
		if c.want != http.StatusOK {
			if gotPath != "" {
				t.Fatalf("%s %s %s reached the backend", c.role, c.method, c.path)
			}
			continue
		}
		if !strings.HasSuffix(gotPath, "/retention") || gotTenant != c.tenant {
			t.Fatalf("%s %s %s: backend saw %q in tenant %q, want tenant %q", c.role, c.method, c.path, gotPath, gotTenant, c.tenant)
		}
	}
}
