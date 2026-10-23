package pkgmeta

import (
	"encoding/json"
	"strings"
	"testing"
)

func servicePackageBody(t *testing.T, mutate func(map[string]any)) []byte {
	t.Helper()
	body := map[string]any{
		"kind":         KindServicePackageV1,
		"name":         "connections",
		"version":      "0.1.0",
		"runtime":      "endpoint",
		"contract":     ServiceContractV1,
		"capabilities": []string{"turnProxy", "egress", "roomFacts", "pages", "callbacks", "store"},
		"endpoint":     map[string]any{"baseUrl": "http://connections.internal:8098"},
		"permissions":  map[string]any{"network": []string{"connections.internal"}},
		"settings":     []map[string]any{{"key": "a", "label": "A", "type": "text", "scope": "platform"}},
		"publisher":    "Acme Corp",
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

func TestParsePackage_acceptsService(t *testing.T) {
	pkg, err := ParsePackage(servicePackageBody(t, nil))
	if err != nil {
		t.Fatalf("parse service: %v", err)
	}
	if len(pkg.Capabilities) != 6 || !AsksForSettings(pkg.Kind) {
		t.Fatalf("pkg = %+v", pkg)
	}
}

func TestParsePackage_rejectsInvalidService(t *testing.T) {
	tests := map[string]struct {
		mutate func(map[string]any)
		want   string
	}{
		"no capabilities":      {func(b map[string]any) { delete(b, "capabilities") }, "declares the primitives"},
		"unknown capability":   {func(b map[string]any) { b["capabilities"] = []string{"turnProxy", "shell"} }, `unknown service capability "shell"`},
		"duplicate capability": {func(b map[string]any) { b["capabilities"] = []string{"pages", "pages"} }, "duplicate"},
		"egress alone":         {func(b map[string]any) { b["capabilities"] = []string{"egress"} }, `"egress" needs "turnProxy"`},
		"wrong contract":       {func(b map[string]any) { b["contract"] = "bnbloc.service.contract/v9" }, "unknown service contract"},
		"sidecar runtime":      {func(b map[string]any) { b["runtime"] = "sidecar" }, `service runtime must be "endpoint"`},
		"no network":           {func(b map[string]any) { delete(b, "permissions") }, "permissions.network"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := ParsePackage(servicePackageBody(t, tt.mutate))
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want %q", err, tt.want)
			}
		})
	}
}
