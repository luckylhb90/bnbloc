package pkgmeta

import (
	"encoding/json"
	"errors"
	"os"
	"slices"
	"strings"
	"testing"
)

// The contract keys a sandbox package may declare, in the fixture the
// desktop's copy reads too.
func TestSandboxSettingShapesMatchFixture(t *testing.T) {
	raw, err := os.ReadFile("testdata/sandbox_settings.json")
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Shapes map[string]struct {
			Type, Scope string
			Options     []string
		}
	}
	if err := json.Unmarshal(raw, &fixture); err != nil {
		t.Fatal(err)
	}
	if len(fixture.Shapes) != len(sandboxSettingShapes) {
		t.Fatalf("fixture has %d shapes, the contract %d", len(fixture.Shapes), len(sandboxSettingShapes))
	}
	for key, want := range sandboxSettingShapes {
		got, ok := fixture.Shapes[key]
		if !ok || got.Type != want.typ || got.Scope != want.scope || !slices.Equal(got.Options, want.options) {
			t.Errorf("%s: fixture %+v, contract %+v", key, got, want)
		}
	}
}

// A sandbox package declares its own settings; a contract key in another
// shape, or a value the kind's rule refuses as its default, is refused.
func TestSandboxPackageSettings(t *testing.T) {
	decl := []map[string]any{
		{"key": "mode", "label": "Run in the VM", "type": "choice", "scope": "member", "options": []string{"off", "borrowed", "all"}, "default": "borrowed"},
		{"key": "forwardEnv", "label": "Environment", "type": "lines", "scope": "member"},
		{"key": "privateHosts", "label": "Private hosts", "type": "lines", "scope": "member"},
		{"key": "orgHosts", "label": "Organization hosts", "type": "lines", "scope": "org"},
		{"key": "memberHosts", "label": "Members may allow hosts", "type": "choice", "scope": "org", "options": []string{"ask", "off"}, "default": "ask"},
		{"key": "cpus", "label": "CPUs", "type": "number", "scope": "member"},
	}
	pkg, err := ParsePackage(validSandboxSidecarBody(t, func(b map[string]any) { b["settings"] = decl }))
	if err != nil || len(pkg.Settings) != len(decl) {
		t.Fatalf("settings = %+v, err = %v", pkg.Settings, err)
	}
	for name, c := range map[string]struct {
		setting map[string]any
		want    string
	}{
		"wrong scope":   {map[string]any{"key": "orgHosts", "label": "Hosts", "type": "lines", "scope": "member"}, "reads this sandbox setting as lines, scope org"},
		"wrong options": {map[string]any{"key": "mode", "label": "Mode", "type": "choice", "scope": "member", "options": []string{"off", "all"}}, "options off, borrowed, all"},
		"bad default":   {map[string]any{"key": "orgHosts", "label": "Hosts", "type": "lines", "scope": "org", "default": "localhost"}, "not a host the sandbox may open"},
		"deliver":       {map[string]any{"key": "cpus", "label": "CPUs", "type": "number", "scope": "member", "deliver": map[string]any{"env": "SBX_CPUS"}}, "deliver.env"},
	} {
		raw := validSandboxSidecarBody(t, func(b map[string]any) { b["settings"] = []map[string]any{c.setting} })
		if _, err := ParsePackage(raw); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
}

func TestCheckKindSettings(t *testing.T) {
	decl := []Setting{
		{Key: "forwardEnv", Label: "Environment", Type: SettingLines, Scope: SettingScopeMember},
		{Key: "orgHosts", Label: "Organization hosts", Type: SettingLines, Scope: SettingScopeOrg},
	}
	if err := CheckKindSettings("sandbox", decl, map[string]string{"forwardEnv": "EXAMPLE_API_KEY\nEXAMPLE_*", "orgHosts": "npm.internal.example\n10.0.0.5"}); err != nil {
		t.Fatalf("valid values: %v", err)
	}
	err := CheckKindSettings("sandbox", decl, map[string]string{"orgHosts": "npm.internal.example\n169.254.169.254"})
	var refusal *SettingRefusal
	if !errors.As(err, &refusal) || refusal.Key != "orgHosts" || !strings.Contains(refusal.Reason, "169.254.169.254") {
		t.Fatalf("metadata address: %v", err)
	}
	if err := CheckKindSettings("sandbox", decl, map[string]string{"forwardEnv": "PATH"}); err == nil || !strings.Contains(err.Error(), "PATH") {
		t.Fatalf("PATH: %v", err)
	}
	if err := CheckKindSettings("plugin", decl, map[string]string{"orgHosts": "localhost"}); err != nil {
		t.Fatalf("another kind has no such rule: %v", err)
	}
}
