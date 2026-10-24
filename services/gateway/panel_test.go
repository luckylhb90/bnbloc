package gateway

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// panelPlugin is a plugin whose job is its own panel: no hook, no capability.
func panelPlugin(t *testing.T, binary, page pkgmeta.Artifact, entry string) []byte {
	t.Helper()
	raw, err := json.Marshal(pkgmeta.Package{
		Kind: pkgmeta.KindPluginPackageV1, Name: "panelled", Version: "1.0.0", Publisher: "acme",
		Contract: pkgmeta.PluginContractV1, Runtime: pkgmeta.RuntimeSidecar,
		Artifacts: map[string]pkgmeta.Artifact{"darwin-arm64": binary},
		Sidecar:   &pkgmeta.Sidecar{LocalURL: "http://127.0.0.1:7195", ListenEnv: "PANELLED_LISTEN", TokenEnv: "PANELLED_TOKEN"},
		Panel:     &pkgmeta.Page{Entry: entry, Bundle: page},
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// A panel's bundle is held to the install gate like any page: uploaded, an
// archive within the bundle limits, holding its entry.
func TestPanel_installProbesTheBundle(t *testing.T) {
	_, srv, tokens := settingsPageGateway(t)
	binary := uploadComponentArtifact(t, srv.URL, tokens["admin"], []byte("plugin binary"))
	page := uploadComponentArtifact(t, srv.URL, tokens["admin"], settingsPageBundle(t))
	notArchive := uploadComponentArtifact(t, srv.URL, tokens["admin"], []byte("<!doctype html> not an archive"))
	for name, c := range map[string]struct {
		page  pkgmeta.Artifact
		entry string
		want  string
	}{
		"placeholder":   {pkgmeta.Artifact{}, "index.html", pkgmeta.PanelBundle + " must be uploaded"},
		"not a bundle":  {notArchive, "index.html", pkgmeta.PanelBundle},
		"missing entry": {page, "panel.html", "panel.html"},
	} {
		t.Run(name, func(t *testing.T) {
			raw, status := postComponent(t, srv.URL, tokens["admin"], panelPlugin(t, binary, c.page, c.entry), "")
			if status != http.StatusBadRequest || !strings.Contains(errorMessage(raw), c.want) {
				t.Fatalf("install = %d %s, want 400 %q", status, raw, c.want)
			}
		})
	}
}

// An installed panel is listed with its component in the member's catalog,
// its bundle may be downloaded with the member's session (the desktop serves
// it by its pin), and the plugin's binaries are not part of that.
func TestPanel_listedAndDownloadableByTheMember(t *testing.T) {
	_, srv, tokens := settingsPageGateway(t)
	binary := uploadComponentArtifact(t, srv.URL, tokens["admin"], []byte("plugin binary"))
	page := uploadComponentArtifact(t, srv.URL, tokens["admin"], settingsPageBundle(t))
	if raw, status := postComponentWithRollout(t, srv.URL, tokens["admin"], panelPlugin(t, binary, page, "index.html"), "", "optin"); status != http.StatusOK {
		t.Fatalf("install = %d %s", status, raw)
	}
	_, raw := call(t, http.MethodGet, srv.URL+"/v1/components?kind=plugin", tokens["member"], nil)
	var catalog wire.ComponentsReply
	if err := json.Unmarshal(raw, &catalog); err != nil || len(catalog.Components) != 1 || catalog.Components[0].Panel == nil ||
		catalog.Components[0].Panel.Bundle != page || catalog.Components[0].Panel.Entry != "index.html" {
		t.Fatalf("member catalog = %s", raw)
	}
	if got := getArtifactStatus(t, srv, tokens["member"], page); got != http.StatusOK {
		t.Fatalf("member bundle download = %d", got)
	}
	if got := getArtifactStatus(t, srv, tokens["member"], binary); got == http.StatusOK {
		t.Fatalf("a member who has not opted in may not download the plugin binary")
	}
}
