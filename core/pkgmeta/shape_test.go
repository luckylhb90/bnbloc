package pkgmeta

import (
	"encoding/json"
	"strings"
	"testing"
)

// A field only some kinds carry is refused on every other kind (the
// field-to-kinds table), so a stray field never installs silently: hooks
// on a core, an auth-provider or a notifier were accepted before.
func TestKindOnlyFieldsRefused(t *testing.T) {
	bodies := map[string]func(t *testing.T, mutate func(map[string]any)) []byte{
		KindCorePackageV1:         validManifestPackageBody,
		KindPluginPackageV1:       validPluginPackageBody,
		KindAuthProviderPackageV1: validAuthProviderPackageBody,
		KindNotifierPackageV1:     notifierPackageBody,
		KindSandboxPackageV1:      validSandboxSidecarBody,
		KindMCPPackageV1:          mcpStdioBody,
		KindPreviewerPackageV1: func(t *testing.T, mutate func(map[string]any)) []byte {
			pkg := validPreviewerPackage()
			mutate(pkg)
			raw, err := json.Marshal(pkg)
			if err != nil {
				t.Fatal(err)
			}
			return raw
		},
	}
	stray := map[string]any{
		"contract":     "bnbloc.other.contract/v1",
		"protocol":     map[string]any{"min": "1.0", "max": "1.0"},
		"manifest":     map[string]any{"name": "x"},
		"sidecar":      map[string]any{"localUrl": "http://127.0.0.1:1"},
		"endpoint":     map[string]any{"baseUrl": "https://x.example.com"},
		"capabilities": []string{"interrupt"},
		"hooks":        []string{"chat_message.v1"},
		"settings":     []map[string]any{{"key": "token", "label": "Token", "type": "text", "scope": "member"}},
		"guest":        map[string]any{"image": "alpine:3.20"},
		"previewer":    map[string]any{"entry": "index.html"},
		"mcp":          map[string]any{"tools": []map[string]any{{"name": "t"}}},
	}
	for kind, body := range bodies {
		if _, err := ParsePackage(body(t, func(map[string]any) {})); err != nil {
			t.Fatalf("%s: the valid body fails: %v", kind, err)
		}
		for _, f := range kindFields {
			allowed := false
			for _, k := range f.kinds {
				allowed = allowed || k == kind
			}
			value, ok := stray[f.field]
			if allowed || !ok {
				continue
			}
			_, err := ParsePackage(body(t, func(b map[string]any) { b[f.field] = value }))
			if err == nil || !strings.Contains(err.Error(), f.field) {
				t.Errorf("%s with a stray %s: err = %v", kind, f.field, err)
			}
		}
		if kind != KindPluginPackageV1 {
			_, err := ParsePackage(body(t, func(b map[string]any) {
				perms, _ := b["permissions"].(map[string]any)
				if perms == nil {
					perms = map[string]any{}
				}
				perms["host"] = []string{"chat"}
				b["permissions"] = perms
			}))
			if err == nil || !strings.Contains(err.Error(), "permissions.host") {
				t.Errorf("%s with permissions.host: err = %v", kind, err)
			}
		}
	}
}

// An endpoint component bnbloc reaches through the network guard declares
// the endpoint host at install, not only at activation.
func TestGuardedEndpointRequiresNetwork(t *testing.T) {
	for kind, body := range map[string][]byte{
		KindAuthProviderPackageV1: validAuthProviderPackageBody(t, func(b map[string]any) { delete(b, "permissions") }),
		KindNotifierPackageV1:     notifierPackageBody(t, func(b map[string]any) { b["permissions"] = map[string]any{"network": []string{"other.example.com"}} }),
		KindSandboxPackageV1:      validSandboxEndpointBody(t, func(b map[string]any) { delete(b, "permissions") }),
		KindMCPPackageV1:          mcpEndpointBody(t, func(b map[string]any) { delete(b, "permissions") }),
	} {
		if _, err := ParsePackage(body); err == nil || !strings.Contains(err.Error(), "permissions.network") {
			t.Errorf("%s: err = %v", kind, err)
		}
	}
}
