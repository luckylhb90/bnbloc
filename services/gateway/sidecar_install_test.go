package gateway

import (
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/core/manifestcore"
	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/core/protocol"
	"github.com/luckylhb90/bnbloc/internal/extreg"
)

func minimalManifest(name string) map[string]any {
	verb := func(path string) map[string]any {
		return map[string]any{"method": "POST", "path": path}
	}
	getVerb := func(path string) map[string]any {
		return map[string]any{"method": "GET", "path": path}
	}
	return map[string]any{
		"name":            name,
		"createWorkspace": verb("/v1/workspaces"),
		"createSession":   verb("/v1/workspaces/{workspaceId}/sessions"),
		"sendInput":       verb("/v1/workspaces/{workspaceId}/sessions/{sessionId}/input"),
		"listTargets":     getVerb("/v1/targets"),
		"messages":        map[string]any{"method": "GET", "path": "/v1/workspaces/{workspaceId}/sessions/{sessionId}/messages", "items": map[string]any{"list": "messages", "fields": map[string]any{"text": "text"}}},
		"pollTurn":        getVerb("/v1/workspaces/{workspaceId}/sessions/{sessionId}/turn"),
	}
}

func sidecarPackageBody(t *testing.T, name string) []byte {
	t.Helper()
	return sidecarPackageBodyWithArtifact(t, name, pkgmeta.Artifact{URL: "https://gateway.test/v1/artifacts/sha256/" + missingHex, SHA256: missingHex})
}

func sidecarPackageBodyWithArtifact(t *testing.T, name string, artifact pkgmeta.Artifact) []byte {
	t.Helper()
	body := map[string]any{
		"kind":     pkgmeta.KindCorePackageV1,
		"name":     name,
		"version":  "0.2.0",
		"protocol": protocol.ProtocolRange{Min: protocol.SupportedMinVersion, Max: protocol.SupportedMaxVersion},
		"runtime":  "sidecar",
		"artifacts": map[string]any{
			"darwin-arm64": map[string]string{
				"url":    artifact.URL,
				"sha256": artifact.SHA256,
			},
		},
		"manifest":    minimalManifest(name),
		"sidecar":     map[string]string{"localUrl": "http://127.0.0.1:7099", "listenEnv": "BNBLOC_TEST_SIDECAR_LISTEN"},
		"permissions": map[string]any{"network": []string{"127.0.0.1"}},
		"publisher":   "Acme Corp",
		"description": "sidecar test core",
	}
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal sidecar package: %v", err)
	}
	return raw
}

func fetchCores(t *testing.T, baseURL, token string) map[string]struct {
	available bool
	backend   string
} {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, baseURL+"/v1/cores", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("cores: %v", err)
	}
	defer resp.Body.Close()
	var out struct {
		Cores []struct {
			Name      string `json:"name"`
			Available bool   `json:"available"`
			Backend   string `json:"backend"`
		} `json:"cores"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode cores: %v", err)
	}
	m := map[string]struct {
		available bool
		backend   string
	}{}
	for _, c := range out.Cores {
		m[c.Name] = struct {
			available bool
			backend   string
		}{c.Available, c.Backend}
	}
	return m
}

func TestSidecarArtifactIncludesDriveManifest(t *testing.T) {
	const name = "sidecar-artifact-manifest-test"
	_, srv, token, key := signedPackageGatewayWithArtifactStore(t, true, true)
	artifact := uploadComponentArtifact(t, srv.URL, token, []byte("manifest sidecar bytes"))
	body := sidecarPackageBodyWithArtifact(t, name, artifact)
	if respBody, status := postComponent(t, srv.URL, token, body, signPackage(key.private, body)); status != http.StatusOK {
		t.Fatalf("install status = %d, body=%s", status, respBody)
	}
	t.Cleanup(func() { _ = core.Unregister(name) })

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/cores/"+name+"/artifact?platform=darwin-arm64", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("artifact: %v", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read artifact: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("artifact status = %d body=%s", resp.StatusCode, raw)
	}
	var out struct {
		Manifest struct {
			Name string `json:"name"`
		} `json:"manifest"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode artifact: %v", err)
	}
	if out.Manifest.Name != name {
		t.Fatalf("artifact manifest name = %q, want %q", out.Manifest.Name, name)
	}
}

