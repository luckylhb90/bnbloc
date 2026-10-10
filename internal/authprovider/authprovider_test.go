package authprovider

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/extgov"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
)

// providerServer fakes an auth-provider endpoint: health status and the
// contract its discover document advertises are configurable.
func providerServer(t *testing.T, healthStatus int, contract string) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/auth-provider/health":
			w.WriteHeader(healthStatus)
		case "/v1/auth-provider/discover":
			_ = json.NewEncoder(w).Encode(map[string]string{"name": "corp", "contract": contract})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(srv.Close)
	return srv
}

func record(t *testing.T, cfg Config) extpipe.Record {
	t.Helper()
	raw, err := json.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	return extpipe.Record{Kind: "auth-provider", Name: "corp", Config: raw, Network: cfg.Permissions.Network}
}

func validConfig(baseURL string) Config {
	return Config{
		Runtime:     pkgmeta.RuntimeEndpoint,
		Contract:    pkgmeta.AuthProviderContractV1,
		Endpoint:    &pkgmeta.Endpoint{BaseURL: baseURL + "/"},
		Permissions: pkgmeta.Permissions{Network: []string{"127.0.0.1"}},
	}
}

func guardFor(cfg Config) extgov.Guard {
	return extgov.Guard{Net: extgov.NewNetGuard(cfg.Permissions.Network)}
}

func TestProbeRecord_acceptsHealthyContractEndpoint(t *testing.T) {
	srv := providerServer(t, http.StatusOK, pkgmeta.AuthProviderContractV1)
	cfg := validConfig(srv.URL)
	if err := ProbeRecord(context.Background(), record(t, cfg), guardFor(cfg)); err != nil {
		t.Fatalf("probe: %v", err)
	}
	inst, err := ActivateRecord(context.Background(), record(t, cfg), guardFor(cfg))
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	ep := inst.(Endpoint)
	if ep.BaseURL != srv.URL || ep.AuthProviderName() != "corp" || ep.AuthProviderContract() != pkgmeta.AuthProviderContractV1 {
		t.Fatalf("endpoint = %+v; want trimmed base URL and contract", ep)
	}
	if !ep.Available(context.Background()) {
		t.Fatal("healthy endpoint reported unavailable")
	}
}

func TestProbeRecord_rejectsInvalidConfigBeforeNetwork(t *testing.T) {
	srv := providerServer(t, http.StatusOK, pkgmeta.AuthProviderContractV1)
	tests := map[string]func(*Config){
		"sidecar runtime":   func(c *Config) { c.Runtime = pkgmeta.RuntimeSidecar },
		"unknown contract":  func(c *Config) { c.Contract = "bnbloc.auth-provider.contract/v9" },
		"missing endpoint":  func(c *Config) { c.Endpoint = nil },
		"blank endpoint":    func(c *Config) { c.Endpoint = &pkgmeta.Endpoint{BaseURL: "  "} },
		"host not in allow": func(c *Config) { c.Permissions.Network = []string{"idp.example"} },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			cfg := validConfig(srv.URL)
			mutate(&cfg)
			if err := ProbeRecord(context.Background(), record(t, cfg), guardFor(cfg)); err == nil {
				t.Fatal("invalid auth-provider config accepted")
			}
			if _, err := ActivateRecord(context.Background(), record(t, cfg), guardFor(cfg)); err == nil {
				t.Fatal("invalid auth-provider config activated")
			}
		})
	}
}

func TestProbeRecord_rejectsUnhealthyOrWrongContractEndpoint(t *testing.T) {
	for name, srv := range map[string]*httptest.Server{
		"health 503":     providerServer(t, http.StatusServiceUnavailable, pkgmeta.AuthProviderContractV1),
		"wrong contract": providerServer(t, http.StatusOK, "something/else"),
	} {
		t.Run(name, func(t *testing.T) {
			cfg := validConfig(srv.URL)
			if err := ProbeRecord(context.Background(), record(t, cfg), guardFor(cfg)); err == nil {
				t.Fatal("probe accepted a broken provider")
			}
		})
	}
}

// The endpoint's HTTP client is built from the guard, so even a config that
// slipped past validation cannot reach a host outside permissions.network.
func TestNewEndpoint_clientEnforcesNetworkAllowlist(t *testing.T) {
	srv := providerServer(t, http.StatusOK, pkgmeta.AuthProviderContractV1)
	cfg := validConfig(srv.URL)
	ep := NewEndpoint(record(t, cfg), cfg, extgov.Guard{Net: extgov.NewNetGuard([]string{"idp.example"})})
	if ep.Available(context.Background()) {
		t.Fatal("endpoint reached a host outside its network allowlist")
	}
}

func TestDecodePackage_roundTripsSharedConfig(t *testing.T) {
	pkg := pkgmeta.Package{
		Kind: pkgmeta.KindAuthProviderPackageV1, Name: "corp", Version: "1.0.0",
		Runtime: pkgmeta.RuntimeEndpoint, Contract: pkgmeta.AuthProviderContractV1,
		Endpoint: &pkgmeta.Endpoint{BaseURL: "http://idp-bridge:8097"}, Permissions: pkgmeta.Permissions{Network: []string{"idp-bridge"}},
	}
	raw, backend, err := DecodePackage(pkg)
	if err != nil || backend == "" {
		t.Fatalf("decode package: %v backend=%q", err, backend)
	}
	cfg, err := DecodeRecord(extpipe.Record{Kind: "auth-provider", Name: "corp", Config: raw})
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Endpoint.BaseURL != "http://idp-bridge:8097" || cfg.Contract != pkgmeta.AuthProviderContractV1 || !strings.EqualFold(cfg.Permissions.Network[0], "idp-bridge") {
		t.Fatalf("decoded config = %+v", cfg)
	}
	if _, err := DecodeRecord(extpipe.Record{Kind: "auth-provider", Name: "corp", Config: json.RawMessage(`{"runtime":`)}); err == nil {
		t.Fatal("corrupt config decoded")
	}
}
