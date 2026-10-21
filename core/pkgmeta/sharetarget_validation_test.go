package pkgmeta

import (
	"encoding/json"
	"strings"
	"testing"
)

func shareTargetPackageBody(t *testing.T, mutate func(map[string]any)) []byte {
	t.Helper()
	body := map[string]any{
		"kind":        KindShareTargetPackageV1,
		"name":        "corp-share",
		"version":     "0.1.0",
		"runtime":     "endpoint",
		"contract":    ShareTargetContractV1,
		"endpoint":    map[string]any{"baseUrl": "http://share.internal:8097/share"},
		"permissions": map[string]any{"network": []string{"share.internal"}},
		"publisher":   "Acme Corp",
	}
	if mutate != nil {
		mutate(body)
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestParsePackage_acceptsShareTargetWithPathPrefix(t *testing.T) {
	pkg, err := ParsePackage(shareTargetPackageBody(t, nil))
	if err != nil {
		t.Fatalf("parse share target: %v", err)
	}
	if pkg.Endpoint.BaseURL != "http://share.internal:8097/share" {
		t.Fatalf("pkg = %+v", pkg)
	}
}

func TestParsePackage_rejectsInvalidShareTarget(t *testing.T) {
	tests := map[string]struct {
		mutate func(map[string]any)
		want   string
	}{
		"sidecar runtime":  {func(b map[string]any) { b["runtime"] = "sidecar" }, `share-target runtime must be "endpoint"`},
		"wrong contract":   {func(b map[string]any) { b["contract"] = "bnbloc.share-target.contract/v9" }, "unknown share-target contract"},
		"missing endpoint": {func(b map[string]any) { delete(b, "endpoint") }, `share-target runtime "endpoint" requires endpoint.baseUrl`},
		"no network":       {func(b map[string]any) { delete(b, "permissions") }, "permissions.network"},
		"capabilities":     {func(b map[string]any) { b["capabilities"] = []string{"groups"} }, "capabilities"},
		"member setting": {func(b map[string]any) {
			b["settings"] = []map[string]any{{"key": "a", "label": "A", "type": "text", "scope": "member"}}
		}, "an endpoint component runs beside the gateway"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := ParsePackage(shareTargetPackageBody(t, tt.mutate))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}
