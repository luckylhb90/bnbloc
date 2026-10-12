package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
)

// contentGateway answers the package export and records the install.
func contentGateway(t *testing.T, pkg string) (*httptest.Server, *map[string]string, *string) {
	t.Helper()
	published := map[string]string{}
	query := ""
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method + " " + r.URL.Path {
		case "GET /v1/platform/content-components/cc_1/package":
			query = r.URL.RawQuery
			_, _ = w.Write([]byte(pkg))
		case "POST /v1/platform/components":
			_ = json.NewDecoder(r.Body).Decode(&published)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, &published, &query
}

const contentPkg = `{"kind":"bnbloc.expert.package/v1","name":"reviewer","version":"1.2.0","publisher":"acme","runtime":"bundle",` +
	`"artifacts":{"bundle":{"url":"https://gw.example.com/v1/artifacts/sha256/aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","sha256":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}}}`

// export writes the exact package bytes; promote signs those bytes with the
// default publisher key and installs them through the component route.
func TestContentExportAndPromote(t *testing.T) {
	home := isolateHome(t)
	srv, published, query := contentGateway(t, contentPkg)
	out := filepath.Join(t.TempDir(), "pkg.json")
	if err := contentExport([]string{"cc_1", "--gateway", srv.URL, "--token", "admin", "--version", "1.2.0", "--publisher", "acme", "--out", out}); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(out); string(raw) != contentPkg || !strings.Contains(*query, "version=1.2.0") || !strings.Contains(*query, "publisher=acme") {
		t.Fatalf("export = %s (query %s)", raw, *query)
	}
	if err := contentPromote([]string{"cc_1", "--gateway", srv.URL, "--token", "admin"}); err == nil || !strings.Contains(err.Error(), "--unsigned") {
		t.Fatalf("promote without a key = %v", err)
	}
	if err := keysGen([]string{"--out", filepath.Join(home, ".bnbloc", "component-publisher")}); err != nil {
		t.Fatal(err)
	}
	if err := contentPromote([]string{"cc_1", "--gateway", srv.URL, "--token", "admin", "--rollout", "optin"}); err == nil {
		t.Fatal("optin rollout accepted")
	}
	if err := contentPromote([]string{"cc_1", "--gateway", srv.URL, "--token", "admin"}); err != nil {
		t.Fatal(err)
	}
	if (*published)["package"] != contentPkg || (*published)["signature"] == "" {
		t.Fatalf("published = %v", *published)
	}
	// The signature covers exactly the exported bytes.
	pub, _ := os.ReadFile(filepath.Join(home, ".bnbloc", "component-publisher.pub"))
	lines := strings.Split(strings.TrimSpace(string(pub)), "\n")
	keyID, _, err := pkgmeta.DecodeMinisignPublicKey(lines[len(lines)-1])
	if err != nil {
		t.Fatal(err)
	}
	trusted := []pkgmeta.TrustedKey{{KeyID: keyID, PublicKey: lines[len(lines)-1]}}
	if _, err := pkgmeta.VerifySignedPackage([]byte(contentPkg), []byte((*published)["signature"]), trusted); err != nil {
		t.Fatalf("signature does not verify: %v", err)
	}
	if _, err := pkgmeta.VerifySignedPackage([]byte(strings.Replace(contentPkg, "acme", "evil", 1)), []byte((*published)["signature"]), trusted); err == nil || !strings.Contains(err.Error(), "verification failed") {
		t.Fatalf("tampered package = %v", err)
	}
}
