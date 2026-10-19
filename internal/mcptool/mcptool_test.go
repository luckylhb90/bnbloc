package mcptool

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/core/pkgmeta"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/extreg"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

func pipeline() *extpipe.Pipeline {
	return &extpipe.Pipeline{
		Settings:      store.NewMemoryPlatformSettings(),
		Audit:         store.NewMemoryExtensionAudit(),
		AllowUnsigned: true,
		Installers:    map[extreg.Kind]extpipe.KindInstaller{extreg.KindMCP: Installer()},
	}
}

func sha(b byte) string { return strings.Repeat(string(b), 64) }

func stdioPackage(name string) pkgmeta.Package {
	return pkgmeta.Package{
		Kind: pkgmeta.KindMCPPackageV1, Name: name, Version: "1.0.0", Publisher: "acme",
		Contract: pkgmeta.MCPContractV1, Runtime: pkgmeta.RuntimeStdio,
		Artifacts: map[string]pkgmeta.Artifact{
			"darwin-arm64": {URL: "https://gw.example/v1/artifacts/sha256/" + sha('a'), SHA256: sha('a')},
			"linux-amd64":  {URL: "https://gw.example/v1/artifacts/sha256/" + sha('b'), SHA256: sha('b')},
		},
		MCP: &pkgmeta.MCP{
			Tools: []pkgmeta.MCPTool{{Name: "get_codeword"}},
			Stdio: &pkgmeta.MCPStdio{Args: []string{"--stdio"}},
		},
		Settings: []pkgmeta.Setting{
			{Key: "salt", Label: "Salt", Type: pkgmeta.SettingSecret, Scope: pkgmeta.SettingScopeMember, Required: true, Deliver: &pkgmeta.SettingDeliver{Env: "CODEWORD_SALT"}},
			{Key: "lang", Label: "Language", Type: pkgmeta.SettingText, Scope: pkgmeta.SettingScopeMember, Deliver: &pkgmeta.SettingDeliver{Env: "CODEWORD_LANG"}},
		},
	}
}

func httpPackage(name, baseURL string) pkgmeta.Package {
	u, _ := url.Parse(baseURL)
	return pkgmeta.Package{
		Kind: pkgmeta.KindMCPPackageV1, Name: name, Version: "2.0.0", Publisher: "acme",
		Contract: pkgmeta.MCPContractV1, Runtime: pkgmeta.RuntimeEndpoint,
		Endpoint:    &pkgmeta.Endpoint{BaseURL: baseURL},
		Permissions: pkgmeta.Permissions{Network: []string{u.Hostname()}},
		MCP: &pkgmeta.MCP{
			Tools: []pkgmeta.MCPTool{{Name: "search"}},
			HTTP:  &pkgmeta.MCPHTTP{Auth: pkgmeta.MCPHTTPAuth{Scheme: pkgmeta.MCPAuthBearer, Setting: "token"}},
		},
		Settings: []pkgmeta.Setting{{Key: "token", Label: "API token", Type: pkgmeta.SettingSecret, Scope: pkgmeta.SettingScopeOrg, Required: true}},
	}
}

func install(t *testing.T, p *extpipe.Pipeline, pkg pkgmeta.Package, rollout string) (extpipe.Record, error) {
	t.Helper()
	raw, err := json.Marshal(pkg)
	if err != nil {
		t.Fatal(err)
	}
	return p.Install(context.Background(), extpipe.InstallRequest{Body: raw, Actor: "root", Rollout: rollout})
}

func TestInstallStdioPersistsAndRegistersNothing(t *testing.T) {
	p := pipeline()
	rec, err := install(t, p, stdioPackage("codeword"), "")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if rec.Kind != string(extreg.KindMCP) {
		t.Fatalf("kind = %q", rec.Kind)
	}
	if _, ok := extreg.Get(extreg.KindMCP, "codeword"); ok {
		t.Fatal("nothing of an MCP tool runs server-side: no live registry entry")
	}
	cfg, err := ValidateRecord(rec)
	if err != nil {
		t.Fatal(err)
	}
	if Transport(cfg) != wire.MCPTransportStdio || cfg.MCP.Stdio.Args[0] != "--stdio" || !reflect.DeepEqual(SettingKeys(cfg), []string{"salt", "lang"}) {
		t.Fatalf("config = %+v", cfg)
	}
	recs, _ := p.List(context.Background(), extreg.KindMCP)
	if len(recs) != 1 {
		t.Fatalf("records = %+v", recs)
	}
}

func TestInstallHTTPProbesInitialize(t *testing.T) {
	var gotMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		var msg map[string]any
		_ = json.Unmarshal(body, &msg)
		gotMethod, _ = msg["method"].(string)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = io.WriteString(w, "event: message\ndata: {\"jsonrpc\":\"2.0\",\"id\":1,\"result\":{\"protocolVersion\":\"2025-06-18\"}}\n\n")
	}))
	defer srv.Close()
	rec, err := install(t, pipeline(), httpPackage("search", srv.URL+"/mcp"), "")
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	if gotMethod != "initialize" {
		t.Fatalf("probe sent %q", gotMethod)
	}
	cfg, _ := ValidateRecord(rec)
	if Transport(cfg) != wire.MCPTransportHTTP || Host(cfg) != strings.TrimPrefix(srv.URL, "http://") {
		t.Fatalf("transport/host = %s %s", Transport(cfg), Host(cfg))
	}
}