func TestSidecarPackageInstall_listsButGatewayNeverDrives(t *testing.T) {
	const name = "sidecar-core-test"
	_, srv, token, key := signedPackageGatewayWithArtifactStore(t, true, true)
	artifact := uploadComponentArtifact(t, srv.URL, token, []byte("listed sidecar bytes"))
	body := sidecarPackageBodyWithArtifact(t, name, artifact)
	respBody, status := postComponent(t, srv.URL, token, body, signPackage(key.private, body))
	if status != http.StatusOK {
		t.Fatalf("install status = %d, body=%s", status, respBody)
	}
	t.Cleanup(func() { _ = core.Unregister(name) })

	cores := fetchCores(t, srv.URL, token)
	got, ok := cores[name]
	if !ok {
		t.Fatalf("sidecar core %q not listed in /v1/cores", name)
	}
	if got.backend != string(core.BackendSidecar) {
		t.Fatalf("backend = %q, want %q", got.backend, core.BackendSidecar)
	}
	if got.available {
		t.Fatalf("sidecar core marked available; the gateway has no endpoint for it (client drives it)")
	}

	if _, err := core.New(name); err == nil {
		t.Fatalf("core.New(%q) succeeded; the gateway must never construct a sidecar provider", name)
	}
}

// A protocol sidecar self-describes via /core/v1/discover, so a package
// without a drive manifest must install (the desktop drives it by URL through
// protocol.NewProvider) and round-trip through rehydrate with no manifest.
func TestSidecarPackageInstall_acceptsMissingManifest(t *testing.T) {
	const name = "sidecar-no-manifest"
	svc, srv, token, key := signedPackageGatewayWithArtifactStore(t, true, true)
	artifact := uploadComponentArtifact(t, srv.URL, token, []byte("manifestless sidecar bytes"))
	body := sidecarPackageBodyWithArtifact(t, name, artifact)
	var pkg map[string]any
	if err := json.Unmarshal(body, &pkg); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	delete(pkg, "manifest")
	stripped, _ := json.Marshal(pkg)
	respBody, status := postComponent(t, srv.URL, token, stripped, signPackage(key.private, stripped))
	if status != http.StatusOK {
		t.Fatalf("install without manifest rejected, want accepted (protocol sidecar); body=%s", respBody)
	}
	t.Cleanup(func() { _ = core.Unregister(name) })

	pkgs, err := svc.listInstalledComponents(t.Context())
	if err != nil {
		t.Fatalf("list installed: %v", err)
	}
	for _, p := range pkgs {
		if p.Name != name {
			continue
		}
		if p.Manifest != nil {
			t.Fatalf("manifest invented for a manifest-less package: %+v", p.Manifest)
		}
		if p.Sidecar == nil || p.Sidecar.LocalURL == "" {
			t.Fatalf("sidecar launch contract lost: %+v", p.Sidecar)
		}
		return
	}
	t.Fatalf("installed manifest-less sidecar package %q missing", name)
}

func TestSidecarPackageRehydratesFromStore(t *testing.T) {
	const name = "sidecar-rehydrate-test"
	svc, srv, token, key := signedPackageGatewayWithArtifactStore(t, true, true)
	artifact := uploadComponentArtifact(t, srv.URL, token, []byte("rehydrated sidecar bytes"))
	body := sidecarPackageBodyWithArtifact(t, name, artifact)
	if _, status := postComponent(t, srv.URL, token, body, signPackage(key.private, body)); status != http.StatusOK {
		t.Fatalf("install status = %d", status)
	}
	t.Cleanup(func() { _ = core.Unregister(name) })

	_ = core.Unregister(name)
	if _, ok := core.Describe(name); ok {
		t.Fatalf("core %q still registered after unregister; cannot prove rehydrate", name)
	}

	RehydrateInstalledComponents(t.Context(), svc.corePipeline(), extreg.ServiceGateway)
	if svc.coreByName(name) != nil {
		t.Fatalf("sidecar core %q activated into the gateway's core list; the gateway must not drive it (client does)", name)
	}
	d, ok := core.Describe(name)
	if !ok {
		t.Fatalf("sidecar core %q not in registry after rehydrate", name)
	}
	if d.Backend != core.BackendSidecar {
		t.Fatalf("rehydrated sidecar backend = %s, want sidecar", d.Backend)
	}
	pkgs, err := svc.listInstalledComponents(t.Context())
	if err != nil {
		t.Fatalf("list installed: %v", err)
	}
	for _, pkg := range pkgs {
		if pkg.Name != name {
			continue
		}
		if pkg.Sidecar == nil || pkg.Sidecar.LocalURL != "http://127.0.0.1:7099" || pkg.Sidecar.ListenEnv != "BNBLOC_TEST_SIDECAR_LISTEN" {
			t.Fatalf("rehydrated sidecar launch contract lost: %+v", pkg.Sidecar)
		}
		if m, ok := pkg.Manifest.(*manifestcore.Manifest); !ok || m.Name != name {
			t.Fatalf("rehydrated sidecar manifest lost: %+v", pkg.Manifest)
		}
		return
	}
	t.Fatalf("installed sidecar package %q missing after rehydrate", name)
}
