package main

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
)

// The example settings page every settings page test uses.
const settingsPageDir = "../../core/pkgmeta/testdata/settings-page"

const mcpEndpointWithPage = `{"name":"search","version":"2.0.0","publisher":"acme","runtime":"endpoint",
 "endpoint":{"baseUrl":"https://tools.example.com/mcp"},"permissions":{"network":["tools.example.com"]},
 "mcp":{"tools":[{"name":"search"}],"http":{"auth":{"scheme":"bearer","setting":"token"}}},
 "settings":[{"key":"token","label":"API token","type":"secret","scope":"org","required":true}],
 "settingsPage":{"entry":"index.html","bundle":{"url":"","sha256":""}}}`

// --settings-page packs a built directory (or takes an archive), uploads it
// and pins it in settingsPage.bundle, for an MCP tool and a package file.
func TestPublishWithSettingsPage(t *testing.T) {
	isolateHome(t)
	gw := newPublishGateway(t)
	raw, err := tarbundle.Pack(settingsPageDir, tarbundle.PreviewerLimits)
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(raw)
	want := hex.EncodeToString(sum[:])
	archive := writeTemp(t, "page.tar", string(raw))

	for name, src := range map[string]string{"a directory": settingsPageDir, "an archive": archive} {
		gw.published = nil
		pkgFile := writeTemp(t, "mcp.json", mcpEndpointWithPage)
		if err := coresPublishMCP([]string{"--gateway", gw.URL, "--token", "admin", "--package", pkgFile, "--settings-page", src, "--unsigned"}); err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		pkg, err := pkgmeta.ParsePackage([]byte(gw.published["package"]))
		if err != nil || pkg.SettingsPage == nil || pkg.SettingsPage.Bundle.SHA256 != want || pkg.SettingsPage.Bundle.URL != gw.URL+"/v1/artifacts/sha256/"+want {
			t.Fatalf("%s: page = %+v err = %v", name, pkg.SettingsPage, err)
		}
	}

	// A plugin's package file, beside its binaries.
	gw.published = nil
	plugin := writeTemp(t, "plugin.json", `{"kind":"bnbloc.plugin.package/v1","name":"notes","version":"1.0.0","publisher":"acme",
 "runtime":"sidecar","contract":"bnbloc.plugin.contract/v1","hooks":["chat_message.v1"],
 "sidecar":{"localUrl":"http://127.0.0.1:7199","listenEnv":"BNBLOC_PLUGIN_LISTEN"},
 "settings":[{"key":"folder","label":"Folder","type":"path","scope":"member"}],
 "settingsPage":{"entry":"index.html","bundle":{"url":"","sha256":""}}}`)
	bin := writeTemp(t, "notes", "notes binary")
	if err := componentsPublishPackage([]string{"--gateway", gw.URL, "--token", "admin", "--package", plugin, "--binary", "darwin-arm64=" + bin, "--settings-page", settingsPageDir, "--unsigned"}); err != nil {
		t.Fatal(err)
	}
	if pkg, err := pkgmeta.ParsePackage([]byte(gw.published["package"])); err != nil || pkg.SettingsPage.Bundle.SHA256 != want || len(pkg.Artifacts) != 1 {
		t.Fatalf("plugin = %+v err = %v", pkg, err)
	}
}

// A page that cannot install is refused before any byte is uploaded.
func TestPublishWithSettingsPageRefusesBeforeUpload(t *testing.T) {
	isolateHome(t)
	gw := newPublishGateway(t)
	declared := writeTemp(t, "mcp.json", mcpEndpointWithPage)
	undeclared := writeTemp(t, "plain.json", strings.Replace(mcpEndpointWithPage, `,
 "settingsPage":{"entry":"index.html","bundle":{"url":"","sha256":""}}`, "", 1))
	otherEntry := writeTemp(t, "other.json", strings.Replace(mcpEndpointWithPage, `"entry":"index.html"`, `"entry":"settings.html"`, 1))
	notArchive := writeTemp(t, "page.tar", "<!doctype html> not an archive")
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"page not passed":   {[]string{"--package", declared}, "pass its bundle with --settings-page"},
		"page not declared": {[]string{"--package", undeclared, "--settings-page", settingsPageDir}, "needs the package's settingsPage"},
		"entry missing":     {[]string{"--package", otherEntry, "--settings-page", settingsPageDir}, "settings.html"},
		"not an archive":    {[]string{"--package", declared, "--settings-page", notArchive}, "--settings-page"},
		"no such page":      {[]string{"--package", declared, "--settings-page", "/nope"}, "--settings-page"},
	} {
		args := append([]string{"--gateway", gw.URL, "--token", "admin", "--unsigned"}, c.args...)
		if err := coresPublishMCP(args); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
	if gw.published != nil || gw.uploads != 0 {
		t.Fatalf("published or uploaded despite refused input: %v %d", gw.published, gw.uploads)
	}
}
