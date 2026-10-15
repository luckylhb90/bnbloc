package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/authprovider"
	"github.com/luckylhb90/bnbloc/internal/extreg"
	"github.com/luckylhb90/bnbloc/internal/orgsync"
	"github.com/luckylhb90/bnbloc/internal/provisioning"
	"github.com/luckylhb90/bnbloc/internal/rbac"
	"github.com/luckylhb90/bnbloc/internal/store"
)

// orgSyncGateway wires a gateway with org sync over in-memory stores and the
// real provisioning resolver, as cmd/main.go does.
func orgSyncGateway(t *testing.T) (*Service, *store.MemoryTenant) {
	t.Helper()
	ctx := context.Background()
	tenants := store.NewMemoryTenant()
	for _, id := range []string{"acme", "labs"} {
		if err := tenants.PutTenant(ctx, store.Tenant{TenantID: id, Name: id}); err != nil {
			t.Fatal(err)
		}
	}
	gw := newGateway(t, "")
	gw.Verifier = auth.DisabledVerifier{}
	gw.TenantResolver = provisioning.New(tenants).Resolve
	gw.RefreshTokens = store.NewMemoryRefreshToken()
	gw.OrgSync = &orgsync.Syncer{
		Settings: store.NewMemoryPlatformSettings(), Tenants: tenants, Rooms: store.NewMemoryRoom(),
		State: store.NewMemoryOrgSync(), Sessions: gw.RefreshTokens, Audit: store.NewMemoryExtensionAudit(),
	}
	return gw, tenants
}

func orgSyncCall(t *testing.T, srvURL, method, path, token string, body any) (*http.Response, []byte) {
	t.Helper()
	raw, _ := json.Marshal(body)
	req, _ := http.NewRequest(method, srvURL+path, bytes.NewReader(raw))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	_, _ = buf.ReadFrom(resp.Body)
	return resp, buf.Bytes()
}

func TestOrgSyncRoutesAreSuperAdminOnlyAndValidateRules(t *testing.T) {
	gw, tenants := orgSyncGateway(t)
	srv := httptest.NewServer(gw.Handler())
	defer srv.Close()
	admin := sessionFor(gw, "root", string(rbac.RoleSuperAdmin))
	orgAdmin := sessionFor(gw, "oa", string(rbac.RoleAdmin))

	if resp, _ := orgSyncCall(t, srv.URL, http.MethodGet, "/v1/platform/org-sync/rules", orgAdmin, nil); resp.StatusCode != http.StatusForbidden {
		t.Fatalf("org_admin GET rules = %d, want 403", resp.StatusCode)
	}
	bad := map[string]any{"rules": []map[string]any{{"match": map[string]string{"group": "root"}, "tenant": "acme", "role": "super_admin"}}}
	if resp, body := orgSyncCall(t, srv.URL, http.MethodPut, "/v1/platform/org-sync/rules", admin, bad); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("super_admin rule PUT = %d %s, want 400", resp.StatusCode, body)
	}
	good := map[string]any{"rules": []map[string]any{{"match": map[string]string{"group": "eng"}, "tenant": "labs", "role": "org_admin"}}}
	if resp, body := orgSyncCall(t, srv.URL, http.MethodPut, "/v1/platform/org-sync/rules", admin, good); resp.StatusCode != http.StatusOK {
		t.Fatalf("rule PUT = %d %s", resp.StatusCode, body)
	}

	// Preview reports the effect without applying it.
	resp, body := orgSyncCall(t, srv.URL, http.MethodPost, "/v1/platform/org-sync/preview", admin,
		map[string]any{"email": "pat@corp.example", "groups": []string{"eng"}})
	var preview orgsync.Result
	_ = json.Unmarshal(body, &preview)
	if resp.StatusCode != http.StatusOK || !preview.DryRun || preview.Tenant != "labs" || len(preview.Changes) != 1 || preview.Changes[0].Action != orgsync.ActionAdd {
		t.Fatalf("preview = %d %s", resp.StatusCode, body)
	}
	if _, ok, _ := tenants.GetMember(context.Background(), "labs", "pat@corp.example"); ok {
		t.Fatal("preview applied a membership")
	}

	// Deprovision removes memberships of the listed users.
	_ = tenants.PutMember(context.Background(), store.TenantMember{TenantID: "acme", UserID: "gone", Role: store.OrgRoleMember})
	resp, body = orgSyncCall(t, srv.URL, http.MethodPost, "/v1/platform/org-sync/deprovision", admin, map[string]any{"userIds": []string{"gone"}})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("deprovision = %d %s", resp.StatusCode, body)
	}
	if _, ok, _ := tenants.GetMember(context.Background(), "acme", "gone"); ok {
		t.Fatal("deprovisioned user kept a membership")
	}
}

// An SSO assertion carrying groups lands the member in the rule's tenant
// with the rule's role, through the real ACS path.
func TestSSOSignInAppliesOrgSyncRules(t *testing.T) {
	provider := http.NewServeMux()
	provider.HandleFunc("GET /v1/auth-provider/health", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"ok":true}`)) })
	provider.HandleFunc("GET /v1/auth-provider/discover", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"name":"corp-sso","contract":"bnbloc.auth-provider.contract/v1"}`))
	})
	provider.HandleFunc("POST /v1/auth-provider/acs", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"subject":"kim@corp.example","email":"kim@corp.example","relay":"/sso-callback","groups":["Eng"],"department":"R&D","displayName":"Kim"}`))
	})
	idp := httptest.NewServer(provider)
	defer idp.Close()
	const name = "org-sync-test-provider"
	if err := extreg.Reregister(extreg.Extension{Kind: extreg.KindAuthProvider, Name: name,
		Instance: authprovider.Endpoint{Name: name, BaseURL: idp.URL, Client: http.DefaultClient}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = extreg.Unregister(extreg.KindAuthProvider, name) })

	gw, tenants := orgSyncGateway(t)
	profiles := store.NewMemoryUserProfiles()
	gw.Profiles = profiles
	if _, err := gw.OrgSync.SaveRules(context.Background(), []orgsync.Rule{
		{Match: orgsync.Match{Group: "eng"}, Tenant: "labs", Role: orgsync.RoleOrgAdmin},
	}, "root"); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(gw.Handler())
	defer srv.Close()
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := client.PostForm(srv.URL+"/api/auth/acs", url.Values{"assertion": {"x"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	loc, _ := url.Parse(resp.Header.Get("Location"))
	frag, _ := url.ParseQuery(loc.Fragment)
	claims, err := gw.Signer.Verify(frag.Get("sessionToken"))
	if resp.StatusCode != http.StatusFound || err != nil {
		t.Fatalf("SSO sign-in: status=%d err=%v", resp.StatusCode, err)
	}
	if claims.Tenant != "labs" || claims.Role != string(store.OrgRoleAdmin) {
		t.Fatalf("session scope = %s/%s, want labs/org_admin", claims.Tenant, claims.Role)
	}
	m, ok, _ := tenants.GetMember(context.Background(), "labs", "kim@corp.example")
	if !ok || m.Source != store.MemberSourceSync {
		t.Fatalf("labs membership = %+v ok=%v, want a sync row", m, ok)
	}
	// Sign-in records the email the provider asserted: an app reads its
	// caller's own (me().account).
	if email, _ := profiles.Email(context.Background(), "kim@corp.example"); email != "kim@corp.example" {
		t.Fatalf("recorded email = %q", email)
	}
}
