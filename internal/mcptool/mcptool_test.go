package mcptool

import (
	"context"
	"crypto/rand"
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
	"github.com/luckylhb90/bnbloc/internal/sealbox"
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
			Tools:       []pkgmeta.MCPTool{{Name: "get_codeword"}},
			Env:         []pkgmeta.MCPEnv{{Name: "CODEWORD_SALT", Secret: true, Required: true}, {Name: "CODEWORD_LANG"}},
			Credentials: pkgmeta.MCPCredentialsMember,
			Stdio:       &pkgmeta.MCPStdio{Args: []string{"--stdio"}},
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
			Tools:       []pkgmeta.MCPTool{{Name: "search"}},
			Env:         []pkgmeta.MCPEnv{{Name: "TOOLS_TOKEN", Secret: true, Required: true}},
			Credentials: pkgmeta.MCPCredentialsOrg,
			HTTP:        &pkgmeta.MCPHTTP{Auth: pkgmeta.MCPHTTPAuth{Scheme: pkgmeta.MCPAuthBearer, Env: "TOOLS_TOKEN"}},
		},
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
	if Transport(cfg) != wire.MCPTransportStdio || cfg.MCP.Stdio.Args[0] != "--stdio" || !reflect.DeepEqual(EnvNames(cfg), []string{"CODEWORD_SALT", "CODEWORD_LANG"}) {
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

func TestResolve(t *testing.T) {
	refs := []wire.ContentRef{
		{ComponentID: "cc_expert", MCP: []wire.ContentMCPRequirement{{Name: "codeword", Env: []string{"CODEWORD_LANG"}}, {Name: "search"}, {Name: "ghost"}}},
		{ComponentID: "cc_pack", MCP: []wire.ContentMCPRequirement{{Name: "codeword"}, {Name: "optin"}}},
	}
	lookup := lookupOf(t,
		candidate(t, stdioPackage("codeword"), true),
		candidate(t, httpPackage("search", "https://tools.example.com/mcp"), true),
		candidate(t, stdioPackage("optin"), false),
	)
	orgCalls := 0
	org := func(name string) (map[string]string, bool, error) {
		orgCalls++
		if name != "search" {
			t.Fatalf("org values asked for %s", name)
		}
		return map[string]string{"TOOLS_TOKEN": "tok"}, true, nil
	}
	tools, missing, err := Resolve(refs, lookup, org)
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
		cw.Credentials != "member" || cw.Values != nil {
		t.Fatalf("codeword = %+v", cw)
	}
	// Required: declared required, or named by a requiring package.
	if !cw.Env[0].Required || !cw.Env[1].Required || !cw.Env[0].Secret {
		t.Fatalf("codeword env = %+v", cw.Env)
	}
	s := tools[1]
	if s.Transport != wire.MCPTransportHTTP || s.URL != "https://tools.example.com/mcp" || s.Auth.Scheme != "bearer" || s.Values["TOOLS_TOKEN"] != "tok" {
		t.Fatalf("search = %+v", s)
	}
	if orgCalls != 1 {
		t.Fatalf("org values read %d times", orgCalls)
	}
}

func TestResolveMissingConnectorAndUndeclaredEnv(t *testing.T) {
	refs := []wire.ContentRef{{ComponentID: "cc", MCP: []wire.ContentMCPRequirement{{Name: "search"}, {Name: "codeword", Env: []string{"GONE"}}}}}
	lookup := lookupOf(t, candidate(t, httpPackage("search", "https://tools.example.com/mcp"), true), candidate(t, stdioPackage("codeword"), true))
	for _, org := range []OrgValues{
		func(string) (map[string]string, bool, error) { return nil, false, nil },
		func(string) (map[string]string, bool, error) { return map[string]string{}, true, nil },
		func(string) (map[string]string, bool, error) { return nil, false, ErrConnectorsOff },
	} {
		tools, missing, err := Resolve(refs, lookup, org)
		if err != nil || len(tools) != 0 || len(missing) != 2 {
			t.Fatalf("tools=%+v missing=%v err=%v", tools, missing, err)
		}
		if !strings.Contains(strings.Join(missing, ";"), "no longer takes the variables") {
			t.Fatalf("missing = %v", missing)
		}
	}
}

func testBox(t *testing.T) *sealbox.Box {
	t.Helper()
	key := make([]byte, 32)
	_, _ = rand.Read(key)
	b, err := sealbox.New(key)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestConnectorsSealedPerOrganization(t *testing.T) {
	ctx := context.Background()
	settings := store.NewMemoryPlatformSettings()
	c := &Connectors{Settings: settings, Box: testBox(t), Now: func() int64 { return 42 }}
	if err := c.Put(ctx, "t1", "search", map[string]string{"TOOLS_TOKEN": "s3cr3t"}, "admin"); err != nil {
		t.Fatal(err)
	}
	rows, _ := settings.ListPlatformSettings(ctx)
	if len(rows) != 1 || strings.Contains(rows[0].Value, "s3cr3t") {
		t.Fatalf("stored in clear: %+v", rows)
	}
	got, ok, err := c.Get(ctx, "t1", "search")
	if err != nil || !ok || got.Values["TOOLS_TOKEN"] != "s3cr3t" || got.UpdatedBy != "admin" || got.UpdatedAtMs != 42 {
		t.Fatalf("get = %+v %v %v", got, ok, err)
	}
	if _, ok, _ := c.Get(ctx, "t2", "search"); ok {
		t.Fatal("another organization has no connector")
	}
	// A row copied to another organization does not open.
	_ = settings.PutPlatformSetting(ctx, store.PlatformSetting{Key: connectorKey("t2", "search"), Value: rows[0].Value})
	if _, _, err := c.Get(ctx, "t2", "search"); err == nil {
		t.Fatal("a moved sealed row must not open")
	}
	_ = c.Put(ctx, "t1", "other", map[string]string{"X": "y"}, "admin")
	if err := c.DeleteComponent(ctx, "search"); err != nil {
		t.Fatal(err)
	}
	rows, _ = settings.ListPlatformSettings(ctx)
	if len(rows) != 1 || rows[0].Key != connectorKey("t1", "other") {
		t.Fatalf("after uninstall = %+v", rows)
	}
	off := &Connectors{Settings: settings}
	if err := off.Put(ctx, "t1", "x", nil, "a"); err != ErrConnectorsOff {
		t.Fatalf("without a key connectors are off: %v", err)
	}
}

func TestMergeValues(t *testing.T) {
	env := []pkgmeta.MCPEnv{{Name: "TOKEN", Secret: true, Required: true}, {Name: "REGION"}}
	if _, err := MergeValues(env, nil, map[string]string{"REGION": "eu"}); err == nil || !strings.Contains(err.Error(), "TOKEN is required") {
		t.Fatalf("err = %v", err)
	}
	if _, err := MergeValues(env, nil, map[string]string{"OTHER": "x"}); err == nil {
		t.Fatal("unknown variable accepted")
	}
	first, err := MergeValues(env, nil, map[string]string{"TOKEN": " t ", "REGION": "eu"})
	if err != nil || first["TOKEN"] != "t" {
		t.Fatalf("first = %v %v", first, err)
	}
	second, err := MergeValues(env, first, map[string]string{"TOKEN": "", "REGION": ""})
	if err != nil || second["TOKEN"] != "t" || second["REGION"] != "" {
		t.Fatalf("an empty secret keeps the stored one, an empty plain value clears: %v %v", second, err)
	}
	if !Configured(env, second) || Configured(env, map[string]string{}) {
		t.Fatal("Configured")
	}
}