func TestInstallHTTPAcceptsAuthRequiredAndRefusesNonMCP(t *testing.T) {
	status := http.StatusUnauthorized
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(status)
		_, _ = io.WriteString(w, `{"hello":"world"}`)
	}))
	defer srv.Close()
	if _, err := install(t, pipeline(), httpPackage("auth", srv.URL), ""); err != nil {
		t.Fatalf("an endpoint that wants its declared credentials installs: %v", err)
	}
	status = http.StatusOK
	if _, err := install(t, pipeline(), httpPackage("notmcp", srv.URL), ""); err == nil || !strings.Contains(err.Error(), "JSON-RPC") {
		t.Fatalf("a non-MCP answer must refuse the install: %v", err)
	}
	status = http.StatusNotFound
	if _, err := install(t, pipeline(), httpPackage("gone", srv.URL), ""); err == nil || !strings.Contains(err.Error(), "status 404") {
		t.Fatalf("err = %v", err)
	}
}

func TestInstallHTTPHeldToNetworkAllowlist(t *testing.T) {
	pkg := httpPackage("search", "https://tools.example.com/mcp")
	pkg.Permissions.Network = []string{"other.example.com"}
	if _, err := install(t, pipeline(), pkg, ""); err == nil || !strings.Contains(err.Error(), "permissions.network") {
		t.Fatalf("err = %v", err)
	}
}

// Visibility goes through the one function: rollout and tenant scope gate
// an MCP record exactly like every executable record.
func TestVisibilityOfMCPRecords(t *testing.T) {
	p := pipeline()
	rec, err := install(t, p, stdioPackage("codeword"), "optin")
	if err != nil {
		t.Fatal(err)
	}
	maya := extpipe.Viewer{Subject: "maya", Tenant: "t1"}
	if v := extpipe.MemberVisibility(maya, extpipe.GateForRecord(rec), 0); !v.Visible || v.Installed {
		t.Fatalf("optin before install = %+v", v)
	}
	if v := extpipe.MemberVisibility(maya, extpipe.GateForRecord(rec), 5); !v.Installed {
		t.Fatalf("optin after install = %+v", v)
	}
	rec.Rollout = extpipe.RolloutOff
	if v := extpipe.MemberVisibility(maya, extpipe.GateForRecord(rec), 5); v.Visible {
		t.Fatalf("off = %+v", v)
	}
	rec.Rollout = extpipe.RolloutAuto
	rec.EnabledTenants = []string{"t2"}
	if v := extpipe.MemberVisibility(maya, extpipe.GateForRecord(rec), 0); v.Visible {
		t.Fatalf("other tenant = %+v", v)
	}
}

func lookupOf(t *testing.T, cands ...Candidate) Lookup {
	t.Helper()
	return func(name string) (Candidate, bool) {
		for _, c := range cands {
			if c.Record.Name == name {
				return c, true
			}
		}
		return Candidate{}, false
	}
}

func candidate(t *testing.T, pkg pkgmeta.Package, installed bool) Candidate {
	t.Helper()
	raw, err := extpipe.EncodePackageConfig(pkg)
	if err != nil {
		t.Fatal(err)
	}
	rec := extpipe.Record{Kind: string(extreg.KindMCP), Name: pkg.Name, Version: pkg.Version, Config: raw}
	cfg, _ := extpipe.DecodeConfig(rec)
	return Candidate{Record: rec, Config: cfg, Visibility: extpipe.Visibility{Visible: true, Installed: installed}}
}

// orgSettings answers the given stored values for every tool, held to
// each tool's declaration like componentsettings.Store.Resolved.
func orgSettings(values map[string]map[string]string) Settings {
	return func(name string, decl []pkgmeta.Setting) (map[string]string, []string, error) {
		stored := map[string]string{}
		for _, s := range pkgmeta.SettingsOfScope(decl, pkgmeta.SettingScopePlatform, pkgmeta.SettingScopeOrg) {
			if v := values[name][s.Key]; v != "" {
				stored[s.Key] = v
			}
		}
		return stored, pkgmeta.UnsetSettings(decl, stored, pkgmeta.SettingScopePlatform, pkgmeta.SettingScopeOrg), nil
	}
}

