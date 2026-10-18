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
)

// publishGateway accepts artifact uploads and records the component publish.
type publishGateway struct {
	*httptest.Server
	published map[string]string
	uploads   int
}

func newPublishGateway(t *testing.T) *publishGateway {
	g := &publishGateway{}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "POST /v1/platform/artifacts":
			g.uploads++
			raw, _ := io.ReadAll(r.Body)
			if r.ContentLength != int64(len(raw)) {
				http.Error(w, "upload must carry its length", http.StatusLengthRequired)
				return
			}
			sum := sha256.Sum256(raw)
			sha := hex.EncodeToString(sum[:])
			_ = json.NewEncoder(w).Encode(map[string]any{"key": "sha256/" + sha, "sha256": sha, "size": len(raw)})
		case "POST /v1/platform/components":
			_ = json.NewDecoder(r.Body).Decode(&g.published)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(g.Close)
	return g
}

func writeTemp(t *testing.T, name, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(content), 0o755); err != nil {
		t.Fatal(err)
	}
	return path
}

// Every publish-sidecar flag lands in the package; --rollout rides in the
// same publish request; the default publisher key signs it.
func TestPublishSidecarFlagMapping(t *testing.T) {
	home := isolateHome(t)
	if err := keysGen([]string{"--out", filepath.Join(home, ".bnbloc", "component-publisher")}); err != nil {
		t.Fatal(err)
	}
	gw := newPublishGateway(t)
	arm := writeTemp(t, "acme-arm64", "arm binary")
	amd := writeTemp(t, "acme-amd64", "amd binary")
	err := coresPublishSidecar([]string{
		"--gateway", gw.URL, "--token", "admin",
		"--name", "acme", "--version", "1.4.0", "--publisher", "acme-inc",
		"--local-url", "http://127.0.0.1:7099", "--listen-env", "ACME_LISTEN", "--supervised-env", "ACME_SUPERVISED",
		"--binary", "darwin-arm64=" + arm, "--binary", "darwin-amd64=" + amd,
		"--rollout", "optin",
	})
	if err != nil {
		t.Fatal(err)
	}
	if gw.published["rollout"] != "optin" || gw.published["signature"] == "" {
		t.Fatalf("publish request = %v, want rollout optin + a signature", gw.published)
	}
	pkg, err := pkgmeta.ParsePackage([]byte(gw.published["package"]))
	if err != nil {
		t.Fatalf("published package invalid: %v", err)
	}
	if pkg.Name != "acme" || pkg.Version != "1.4.0" || pkg.Publisher != "acme-inc" || pkg.Runtime != pkgmeta.RuntimeSidecar {
		t.Fatalf("pkg = %+v", pkg)
	}
	if pkg.Sidecar == nil || pkg.Sidecar.LocalURL != "http://127.0.0.1:7099" || pkg.Sidecar.ListenEnv != "ACME_LISTEN" || pkg.Sidecar.SupervisedEnv != "ACME_SUPERVISED" {
		t.Fatalf("sidecar = %+v", pkg.Sidecar)
	}
	sum := sha256.Sum256([]byte("arm binary"))
	if a := pkg.Artifacts["darwin-arm64"]; a.SHA256 != hex.EncodeToString(sum[:]) || a.URL != gw.URL+"/v1/artifacts/sha256/"+a.SHA256 {
		t.Fatalf("arm artifact = %+v", a)
	}
	if len(pkg.Artifacts) != 2 {
		t.Fatalf("artifacts = %v", pkg.Artifacts)
	}
}

