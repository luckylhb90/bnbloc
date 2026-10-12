package pkgmeta

import (
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/archtest"
)

// The reserved room tools name is refused at install, not only when a
// bridge attaches a tool.
func TestMCPToolNameRule(t *testing.T) {
	for name, ok := range map[string]bool{
		"codeword": true, "acme_search": true, "acme.search-2": true,
		ReservedMCPServerName: false, "Acme": false, "../x": false, "": false, "a/b": false, "-x": false,
	} {
		if got := ValidMCPToolName(name) == nil; got != ok {
			t.Errorf("ValidMCPToolName(%q) ok = %v, want %v", name, got, ok)
		}
	}
	_, err := ParsePackage(mcpStdioBody(t, func(b map[string]any) { b["name"] = ReservedMCPServerName }))
	if err == nil || !strings.Contains(err.Error(), "reserved") {
		t.Fatalf("installing an MCP tool named %q: err = %v", ReservedMCPServerName, err)
	}
	if _, err := ParsePackage(mcpStdioBody(t, func(b map[string]any) { b["name"] = "acme_search" })); err != nil {
		t.Fatalf("acme_search: %v", err)
	}
}

func TestUnsetEnv(t *testing.T) {
	values := map[string]string{"A": "x", "B": "", "C": "  \t", "D": "y"}
	if got := UnsetEnv([]string{"A", "B", "C", "E", "D"}, values); !slices.Equal(got, []string{"B", "C", "E"}) {
		t.Fatalf("UnsetEnv = %v (a blank value is not set)", got)
	}
	if got := RequiredEnv([]MCPEnv{{Name: "A", Required: true}, {Name: "B"}, {Name: "C", Required: true}}); !slices.Equal(got, []string{"A", "C"}) {
		t.Fatalf("RequiredEnv = %v", got)
	}
}

func TestEnvNameRule(t *testing.T) {
	for name, ok := range map[string]bool{"TOKEN": true, "A_1": true, "token": false, "_A": false, "1A": false, "A-B": false, "": false} {
		if ValidEnvName(name) != ok {
			t.Errorf("ValidEnvName(%q) != %v", name, ok)
		}
	}
}

func TestPackageVersionIsStrictSemver(t *testing.T) {
	for v, ok := range map[string]bool{
		"1.0.0": true, "1.2.3-rc.1": true, "1.2.3+build.5": true, "1.2.3-rc.1+b": true,
		"v1.0.0": false, " 1.0.0": false, "01.0.0": false, "1.0": false, "1.0.0+": false, "1.0.0-": false, "1.0.0+a..b": false,
	} {
		_, err := ParsePackage(mcpStdioBody(t, func(b map[string]any) { b["version"] = v }))
		if (err == nil) != ok {
			t.Errorf("version %q: err = %v, want ok=%v", v, err, ok)
		}
	}
}

// The MCP name, env name, required-env and semver rules each have one
// owner: no other non-test file re-implements them.
func TestPackageRulesSingleOwner(t *testing.T) {
	forbidden := []struct {
		re   *regexp.Regexp
		what string
	}{
		{regexp.MustCompile(`\.Required && [^\n]*(== ""|!= "")`), "a required-env check (pkgmeta.UnsetEnv)"},
		{regexp.MustCompile(`[!=]= (roomtools\.)?(ServerName|ReservedMCPServerName)\b`), "the reserved MCP name (pkgmeta.ValidMCPToolName)"},
		{regexp.MustCompile(`\[0-9\]\+\\\.\[0-9\]\+\\\.\[0-9\]\+`), "a semver regexp (semver.Valid)"},
		{regexp.MustCompile("`\\^\\[A-Z\\]\\[A-Z0-9_\\]\\*\\$`"), "an env name regexp (pkgmeta.ValidEnvName)"},
	}
	owners := map[string]bool{"core/pkgmeta/mcp_validation.go": true, "core/pkgmeta/sidecar_validation.go": true}
	archtest.Scan(t, archtest.Opts{Lang: archtest.Go}, func(rel string, src []byte) {
		if owners[rel] {
			return
		}
		for i, line := range strings.Split(string(src), "\n") {
			if s := strings.TrimSpace(line); strings.HasPrefix(s, "//") {
				continue
			}
			for _, f := range forbidden {
				if f.re.MatchString(line) {
					t.Errorf("%s:%d re-implements %s: %s", rel, i+1, f.what, strings.TrimSpace(line))
				}
			}
		}
	})
}
