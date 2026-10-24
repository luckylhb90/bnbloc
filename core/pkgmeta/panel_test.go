package pkgmeta

import (
	"strings"
	"testing"
)

// panelPlugin is a plugin with a panel and no hook: the panel is its job.
func panelPlugin(t *testing.T, mutate func(map[string]any)) []byte {
	t.Helper()
	return validPluginPackageBody(t, func(b map[string]any) {
		delete(b, "hooks")
		delete(b, "capabilities")
		b["sidecar"].(map[string]any)["tokenEnv"] = "AUDITOR_TOKEN"
		b["panel"] = settingsPageBlock("index.html")
		if mutate != nil {
			mutate(b)
		}
	})
}

func TestPanel_aPluginWithAPanelNeedsNoHook(t *testing.T) {
	pkg, err := ParsePackage(panelPlugin(t, nil))
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Panel == nil || pkg.Pins()[PanelBundle] != pkg.Panel.Bundle {
		t.Fatalf("panel = %+v, pins = %v", pkg.Panel, pkg.Pins())
	}
	if _, ok := pkg.Pins()[SettingsPageBundle]; ok {
		t.Error("a package with no settings page pins none")
	}
	// The bundle is held to the gateway artifact rule like a binary.
	pkg.Artifacts = nil
	if err := ValidateGatewayArtifactURLs(pkg, "http://gateway.test"); err != nil {
		t.Fatal(err)
	}
	if err := ValidateGatewayArtifactURLs(pkg, "http://elsewhere.test"); err == nil || !strings.Contains(err.Error(), PanelBundle+".url") {
		t.Fatalf("a bundle of another gateway: err = %v", err)
	}
}

func TestPanel_refusals(t *testing.T) {
	cases := map[string]struct {
		raw  []byte
		want string
	}{
		"no bearer":      {panelPlugin(t, func(b map[string]any) { delete(b["sidecar"].(map[string]any), "tokenEnv") }), "panel requires sidecar.tokenEnv"},
		"entry not html": {panelPlugin(t, func(b map[string]any) { b["panel"].(map[string]any)["entry"] = "index.js" }), "panel.entry"},
		"entry escapes":  {panelPlugin(t, func(b map[string]any) { b["panel"].(map[string]any)["entry"] = "../index.html" }), "panel.entry"},
		"half a pin": {panelPlugin(t, func(b map[string]any) {
			b["panel"].(map[string]any)["bundle"] = map[string]any{"url": "", "sha256": strings.Repeat("c", 64)}
		}), PanelBundle + " requires url and sha256"},
		"an unknown field": {panelPlugin(t, func(b map[string]any) { b["panel"].(map[string]any)["script"] = "x.js" }), "unknown field"},
		"not a plugin's":   {withEndpointMutation(t, mcpStdioBody(t, nil), func(b map[string]any) { b["panel"] = settingsPageBlock("index.html") }), "panel is only valid for"},
		"a previewer's":    {mustJSON(t, withPreviewerPage(func(b map[string]any) { b["panel"] = settingsPageBlock("index.html") })), "panel is only valid for"},
		"no job at all":    {validPluginPackageBody(t, func(b map[string]any) { delete(b, "hooks"); delete(b, "capabilities") }), "at least one hook"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := ParsePackage(c.raw)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("err = %v, want %q", err, c.want)
			}
		})
	}
}

// An upload placeholder parses (a draft before its bundle is uploaded) but is
// never published.
func TestPanel_placeholderIsUploadedBeforePublish(t *testing.T) {
	pkg, err := ParsePackage(panelPlugin(t, func(b map[string]any) {
		b["panel"].(map[string]any)["bundle"] = map[string]any{"url": "", "sha256": ""}
	}))
	if err != nil {
		t.Fatal(err)
	}
	if err := ValidateCompleteArtifacts(pkg); err == nil || !strings.Contains(err.Error(), PanelBundle) {
		t.Fatalf("a placeholder panel bundle was publishable: %v", err)
	}
}

// A panel and a settings page are two pages with two places among the pins.
func TestPanel_besideASettingsPage(t *testing.T) {
	pkg, err := ParsePackage(panelPlugin(t, func(b map[string]any) {
		b["settings"] = []map[string]any{{"key": "apiBase", "label": "API base", "type": "url", "scope": "org"}}
		b["settingsPage"] = settingsPageBlock("settings.html")
	}))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := pkg.Pins()[SettingsPageBundle]; !ok {
		t.Error("the settings page is pinned")
	}
	if _, ok := pkg.Pins()[PanelBundle]; !ok {
		t.Error("the panel is pinned")
	}
}
