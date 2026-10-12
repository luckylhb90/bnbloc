package pkgmeta

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/core/protocol"
	sandboxprotocol "github.com/luckylhb90/bnbloc/sandbox/protocol"
)

// Install and runtime apply one protocol-version rule: a package is
// installable exactly when the runtime would talk to it
// (protocol.CompatibleRange). A minor bump of an adapter must never make it
// uninstallable while the runtime still accepts it.
func TestProtocolRangeMatchesRuntime(t *testing.T) {
	ranges := [][2]string{
		{"1.0", "1.0"}, {"1.0", "1.9"}, {"1.5", "1.9"}, {"0.5", "1.3"},
		{"1.0", "2.0"}, {"0.1", "0.9"}, {"2.0", "2.4"}, {"x", "1.0"},
	}
	for _, r := range ranges {
		remote := protocol.ProtocolRange{Min: r[0], Max: r[1]}
		_, coreErr := ParsePackage(validEndpointPackageBody(t, r[0], r[1]))
		if want := protocol.Compatible(remote); (coreErr == nil) != (want == nil) {
			t.Errorf("core package %v: install err = %v, runtime err = %v", r, coreErr, want)
		}
		_, sbxErr := ParsePackage(validSandboxSidecarBody(t, func(b map[string]any) {
			b["protocol"] = map[string]any{"min": r[0], "max": r[1]}
		}))
		if want := sandboxprotocol.Compatible(remote); (sbxErr == nil) != (want == nil) {
			t.Errorf("sandbox package %v: install err = %v, runtime err = %v", r, sbxErr, want)
		}
	}
	// No second copy of the rule: package validation never parses or
	// compares protocol versions itself.
	for _, f := range pkgmetaSources(t) {
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"ParseVersion(", "VersionMismatchError{"} {
			if strings.Contains(string(src), forbidden) {
				t.Errorf("%s re-implements the protocol range rule (%s); call protocol.CompatibleRange through protocol.Compatible / sandboxprotocol.Compatible", f, forbidden)
			}
		}
	}
}

// Every Core Protocol capability a daemon may advertise is installable, and
// the capability registry names every Capability* constant the protocol
// declares, so a new capability cannot be added without the install gate
// learning it.
func TestCoreCapabilitiesAcceptEveryProtocolCapability(t *testing.T) {
	for _, c := range protocol.Capabilities {
		body := validEndpointPackageBody(t, protocol.SupportedMinVersion, protocol.SupportedMaxVersion)
		body = []byte(strings.Replace(string(body), `"capabilities":["interrupt","writeFile"]`, `"capabilities":["`+c+`"]`, 1))
		if _, err := ParsePackage(body); err != nil {
			t.Errorf("capability %q refused at install: %v", c, err)
		}
	}
	if _, err := ParsePackage([]byte(strings.Replace(string(validEndpointPackageBody(t, "1.0", "1.0")), `"interrupt"`, `"mintCoins"`, 1))); err == nil {
		t.Fatal("an unknown capability installed")
	}

	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, filepath.Join("..", "protocol", "types.go"), nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	declared := map[string]bool{}
	listed := map[string]bool{}
	for _, d := range file.Decls {
		gd, ok := d.(*ast.GenDecl)
		if !ok {
			continue
		}
		for _, spec := range gd.Specs {
			vs, ok := spec.(*ast.ValueSpec)
			if !ok {
				continue
			}
			for i, name := range vs.Names {
				switch {
				case gd.Tok == token.CONST && strings.HasPrefix(name.Name, "Capability"):
					declared[name.Name] = true
				case gd.Tok == token.VAR && name.Name == "Capabilities" && i < len(vs.Values):
					ast.Inspect(vs.Values[i], func(n ast.Node) bool {
						if id, ok := n.(*ast.Ident); ok {
							listed[id.Name] = true
						}
						return true
					})
				}
			}
		}
	}
	if len(declared) == 0 {
		t.Fatal("found no Capability* constants in core/protocol/types.go")
	}
	for name := range declared {
		if !listed[name] {
			t.Errorf("protocol.%s is not in protocol.Capabilities: packages advertising it cannot install", name)
		}
	}
}

func pkgmetaSources(t *testing.T) []string {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, f := range files {
		if !strings.HasSuffix(f, "_test.go") {
			out = append(out, f)
		}
	}
	return out
}