// Bad input is refused before any byte is uploaded.
func TestPublishSidecarRefusesBeforeUpload(t *testing.T) {
	isolateHome(t) // no default key
	gw := newPublishGateway(t)
	bin := writeTemp(t, "acme", "bin")
	base := []string{"--gateway", gw.URL, "--token", "admin", "--name", "acme", "--local-url", "http://127.0.0.1:7099", "--binary", "darwin-arm64=" + bin}
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"bad rollout":  {append(append([]string{}, base...), "--rollout", "everyone", "--unsigned"), "--rollout"},
		"no key":       {base, "component-publisher.key does not exist"},
		"bad binary":   {[]string{"--name", "acme", "--local-url", "http://x", "--binary", "nopath", "--unsigned"}, "--binary must be"},
		"no local url": {[]string{"--name", "acme", "--binary", "darwin-arm64=" + bin, "--unsigned"}, "--local-url is required"},
		"no binary":    {[]string{"--name", "acme", "--local-url", "http://x", "--unsigned"}, "at least one --binary"},
		"no name":      {[]string{"--binary", "darwin-arm64=" + bin, "--unsigned"}, "--name is required"},
		// The package is held to the install gate before any upload, so a
		// bad name, version or URL leaves no orphan binary behind.
		"bad name":     {append(append([]string{}, base...), "--unsigned", "--name", "Acme!"), "package invalid"},
		"bad version":  {append(append([]string{}, base...), "--unsigned", "--version", "1.0"), "semver"},
		"remote url":   {append(append([]string{}, base...), "--unsigned", "--local-url", "http://example.com:7099"), "loopback"},
		"bad env name": {append(append([]string{}, base...), "--unsigned", "--listen-env", "acme-listen"), "upper-case identifier"},
	} {
		err := coresPublishSidecar(c.args)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
	if gw.uploads != 0 {
		t.Fatalf("%d binaries uploaded for refused input", gw.uploads)
	}
	if gw.published != nil {
		t.Fatal("published despite refused input")
	}
}

// `cores install --package --signature` passes a pre-built package and its
// signature through unchanged, with the rollout.
func TestInstallPrebuiltPackageWithRollout(t *testing.T) {
	isolateHome(t)
	gw := newPublishGateway(t)
	body := `{"kind":"bnbloc.core.package/v1","name":"acme","version":"1.0.0","runtime":"endpoint","endpoint":{"baseUrl":"https://acme.example.com"},"protocol":{"min":"1.0","max":"1.0"},"publisher":"self","description":"","permissions":{}}`
	pkg := writeTemp(t, "pkg.json", body)
	sig := writeTemp(t, "pkg.sig", "signature-bytes\n")
	// An invalid package is refused locally, before anything is sent.
	bad := writeTemp(t, "bad.json", `{"kind":"bnbloc.core.package/v1","name":"acme"}`)
	if err := coresInstall([]string{"--gateway", gw.URL, "--token", "admin", "--package", bad, "--signature", sig}); err == nil || gw.published != nil {
		t.Fatalf("an invalid package was sent: err = %v", err)
	}
	if err := coresInstall([]string{"--gateway", gw.URL, "--token", "admin", "--package", pkg, "--signature", sig, "--rollout", "off"}); err != nil {
		t.Fatal(err)
	}
	if gw.published["package"] != body || gw.published["signature"] != "signature-bytes\n" || gw.published["rollout"] != "off" {
		t.Fatalf("publish request = %v", gw.published)
	}
}

