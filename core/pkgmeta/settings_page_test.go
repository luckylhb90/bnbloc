package pkgmeta

import (
	"strings"
	"testing"
)

// settingsPageBlock is a settings page pinned at the gateway the
// artifact tests publish to.
func settingsPageBlock(entry string) map[string]any {
	sha := strings.Repeat("c", 64)
	return map[string]any{"entry": entry, "bundle": map[string]any{"url": "http://gateway.test/v1/artifacts/sha256/" + sha, "sha256": sha}}
}

func TestSettingsPage_acceptedOnEveryKindThatDeclaresSettings(t *testing.T) {
	bodies := map[string][]byte{
		"plugin": pluginWithSettings(t, map[string]any{"key": "apiBase", "label": "API base", "type": "url", "scope": "org"}),
		"stdio":  mcpStdioBody(t, nil),
		"http":   mcpEndpointBody(t, nil),
	}
	for name, raw := range bodies {
		t.Run(name, func(t *testing.T) {
			pkg, err := ParsePackage(withEndpointMutation(t, raw, func(b map[string]any) { b["settingsPage"] = settingsPageBlock("pages/settings.html") }))
			if err != nil {
				t.Fatal(err)
			}
			if pkg.SettingsPage.Entry != "pages/settings.html" || pkg.Pins()[SettingsPageBundle] != pkg.SettingsPage.Bundle {
				t.Fatalf("settings page = %+v, pins = %v", pkg.SettingsPage, pkg.Pins())
			}
			// The bundle is held to the gateway artifact rule like a binary.
			pkg.Artifacts = nil
			if err := ValidateGatewayArtifactURLs(pkg, "http://gateway.test"); err != nil {
				t.Fatal(err)
			}
			if err := ValidateGatewayArtifactURLs(pkg, "http://elsewhere.test"); err == nil || !strings.Contains(err.Error(), SettingsPageBundle+".url") {
				t.Fatalf("a bundle of another gateway: err = %v", err)
			}
		})
	}
}

func TestSettingsPage_refusals(t *testing.T) {
	page := func(mutate func(map[string]any)) func(map[string]any) {
		return func(b map[string]any) {
			p := settingsPageBlock("index.html")
			if mutate != nil {
				mutate(p)
			}
			b["settingsPage"] = p
		}
	}
	cases := map[string]struct {
		raw  []byte
		want string
	}{
		"no settings":       {withEndpointMutation(t, validPluginPackageBody(t, nil), page(nil)), "settingsPage needs settings"},
		"a previewer's":     {mustJSON(t, withPreviewerPage(page(nil))), "must not declare settingsPage"},
		"entry not html":    {withEndpointMutation(t, mcpStdioBody(t, nil), page(func(p map[string]any) { p["entry"] = "index.js" })), "settingsPage.entry"},
		"entry escapes":     {withEndpointMutation(t, mcpStdioBody(t, nil), page(func(p map[string]any) { p["entry"] = "../index.html" })), "settingsPage.entry"},
		"entry absolute":    {withEndpointMutation(t, mcpStdioBody(t, nil), page(func(p map[string]any) { p["entry"] = "/index.html" })), "settingsPage.entry"},
		"no entry":          {withEndpointMutation(t, mcpStdioBody(t, nil), page(func(p map[string]any) { delete(p, "entry") })), "settingsPage.entry"},
		"half a pin":        {withEndpointMutation(t, mcpStdioBody(t, nil), page(func(p map[string]any) { p["bundle"] = map[string]any{"url": "", "sha256": strings.Repeat("c", 64)} })), SettingsPageBundle + " requires url and sha256"},
		"upper-case digest": {withEndpointMutation(t, mcpStdioBody(t, nil), page(func(p map[string]any) { p["bundle"].(map[string]any)["sha256"] = strings.Repeat("C", 64) })), SettingsPageBundle + ".sha256"},
		"an unknown field":  {withEndpointMutation(t, mcpStdioBody(t, nil), page(func(p map[string]any) { p["script"] = "x.js" })), "unknown field"},
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

// An upload placeholder parses (a draft before its bundle is uploaded) but
// is never published, whatever the runtime.
func TestSettingsPage_placeholderIsUploadedBeforePublish(t *testing.T) {
	for name, raw := range map[string][]byte{"stdio": mcpStdioBody(t, nil), "http": mcpEndpointBody(t, nil)} {
		t.Run(name, func(t *testing.T) {
			pkg, err := ParsePackage(withEndpointMutation(t, raw, func(b map[string]any) {
				b["settingsPage"] = map[string]any{"entry": "index.html", "bundle": map[string]any{"url": "", "sha256": ""}}
			}))
			if err != nil {
				t.Fatal(err)
			}
			if err := ValidateCompleteArtifacts(pkg); err == nil || !strings.Contains(err.Error(), SettingsPageBundle+" must be uploaded") {
				t.Fatalf("err = %v", err)
			}
		})
	}
}

func withPreviewerPage(mutate func(map[string]any)) map[string]any {
	pkg := validPreviewerPackage()
	mutate(pkg)
	return pkg
}
