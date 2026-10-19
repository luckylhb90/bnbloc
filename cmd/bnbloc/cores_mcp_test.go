package main

import (
	"crypto/sha256"
	"encoding/hex"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
)

const mcpStdioJSON = `{"name":"codeword","version":"1.0.0","publisher":"acme","runtime":"stdio",
 "mcp":{"tools":[{"name":"get_codeword"}],"stdio":{"args":["serve"]}},
 "settings":[{"key":"salt","label":"Salt","type":"secret","scope":"member","required":true,"deliver":{"env":"CODEWORD_SALT"}}]}`

// A stdio MCP tool: every --binary is uploaded and pinned, the package is
// signed by the default key and published with the rollout.
func TestPublishMCPStdio(t *testing.T) {
	home := isolateHome(t)
	if err := keysGen([]string{"--out", filepath.Join(home, ".bnbloc", "component-publisher")}); err != nil {
		t.Fatal(err)
	}
	gw := newPublishGateway(t)
	pkgFile := writeTemp(t, "mcp.json", mcpStdioJSON)
	arm := writeTemp(t, "codeword-arm64", "arm binary")
	amd := writeTemp(t, "codeword-amd64", "amd binary")
	if err := coresPublishMCP([]string{"--gateway", gw.URL, "--token", "admin", "--package", pkgFile,
		"--binary", "darwin-arm64=" + arm, "--binary", "linux-amd64=" + amd, "--rollout", "optin"}); err != nil {
		t.Fatal(err)
	}
	if gw.published["rollout"] != "optin" || gw.published["signature"] == "" {
		t.Fatalf("publish = %v", gw.published)
	}
	pkg, err := pkgmeta.ParsePackage([]byte(gw.published["package"]))
	if err != nil {
		t.Fatalf("published package invalid: %v", err)
	}
	if pkg.Kind != pkgmeta.KindMCPPackageV1 || pkg.Contract != pkgmeta.MCPContractV1 || pkg.Runtime != pkgmeta.RuntimeStdio || pkg.MCP.Stdio.Args[0] != "serve" {
		t.Fatalf("pkg = %+v", pkg)
	}
	sum := sha256.Sum256([]byte("arm binary"))
	if a := pkg.Artifacts["darwin-arm64"]; a.SHA256 != hex.EncodeToString(sum[:]) || a.URL != gw.URL+"/v1/artifacts/sha256/"+a.SHA256 || len(pkg.Artifacts) != 2 {
		t.Fatalf("artifacts = %+v", pkg.Artifacts)
	}
}

// An endpoint MCP tool publishes without binaries.
func TestPublishMCPEndpoint(t *testing.T) {
	isolateHome(t)
	gw := newPublishGateway(t)
	pkgFile := writeTemp(t, "mcp.json", `{"name":"search","version":"2.0.0","publisher":"acme","runtime":"endpoint",
 "endpoint":{"baseUrl":"https://tools.example.com/mcp"},"permissions":{"network":["tools.example.com"]},
 "mcp":{"tools":[{"name":"search"}],"http":{"auth":{"scheme":"bearer","setting":"token"}}},
 "settings":[{"key":"token","label":"API token","type":"secret","scope":"org","required":true}]}`)
	if err := coresPublishMCP([]string{"--gateway", gw.URL, "--token", "admin", "--package", pkgFile, "--unsigned"}); err != nil {
		t.Fatal(err)
	}
	pkg, err := pkgmeta.ParsePackage([]byte(gw.published["package"]))
	if err != nil || pkg.Runtime != pkgmeta.RuntimeEndpoint || len(pkg.Artifacts) != 0 {
		t.Fatalf("pkg = %+v err = %v", pkg, err)
	}
}

// Bad input is refused before any byte is uploaded.
func TestPublishMCPRefusesBeforeUpload(t *testing.T) {
	isolateHome(t)
	gw := newPublishGateway(t)
	stdio := writeTemp(t, "mcp.json", mcpStdioJSON)
	bin := writeTemp(t, "codeword", "bin")
	core := writeTemp(t, "core.json", `{"kind":"bnbloc.core.package/v1","name":"acme","version":"1.0.0","publisher":"x","runtime":"manifest"}`)
	invalid := writeTemp(t, "bad.json", strings.Replace(mcpStdioJSON, `"scope":"member"`, `"scope":"room"`, 1))
	endpoint := writeTemp(t, "ep.json", `{"name":"s","version":"1.0.0","publisher":"a","runtime":"endpoint","endpoint":{"baseUrl":"https://t.example.com"},"permissions":{"network":["t.example.com"]},"mcp":{"tools":[{"name":"s"}]}}`)
	for name, c := range map[string]struct {
		args []string
		want string
	}{
		"no package":      {[]string{"--unsigned"}, "usage"},
		"stdio no binary": {[]string{"--package", stdio, "--unsigned"}, "at least one --binary"},
		"missing binary":  {[]string{"--package", stdio, "--binary", "darwin-arm64=/nope", "--unsigned"}, "--binary darwin-arm64"},
		"another kind":    {[]string{"--package", core, "--unsigned"}, "is not bnbloc.mcp.package/v1"},
		"invalid":         {[]string{"--package", invalid, "--binary", "darwin-arm64=" + bin, "--unsigned"}, "scope must be one of"},
		"endpoint binary": {[]string{"--package", endpoint, "--binary", "darwin-arm64=" + bin, "--unsigned"}, "only for a stdio MCP tool"},
		"no key":          {[]string{"--package", stdio, "--binary", "darwin-arm64=" + bin}, "component-publisher.key does not exist"},
		"bad rollout":     {[]string{"--package", stdio, "--binary", "darwin-arm64=" + bin, "--rollout", "all", "--unsigned"}, "--rollout"},
	} {
		args := append([]string{"--gateway", gw.URL, "--token", "admin"}, c.args...)
		if err := coresPublishMCP(args); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want %q", name, err, c.want)
		}
	}
	if gw.published != nil {
		t.Fatal("published despite refused input")
	}
}
