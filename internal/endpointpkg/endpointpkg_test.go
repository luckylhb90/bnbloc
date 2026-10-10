package endpointpkg

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

var testKind = Kind{Label: "widget", Contract: "bnbloc.widget.contract/v1", DiscoverPath: "/discover"}

func record(t *testing.T, baseURL string, network []string) extpipe.Record {
	t.Helper()
	raw, err := json.Marshal(extpipe.PackageConfig{
		Runtime: pkgmeta.RuntimeEndpoint, Contract: testKind.Contract,
		Endpoint: &pkgmeta.Endpoint{BaseURL: baseURL}, Permissions: pkgmeta.Permissions{Network: network},
	})
	if err != nil {
		t.Fatal(err)
	}
	return extpipe.Record{Name: "w", Config: raw, Network: network}
}

func TestProbe_baseURLWithPathPrefix(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/prefix/discover" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{"name": "w", "contract": testKind.Contract})
	}))
	defer srv.Close()
	rec := record(t, srv.URL+"/prefix/", []string{"127.0.0.1"})
	guard := extgov.Guard{Net: extgov.NewNetGuard(rec.Network)}
	inst, err := testKind.Activate(context.Background(), rec, guard)
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	if ep := inst.(Endpoint); ep.BaseURL != srv.URL+"/prefix" {
		t.Fatalf("base url = %q", ep.BaseURL)
	}
}

func TestValidate_hostOutsideNetworkAllowlist(t *testing.T) {
	rec := record(t, "http://elsewhere.internal/prefix", []string{"allowed.internal"})
	err := testKind.Probe(context.Background(), rec, extgov.Guard{Net: extgov.NewNetGuard(rec.Network)})
	if err == nil || !strings.Contains(err.Error(), "widget endpoint host is not in permissions.network") {
		t.Fatalf("error = %v", err)
	}
}