func TestResolve(t *testing.T) {
	refs := []wire.ContentRef{
		{ComponentID: "cc_expert", MCP: []wire.ContentMCPRequirement{{Name: "codeword", Settings: []string{"lang"}}, {Name: "search"}, {Name: "ghost"}}},
		{ComponentID: "cc_pack", MCP: []wire.ContentMCPRequirement{{Name: "codeword"}, {Name: "optin"}}},
	}
	lookup := lookupOf(t,
		candidate(t, stdioPackage("codeword"), true),
		candidate(t, httpPackage("search", "https://tools.example.com/mcp"), true),
		candidate(t, stdioPackage("optin"), false),
	)
	tools, missing, err := Resolve(refs, nil, lookup, orgSettings(map[string]map[string]string{"search": {"token": "tok"}}))
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(missing, []string{"MCP tool ghost", "MCP tool optin"}) {
		t.Fatalf("missing = %v", missing)
	}
	if len(tools) != 2 {
		t.Fatalf("tools = %+v", tools)
	}
	cw := tools[0]
	if cw.Name != "codeword" || cw.Transport != wire.MCPTransportStdio || cw.Artifacts["darwin-arm64"] != sha('a') ||
		!reflect.DeepEqual(cw.Args, []string{"--stdio"}) || !reflect.DeepEqual(cw.RequiredBy, []string{"cc_expert", "cc_pack"}) ||
		cw.Credentials != "" || cw.Values != nil {
		t.Fatalf("codeword = %+v", cw)
	}
	// Required: declared required, or named by a requiring package.
	if !cw.Settings[0].Required || !cw.Settings[1].Required || cw.Settings[0].Deliver.Env != "CODEWORD_SALT" {
		t.Fatalf("codeword settings = %+v", cw.Settings)
	}
	s := tools[1]
	if s.Transport != wire.MCPTransportHTTP || s.URL != "https://tools.example.com/mcp" || s.Auth.Scheme != "bearer" || s.Auth.Setting != "token" || s.Values["token"] != "tok" {
		t.Fatalf("search = %+v", s)
	}
}

func TestResolveOAuthNeedsNoSettings(t *testing.T) {
	pkg := httpPackage("drive", "https://tools.example.com/mcp")
	pkg.Settings = nil
	pkg.MCP.HTTP = &pkgmeta.MCPHTTP{Auth: pkgmeta.MCPHTTPAuth{Scheme: pkgmeta.MCPAuthOAuth, ClientID: "c", Scopes: []string{"files:read"}}}
	refs := []wire.ContentRef{{ComponentID: "cc", MCP: []wire.ContentMCPRequirement{{Name: "drive"}}}}
	tools, missing, err := Resolve(refs, nil, lookupOf(t, candidate(t, pkg, true)), orgSettings(nil))
	if err != nil || len(missing) != 0 || len(tools) != 1 {
		t.Fatalf("tools=%+v missing=%v err=%v", tools, missing, err)
	}
	if a := tools[0].Auth; a == nil || a.Scheme != pkgmeta.MCPAuthOAuth || a.Setting != "" || tools[0].Credentials != "" || tools[0].Values != nil {
		t.Fatalf("drive = %+v auth=%+v", tools[0], tools[0].Auth)
	}
}

// A tool whose organization settings are not set needs setup, and one that
// no longer has the settings the content names is unmet.
func TestResolveNeedsSetupAndUndeclaredSetting(t *testing.T) {
	refs := []wire.ContentRef{{ComponentID: "cc", MCP: []wire.ContentMCPRequirement{{Name: "search"}, {Name: "codeword", Settings: []string{"gone"}}}}}
	lookup := lookupOf(t, candidate(t, httpPackage("search", "https://tools.example.com/mcp"), true), candidate(t, stdioPackage("codeword"), true))
	tools, missing, err := Resolve(refs, nil, lookup, orgSettings(map[string]map[string]string{"search": {"token": " "}}))
	if err != nil || len(tools) != 0 {
		t.Fatalf("tools=%+v missing=%v err=%v", tools, missing, err)
	}
	want := []string{"MCP tool codeword (it no longer has the settings the content needs)", "MCP tool search (needs setup: an admin has not set API token)"}
	if !reflect.DeepEqual(missing, want) {
		t.Fatalf("missing = %v", missing)
	}
}

// A tool the requester installed is in effect without any content
// requiring it (docs/prd/2026-10-17-browser-component.md §3.6); one that
// cannot be attached is left out without a missing line, unless content
// requires it too.
func TestResolveOffersInstalledTools(t *testing.T) {
	lookup := lookupOf(t,
		candidate(t, stdioPackage("codeword"), true),
		candidate(t, httpPackage("search", "https://tools.example.com/mcp"), true),
		candidate(t, stdioPackage("optin"), false),
	)
	notSet := orgSettings(nil)
	tools, missing, err := Resolve(nil, []string{"codeword", "search", "optin"}, lookup, notSet)
	if err != nil || len(missing) != 0 || len(tools) != 1 || tools[0].Name != "codeword" || len(tools[0].RequiredBy) != 0 {
		t.Fatalf("tools=%+v missing=%v err=%v", tools, missing, err)
	}
	refs := []wire.ContentRef{{ComponentID: "cc", MCP: []wire.ContentMCPRequirement{{Name: "search"}}}}
	tools, missing, err = Resolve(refs, []string{"codeword", "search"}, lookup, notSet)
	if err != nil || len(tools) != 1 || !reflect.DeepEqual(missing, []string{"MCP tool search (needs setup: an admin has not set API token)"}) {
		t.Fatalf("tools=%+v missing=%v err=%v", tools, missing, err)
	}
}
