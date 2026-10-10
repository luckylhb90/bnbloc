package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The public ACS path follows configuration (the URL registered with the IdP).
func TestPublicACSPathIsConfigurable(t *testing.T) {
	post := func(gw *Service, path string) int {
		t.Helper()
		srv := httptest.NewServer(gw.Handler())
		defer srv.Close()
		resp, err := http.Post(srv.URL+path, "application/x-www-form-urlencoded", strings.NewReader("assertion=x"))
		if err != nil {
			t.Fatalf("POST %s: %v", path, err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	// No auth-provider installed: the ACS handler answers 503; any other path
	// is not the ACS.
	def := newGateway(t, "")
	if got := post(def, "/api/auth/acs"); got != http.StatusServiceUnavailable {
		t.Fatalf("default ACS = %d, want 503 from the ACS handler", got)
	}
	custom := newGateway(t, "")
	custom.AuthACSPath = "/api/sso/acs"
	if got := post(custom, "/api/sso/acs"); got != http.StatusServiceUnavailable {
		t.Fatalf("configured ACS = %d, want 503 from the ACS handler", got)
	}
	if got := post(custom, "/api/auth/acs"); got == http.StatusServiceUnavailable {
		t.Fatal("default path still served after configuring a different ACS path")
	}
}
