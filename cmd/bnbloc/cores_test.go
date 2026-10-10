package main

import (
	"encoding/json"
	"flag"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/core/protocol"
)

func TestBuildPackageFromURLProbesDiscovery(t *testing.T) {
	daemon := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != protocol.PathDiscover {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		_ = json.NewEncoder(w).Encode(protocol.Discovery{
			Name: "acme", Version: "1.2.3", Protocol: protocol.ProtocolRange{Min: "1.0", Max: "1.0"},
		})
	}))
	defer daemon.Close()

	body, err := buildPackage(daemon.URL, "", "", "acme", "0.1.0", "self")
	if err != nil {
		t.Fatalf("buildPackage: %v", err)
	}
	// The assembled package must be a valid, parseable endpoint core package.
	pkg, err := pkgmeta.ParsePackage(body)
	if err != nil {
		t.Fatalf("assembled package invalid: %v", err)
	}
	if pkg.Runtime != pkgmeta.RuntimeEndpoint || pkg.Name != "acme" {
		t.Fatalf("pkg = %+v; want endpoint runtime named acme", pkg)
	}
	if pkg.Endpoint == nil || pkg.Endpoint.BaseURL != daemon.URL {
		t.Fatalf("endpoint = %+v; want %s", pkg.Endpoint, daemon.URL)
	}
	if len(pkg.Permissions.Network) != 1 {
		t.Fatalf("permissions.network = %v; want the daemon host", pkg.Permissions.Network)
	}
}

func TestBuildPackageRequiresASource(t *testing.T) {
	if _, err := buildPackage("", "", "", "n", "0.1.0", "self"); err == nil {
		t.Fatal("buildPackage with no --url/--manifest/--package must error")
	}
}

func TestBuildPackageURLRequiresName(t *testing.T) {
	if _, err := buildPackage("http://x.test", "", "", "", "0.1.0", "self"); err == nil {
		t.Fatal("--url without --name must error")
	}
}

func TestResolveConfigLayering(t *testing.T) {
	isolateHome(t)
	t.Setenv("BNBLOC_GATEWAY", "http://env-gw:9")
	t.Setenv("BNBLOC_TOKEN", "env-token")
	// flag overrides env
	cfg := resolveConfig("http://flag-gw:1/", "")
	if cfg.Gateway != "http://flag-gw:1" {
		t.Fatalf("gateway = %q; want flag override (trailing slash trimmed)", cfg.Gateway)
	}
	if cfg.Token != "env-token" {
		t.Fatalf("token = %q; want env fallback", cfg.Token)
	}
}

// BR-23: a flag's value must never be taken as the component name, wherever
// the flags appear relative to it.
func TestParseNamedInterspersedFlags(t *testing.T) {
	cases := [][]string{
		{"--gateway", "http://gw:1", "nimbus"},
		{"nimbus", "--gateway", "http://gw:1"},
		{"--gateway=http://gw:1", "nimbus", "--token", "tok"},
	}
	for _, args := range cases {
		fs := flag.NewFlagSet("cores uninstall", flag.ContinueOnError)
		gateway := fs.String("gateway", "", "")
		_ = fs.String("token", "", "")
		name, err := parseNamed(fs, args)
		if err != nil || name != "nimbus" || *gateway != "http://gw:1" {
			t.Fatalf("parseNamed(%q) = %q, gateway %q, err %v; want nimbus + http://gw:1", args, name, *gateway, err)
		}
	}
	fs := flag.NewFlagSet("x", flag.ContinueOnError)
	if _, err := parseNamed(fs, []string{"a", "b"}); err == nil {
		t.Fatal("two positionals must be rejected")
	}
}

// BR-23: a streamed artifact upload has no total client deadline (a 256 MiB
// upload takes longer than any JSON call), while JSON calls stay bounded.
func TestUploadIsNotBoundedByTotalTimeout(t *testing.T) {
	c := newClient(config{Gateway: "http://gw"})
	if c.http.Timeout != 0 {
		t.Fatalf("http.Client.Timeout = %v; uploads must not have a total deadline", c.http.Timeout)
	}
}

// BR-24: cores commands address the core kind explicitly.
func TestCoreComponentPathPinsKind(t *testing.T) {
	if got := coreComponentPath("my core", "/tenants"); got != "/v1/platform/components/my%20core/tenants?kind=core" {
		t.Fatalf("path = %q", got)
	}
}
