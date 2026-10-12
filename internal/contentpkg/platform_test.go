package contentpkg

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
)

// A content version's platform package parses as a pkgmeta package of its
// content kind (so the existing signing and install path takes it), and
// nothing an executable component declares is accepted in it.
func TestPlatformPackage(t *testing.T) {
	sha := strings.Repeat("a", 64)
	m := Manifest{Kind: KindExpert, Name: "reviewer", Version: "1.2.0", Description: "Reviews."}
	pkg := PlatformPackage(m, sha, "https://gw.example.com/v1/artifacts/sha256/"+sha, "acme")
	raw, err := json.Marshal(pkg)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pkgmeta.ParsePackage(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if got.Kind != KindExpert || got.Artifacts[PlatformBundleArtifact].SHA256 != sha || got.Runtime != pkgmeta.RuntimeBundle {
		t.Fatalf("package = %+v", got)
	}
	if err := pkgmeta.ValidateGatewayArtifactURLs(got, "https://gw.example.com"); err != nil {
		t.Fatalf("gateway artifact url: %v", err)
	}
	for name, mutate := range map[string]func(*pkgmeta.Package){
		"network":   func(p *pkgmeta.Package) { p.Permissions.Network = []string{"x.example.com"} },
		"endpoint":  func(p *pkgmeta.Package) { p.Endpoint = &pkgmeta.Endpoint{BaseURL: "http://127.0.0.1:1"} },
		"runtime":   func(p *pkgmeta.Package) { p.Runtime = pkgmeta.RuntimeSidecar },
		"artifacts": func(p *pkgmeta.Package) { p.Artifacts["extra"] = pkgmeta.Artifact{URL: "u", SHA256: sha} },
		"no bundle": func(p *pkgmeta.Package) { p.Artifacts = nil },
		"name":      func(p *pkgmeta.Package) { p.Name = "Bad.Name" },
		"hooks":     func(p *pkgmeta.Package) { p.Hooks = []string{"chat_message.v1"} },
	} {
		bad := PlatformPackage(m, sha, "https://gw.example.com/v1/artifacts/sha256/"+sha, "acme")
		mutate(&bad)
		if err := pkgmeta.ValidatePackage(bad); err == nil {
			t.Fatalf("%s: accepted", name)
		}
	}
	if !IsContentKind(KindPrompt) || IsContentKind(pkgmeta.KindCorePackageV1) {
		t.Fatal("IsContentKind")
	}
}
