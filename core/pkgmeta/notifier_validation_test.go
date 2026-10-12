package pkgmeta

import (
	"encoding/json"
	"strings"
	"testing"
)

func notifierPackageBody(t *testing.T, mutate func(map[string]any)) []byte {
	t.Helper()
	body := map[string]any{
		"kind":        KindNotifierPackageV1,
		"name":        "corp-notify",
		"version":     "0.1.0",
		"runtime":     "endpoint",
		"contract":    NotifierContractV1,
		"endpoint":    map[string]any{"baseUrl": "http://notify.internal:8097/notifier"},
		"permissions": map[string]any{"network": []string{"notify.internal"}},
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

func TestParsePackage_acceptsNotifierWithPathPrefix(t *testing.T) {
	pkg, err := ParsePackage(notifierPackageBody(t, nil))
	if err != nil {
		t.Fatalf("parse notifier: %v", err)
	}
	if pkg.Endpoint.BaseURL != "http://notify.internal:8097/notifier" {
		t.Fatalf("pkg = %+v", pkg)
	}
}

func TestParsePackage_rejectsInvalidNotifier(t *testing.T) {
	tests := map[string]struct {
		mutate func(map[string]any)
		want   string
	}{
		"sidecar runtime":  {func(b map[string]any) { b["runtime"] = "sidecar" }, `notifier runtime must be "endpoint"`},
		"wrong contract":   {func(b map[string]any) { b["contract"] = "bnbloc.notifier.contract/v9" }, "unknown notifier contract"},
		"missing endpoint": {func(b map[string]any) { delete(b, "endpoint") }, `notifier runtime "endpoint" requires endpoint.baseUrl`},
		// Notifiers carry no member settings: personal delivery is a desktop
		// plugin (localSettings), never a server-side secret.
		"member settings": {func(b map[string]any) { b["memberSettings"] = []map[string]any{{"key": "a", "label": "A"}} }, "unknown field"},
		"local settings":  {func(b map[string]any) { b["localSettings"] = []map[string]any{{"key": "a", "label": "A"}} }, "localSettings is only valid"},
		"host permission": {func(b map[string]any) {
			b["permissions"] = map[string]any{"network": []string{"notify.internal"}, "host": []string{"chat"}}
		}, "permissions.host is only valid"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if _, err := ParsePackage(notifierPackageBody(t, tt.mutate)); err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("error = %v; want %q", err, tt.want)
			}
		})
	}
}
