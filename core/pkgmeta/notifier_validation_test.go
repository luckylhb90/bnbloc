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

// A notifier may declare that it delivers to groups.
func TestParsePackage_acceptsNotifierGroups(t *testing.T) {
	pkg, err := ParsePackage(notifierPackageBody(t, func(b map[string]any) { b["capabilities"] = []string{NotifierCapabilityGroups} }))
	if err != nil || len(pkg.Capabilities) != 1 || pkg.Capabilities[0] != "groups" {
		t.Fatalf("parse = %+v, %v", pkg.Capabilities, err)
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
		// plugin (its settings), never a server-side secret.
		"member settings": {func(b map[string]any) { b["memberSettings"] = []map[string]any{{"key": "a", "label": "A"}} }, "unknown field"},
		// An endpoint component asks for its platform and organization
		// values: nothing is delivered, nothing of a member's reaches it,
		// and the one form renders its declaration.
		"member setting": {func(b map[string]any) {
			b["settings"] = []map[string]any{{"key": "a", "label": "A", "type": "text", "scope": "member"}}
		}, "an endpoint component runs beside the gateway"},
		"delivered setting": {func(b map[string]any) {
			b["settings"] = []map[string]any{{"key": "a", "label": "A", "type": "text", "scope": "platform", "deliver": map[string]any{"env": "A"}}}
		}, "an endpoint component asks for its values"},
		"settings page": {func(b map[string]any) {
			b["settings"] = []map[string]any{{"key": "a", "label": "A", "type": "text", "scope": "platform"}}
			b["settingsPage"] = map[string]any{"entry": "index.html", "bundle": map[string]any{"url": "https://cdn.example.com/p.tar.gz", "sha256": strings.Repeat("a", 64)}}
		}, "must not declare settingsPage"},
		"unknown capability": {func(b map[string]any) { b["capabilities"] = []string{"broadcast"} }, `unknown notifier capability "broadcast"`},
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