// keys gen writes the private key 0600 (even replacing an older file) and
// never clobbers an existing keypair without --force.
func TestKeysGenWritesPrivateKey0600(t *testing.T) {
	home := isolateHome(t)
	prefix := filepath.Join(home, ".bnbloc", "component-publisher")
	if err := os.MkdirAll(filepath.Dir(prefix), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(prefix+".key", []byte("old"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := keysGen([]string{"--out", prefix}); err == nil {
		t.Fatal("existing key overwritten without --force")
	}
	if err := keysGen([]string{"--out", prefix, "--force"}); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(prefix + ".key")
	if err != nil || st.Mode().Perm() != 0o600 {
		t.Fatalf("key mode = %v (%v), want 0600", st.Mode().Perm(), err)
	}
	if _, err := loadPrivateKey(prefix + ".key"); err != nil {
		t.Fatalf("generated key unreadable: %v", err)
	}
	if _, err := os.Stat(prefix + ".pub"); err != nil {
		t.Fatal(err)
	}
}

// A package file with no artifacts is published with the binaries it is given:
// they are uploaded and pinned, the package is signed, and a plugin that has
// no hook (a skills provider) passes the gate.
func TestPublishPackageUploadsBinariesAndPinsThem(t *testing.T) {
	home := isolateHome(t)
	if err := keysGen([]string{"--out", filepath.Join(home, ".bnbloc", "component-publisher")}); err != nil {
		t.Fatal(err)
	}
	gw := newPublishGateway(t)
	bin := writeTemp(t, "skills", "binary")
	pkgFile := writeTemp(t, "package.json", `{
  "kind": "bnbloc.plugin.package/v1",
  "contract": "bnbloc.plugin.contract/v1",
  "name": "skills",
  "version": "0.1.0",
  "publisher": "acme",
  "runtime": "sidecar",
  "capabilities": ["skills-provider", "sandbox-mounts"],
  "sidecar": {
    "localUrl": "http://127.0.0.1:7192",
    "listenEnv": "SKILLS_LISTEN_ADDR",
    "tokenEnv": "SKILLS_TOKEN",
    "mountsEnv": "SKILLS_MOUNTS_FILE",
    "mountRootEnv": "SKILLS_MOUNT_ROOT"
  }
}`)
	if err := componentsPublishPackage([]string{"--gateway", gw.URL, "--token", "admin", "--package", pkgFile, "--binary", "darwin-arm64=" + bin, "--rollout", "optin"}); err != nil {
		t.Fatal(err)
	}
	if gw.uploads != 1 || gw.published["rollout"] != "optin" || gw.published["signature"] == "" {
		t.Fatalf("uploads=%d publish=%v", gw.uploads, gw.published)
	}
	pkg, err := pkgmeta.ParsePackage([]byte(gw.published["package"]))
	if err != nil {
		t.Fatalf("published package invalid: %v", err)
	}
	art := pkg.Artifacts["darwin-arm64"]
	if pkg.Kind != pkgmeta.KindPluginPackageV1 || art.URL == "" || len(art.SHA256) != 64 || !strings.Contains(strings.Join(pkg.Capabilities, ","), "skills-provider") {
		t.Fatalf("pkg = %+v", pkg)
	}
}

func TestPublishPackageRefusesBeforeUploading(t *testing.T) {
	isolateHome(t)
	gw := newPublishGateway(t)
	bin := writeTemp(t, "bin", "binary")
	pinned := writeTemp(t, "pinned.json", `{"kind":"bnbloc.plugin.package/v1","contract":"bnbloc.plugin.contract/v1","name":"p","version":"1.0.0","runtime":"sidecar","capabilities":["skills-provider"],"sidecar":{"localUrl":"http://127.0.0.1:7192","tokenEnv":"T"},"artifacts":{"darwin-arm64":{"url":"x","sha256":"y"}}}`)
	hookless := writeTemp(t, "hookless.json", `{"kind":"bnbloc.plugin.package/v1","contract":"bnbloc.plugin.contract/v1","name":"p","version":"1.0.0","runtime":"sidecar","sidecar":{"localUrl":"http://127.0.0.1:7192"}}`)
	for name, args := range map[string][]string{
		"artifacts named in the file": {"--package", pinned, "--binary", "darwin-arm64=" + bin},
		"a package the gate refuses":  {"--package", hookless, "--binary", "darwin-arm64=" + bin},
		"no package":                  {"--binary", "darwin-arm64=" + bin},
		"no binary":                   {"--package", hookless},
	} {
		if err := componentsPublishPackage(append([]string{"--gateway", gw.URL, "--token", "admin", "--unsigned"}, args...)); err == nil {
			t.Errorf("%s must be refused", name)
		}
	}
	if gw.uploads != 0 || gw.published != nil {
		t.Errorf("a refused package uploads nothing: uploads=%d published=%v", gw.uploads, gw.published)
	}
}
