package gateway

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/authprovider"
	"github.com/luckylhb90/bnbloc/internal/extreg"
	"github.com/luckylhb90/bnbloc/internal/rbac"
)

// productionGateway is how a production gateway without OIDC is wired: direct
// token login is disabled (auth.DisabledVerifier); BNBLOC_SUPER_ADMINS maps an
// SSO identity's email to super_admin, as provisioning.Resolver does.
func productionGateway() *Service {
	return &Service{
		Verifier: auth.DisabledVerifier{},
		Signer:   auth.NewSigner([]byte("k")),
		TenantResolver: func(id auth.Identity, def string) (string, string, error) {
			if strings.EqualFold(id.Email, "admin@corp.example") {
				return def, string(rbac.RoleSuperAdmin), nil
			}
			return def, "member", nil
		},
	}
}

func directLogin(t *testing.T, srvURL, idp string) int {
	t.Helper()
	body, _ := json.Marshal(loginBody{IDPToken: idp})
	resp, err := http.Post(srvURL+"/v1/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	return resp.StatusCode
}

// Production + no OIDC + an installed auth-provider: the gateway serves, the
// dev/direct login is refused for every identity (incl. a super-admin's), and
// SSO through the installed provider signs the super-admin in with their role.
func TestProductionWithoutOIDCServesSSOAndRefusesDirectLogin(t *testing.T) {
	provider := http.NewServeMux()
	provider.HandleFunc("GET /v1/auth-provider/health", func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(`{"ok":true}`)) })
	provider.HandleFunc("GET /v1/auth-provider/discover", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"name":"corp-sso","contract":"bnbloc.auth-provider.contract/v1"}`))
	})
	provider.HandleFunc("POST /v1/auth-provider/acs", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"subject":"u-123","email":"admin@corp.example","relay":"/sso-callback"}`))
	})
	idp := httptest.NewServer(provider)
	defer idp.Close()
	const name = "prod-login-test-provider"
	if err := extreg.Reregister(extreg.Extension{Kind: extreg.KindAuthProvider, Name: name,
		Instance: authprovider.Endpoint{Name: name, BaseURL: idp.URL, Client: http.DefaultClient}}); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = extreg.Unregister(extreg.KindAuthProvider, name) })

	gw := productionGateway()
	srv := httptest.NewServer(gw.Handler())
	defer srv.Close()

	for _, who := range []string{"admin@corp.example", "root", "anyone"} {
		if c := directLogin(t, srv.URL, who); c != http.StatusUnauthorized {
			t.Fatalf("direct login as %q = %d, want 401 (dev login disabled in production)", who, c)
		}
	}
	resp, err := http.Get(srv.URL + "/v1/auth/sso-config")
	if err != nil {
		t.Fatal(err)
	}
	var cfg struct {
		Available bool `json:"authProviderAvailable"`
		Direct    bool `json:"directLoginAvailable"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&cfg)
	resp.Body.Close()
	if !cfg.Available || cfg.Direct {
		t.Fatalf("sso-config = %+v, want SSO offered and direct login off", cfg)
	}
	client := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err = client.PostForm(srv.URL+"/api/auth/acs", url.Values{"assertion": {"x"}})
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	loc, _ := url.Parse(resp.Header.Get("Location"))
	frag, _ := url.ParseQuery(loc.Fragment)
	claims, err := gw.Signer.Verify(frag.Get("sessionToken"))
	if resp.StatusCode != http.StatusFound || err != nil || claims.Role != string(rbac.RoleSuperAdmin) {
		t.Fatalf("SSO sign-in: status=%d err=%v claims=%+v", resp.StatusCode, err, claims)
	}
}

// Production + no OIDC + no auth-provider: nobody can sign in (fail closed);
// the first super-admin is bootstrapped out of band (mint-admin-token).
func TestProductionWithoutAnyIdentitySourceFailsClosed(t *testing.T) {
	srv := httptest.NewServer(productionGateway().Handler())
	defer srv.Close()
	if c := directLogin(t, srv.URL, "admin@corp.example"); c != http.StatusUnauthorized {
		t.Fatalf("direct login = %d, want 401", c)
	}
}
