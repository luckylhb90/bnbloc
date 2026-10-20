package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
)

// contentPublishGateway stores uploads by key and records every install.
type contentPublishGateway struct {
	*httptest.Server
	blobs    map[string][]byte
	installs []map[string]string
}

func newContentPublishGateway(t *testing.T) *contentPublishGateway {
	t.Helper()
	g := &contentPublishGateway{blobs: map[string][]byte{}}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "POST /v1/platform/artifacts":
			raw, _ := io.ReadAll(r.Body)
			sum := sha256.Sum256(raw)
			sha := hex.EncodeToString(sum[:])
			g.blobs["sha256/"+sha] = raw
			_ = json.NewEncoder(w).Encode(map[string]any{"key": "sha256/" + sha, "sha256": sha, "size": len(raw)})
		case "POST /v1/platform/components":
			var req map[string]string
			_ = json.NewDecoder(r.Body).Decode(&req)
			g.installs = append(g.installs, req)
			_, _ = w.Write([]byte(`{}`))
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(g.Close)
	return g
}

// writePackage writes a package directory under root.
func writePackage(t *testing.T, root, name string, files map[string]string) string {
	t.Helper()
	dir := filepath.Join(root, name)
	for rel, body := range files {
		p := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const (
	starterPack  = `{"kind":"bnbloc.prompt.package/v1","name":"starters","version":"1.1.0","everyRoom":true,"prompts":[{"id":"plan","title":"Plan","body":"Plan a launch.","starter":true}]}`
	enhanceTask  = `{"kind":"bnbloc.assist.package/v1","name":"enhance-prompt","version":"1.1.0","output":"text"}`
	brokenAssist = `{"kind":"bnbloc.assist.package/v1","name":"find-a-way","version":"1.1.0","output":"text"}`
)

// publish --all packs every package directory with the platform's packer
// (junk never packed), validates each as a save would, uploads its bundle,
// pins the bundle in the platform package, signs it and installs it with
// the rollout; a package that fails (an assist missing its contract)
// stops the publish before anything is uploaded.
func TestContentPublishFromDirectories(t *testing.T) {
	home := isolateHome(t)
	if err := keysGen([]string{"--out", filepath.Join(home, ".bnbloc", "component-publisher")}); err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	writePackage(t, root, "starters", map[string]string{"component.json": starterPack, ".DS_Store": "junk"})
	writePackage(t, root, "enhance-prompt", map[string]string{"component.json": enhanceTask, "INSTRUCTIONS.md": "Improve the request.\n"})
	if err := os.MkdirAll(filepath.Join(root, "notes"), 0o755); err != nil { // no component.json: not a package
		t.Fatal(err)
	}
	g := newContentPublishGateway(t)
	if err := contentPublish([]string{"--all", root, "--gateway", g.URL, "--token", "admin", "--rollout", "optin"}); err == nil {
		t.Fatal("an optin rollout was accepted")
	}
	if err := contentPublish([]string{"--all", root, "--gateway", g.URL, "--token", "admin", "--rollout", "auto", "--publisher", "acme"}); err != nil {
		t.Fatal(err)
	}
	if len(g.installs) != 2 || len(g.blobs) != 2 {
		t.Fatalf("installs = %d, uploads = %d", len(g.installs), len(g.blobs))
	}
	pub, _ := os.ReadFile(filepath.Join(home, ".bnbloc", "component-publisher.pub"))
	lines := strings.Split(strings.TrimSpace(string(pub)), "\n")
	keyID, _, err := pkgmeta.DecodeMinisignPublicKey(lines[len(lines)-1])
	if err != nil {
		t.Fatal(err)
	}
	trusted := []pkgmeta.TrustedKey{{KeyID: keyID, PublicKey: lines[len(lines)-1]}}
	names := []string{}
	for _, in := range g.installs {
		verified, err := pkgmeta.VerifySignedPackage([]byte(in["package"]), []byte(in["signature"]), trusted)
		pkg := verified.Package
		if err != nil {
			t.Fatalf("signature: %v", err)
		}
		if in["rollout"] != "auto" || pkg.Publisher != "acme" || pkg.Runtime != pkgmeta.RuntimeBundle {
			t.Fatalf("install = %+v (rollout %q)", pkg, in["rollout"])
		}
		a := pkg.Artifacts[contentpkg.PlatformBundleArtifact]
		raw, ok := g.blobs["sha256/"+a.SHA256]
		if !ok || !strings.HasPrefix(a.URL, g.URL+"/v1/artifacts/sha256/") {
			t.Fatalf("the package pins %+v, which was not uploaded", a)
		}
		b, err := tarbundle.Parse(raw, a.SHA256, contentpkg.BundleLimits())
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range b.Paths() {
			if strings.Contains(p, ".DS_Store") {
				t.Fatalf("%s packed junk: %v", pkg.Name, b.Paths())
			}
		}
		names = append(names, pkg.Name+"@"+pkg.Version)
	}
	if strings.Join(names, ",") != "enhance-prompt@1.1.0,starters@1.1.0" {
		t.Fatalf("published %v", names)
	}

	// The same bytes again (a rerun) are the same bundle.
	first := g.installs[1]["package"]
	if err := contentPublish([]string{"--dir", filepath.Join(root, "starters"), "--gateway", g.URL, "--token", "admin", "--rollout", "auto", "--publisher", "acme"}); err != nil {
		t.Fatal(err)
	}
	if again := g.installs[2]["package"]; again != first {
		t.Fatalf("a rerun pins another bundle:\n%s\n%s", first, again)
	}

	writePackage(t, root, "find-a-way", map[string]string{"component.json": brokenAssist, "INSTRUCTIONS.md": "Find a way.\n"})
	uploads := len(g.blobs)
	err = contentPublish([]string{"--all", root, "--gateway", g.URL, "--token", "admin"})
	if err == nil || !strings.Contains(err.Error(), "find-a-way") || !strings.Contains(err.Error(), "answers json") {
		t.Fatalf("a package missing its contract = %v", err)
	}
	if len(g.blobs) != uploads || len(g.installs) != 3 {
		t.Fatal("a failing package left an upload or an install behind")
	}
	if err := contentCheck([]string{"--all", root}); err == nil || !strings.Contains(err.Error(), "1 of 3") {
		t.Fatalf("check = %v", err)
	}
	if err := contentCheck([]string{filepath.Join(root, "starters"), filepath.Join(root, "enhance-prompt")}); err != nil {
		t.Fatalf("check of good packages = %v", err)
	}
}
