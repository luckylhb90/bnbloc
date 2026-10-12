package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/extreg"
)

func authProviderPackageBody(t *testing.T, name string, endpointURL string) []byte {
	t.Helper()
	body := map[string]any{
		"kind":        pkgmeta.KindAuthProviderPackageV1,
		"name":        name,
		"version":     "0.1.0",
		"runtime":     pkgmeta.RuntimeEndpoint,
		"contract":    pkgmeta.AuthProviderContractV1,
		"endpoint":    map[string]any{"baseUrl": endpointURL},
		"permissions": map[string]any{"network": []string{"127.0.0.1", "localhost"}},
		"publisher":   "Acme Corp",
		"description": "auth-provider test endpoint",
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal auth-provider package: %v", err)
	}
	return raw
}

func TestAuthProviderPackageInstall_persistsGatewayContract(t *testing.T) {
	// Given
	const name = "sso-provider-test"
	svc, srv, token, key := signedPackageGatewayWithArtifactStore(t, true, true)
	authSrv := healthyAuthProviderServer(t)
	body := authProviderPackageBody(t, name, authSrv.URL)
	t.Cleanup(func() { _ = svc.corePipeline().Uninstall(t.Context(), extreg.KindAuthProvider, name, "test") })

	// When
	respBody, status := postComponent(t, srv.URL, token, body, signPackage(key.private, body))

	// Then
	if status != http.StatusOK {
		t.Fatalf("install status = %d, body=%s", status, respBody)
	}
	if got, ok := extreg.Get(extreg.KindAuthProvider, name); !ok {
		t.Fatal("auth-provider endpoint was not activated in the gateway host")
	} else if provider, ok := got.Instance.(extreg.AuthProviderHost); !ok || provider.AuthProviderContract() != pkgmeta.AuthProviderContractV1 {
		t.Fatalf("auth-provider instance = %+v, want generic contract host", got.Instance)
	}
	assertAuthProviderConfig(t, srv.URL, true, "/api/auth/init")
	components := listComponentsForAdmin(t, srv.URL, token)
	for _, component := range components {
		if component.Name != name {
			continue
		}
		if component.Kind != string(extreg.KindAuthProvider) || component.Runtime != string(pkgmeta.RuntimeEndpoint) || component.Contract != pkgmeta.AuthProviderContractV1 {
			t.Fatalf("component kind/runtime/contract = %s/%s/%s, want auth-provider/endpoint/%s", component.Kind, component.Runtime, component.Contract, pkgmeta.AuthProviderContractV1)
		}
		if component.Trust.KeyID == "" || !component.Trust.Signed {
			t.Fatalf("trust = %+v, want signed auth-provider package", component.Trust)
		}
		if component.Endpoint == nil || component.Endpoint.BaseURL != authSrv.URL {
			t.Fatalf("endpoint = %+v, want auth-provider endpoint metadata", component.Endpoint)
		}
		return
	}
	t.Fatal("auth-provider component missing from admin list")
}

func TestAuthProviderPackageInstallRefusesUnavailableEndpoint(t *testing.T) {
	// Given
	_, srv, token, key := signedPackageGatewayWithArtifactStore(t, true, true)
	body := authProviderPackageBody(t, "sso-provider-unreachable", "http://127.0.0.1:1")

	// When
	respBody, status := postComponent(t, srv.URL, token, body, signPackage(key.private, body))

	// Then
	if status != http.StatusBadRequest || !strings.Contains(string(respBody), "auth-provider health probe failed") {
		t.Fatalf("install status = %d body=%s, want health probe failure", status, respBody)
	}
	if _, ok := extreg.Get(extreg.KindAuthProvider, "sso-provider-unreachable"); ok {
		t.Fatal("unreachable auth-provider endpoint must not be registered as available")
	}
}

func TestAuthProviderConfigReportsUnavailableWhenEndpointStops(t *testing.T) {
	// Given
	const name = "sso-provider-stops"
	svc, srv, token, key := signedPackageGatewayWithArtifactStore(t, true, true)
	authSrv := healthyAuthProviderServer(t)
	body := authProviderPackageBody(t, name, authSrv.URL)
	t.Cleanup(func() { _ = svc.corePipeline().Uninstall(t.Context(), extreg.KindAuthProvider, name, "test") })
	if respBody, status := postComponent(t, srv.URL, token, body, signPackage(key.private, body)); status != http.StatusOK {
		t.Fatalf("install status = %d, body=%s", status, respBody)
	}
	assertAuthProviderConfig(t, srv.URL, true, "/api/auth/init")

	// When
	authSrv.Close()

	// Then
	assertAuthProviderConfig(t, srv.URL, false, "")
}

func TestAuthProviderConfigReportsUnavailableWithoutInstalledProvider(t *testing.T) {
	// Given
	srv := httptest.NewServer(newGateway(t, "").Handler())
	t.Cleanup(srv.Close)

	// When / Then
	assertAuthProviderConfig(t, srv.URL, false, "")
}

func assertAuthProviderConfig(t *testing.T, baseURL string, available bool, startPath string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, baseURL+"/v1/auth/sso-config", nil)
	if err != nil {
		t.Fatalf("new sso config request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("sso config: %v", err)
	}
	defer resp.Body.Close()
	var cfg struct {
		AuthProviderAvailable bool   `json:"authProviderAvailable"`
		AuthProviderStartPath string `json:"authProviderStartPath"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&cfg); err != nil {
		t.Fatalf("decode sso config: %v", err)
	}
	if cfg.AuthProviderAvailable != available || cfg.AuthProviderStartPath != startPath {
		t.Fatalf("sso config = %+v, want available=%v startPath=%q", cfg, available, startPath)
	}
}

func healthyAuthProviderServer(t *testing.T) *httptest.Server {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/auth-provider/health", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	})
	mux.HandleFunc("GET /v1/auth-provider/discover", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"name":"sso-provider-test","contract":"bnbloc.auth-provider.contract/v1"}`))
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}
