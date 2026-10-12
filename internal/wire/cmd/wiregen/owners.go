package main

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/rbac"
	"github.com/luckylhb90/bnbloc/internal/store"
)

// enumSource is a named string type owned outside internal/wire whose
// constants the web needs as a union. A wire field names it with
// `tstype:"<TS>"`; the values are read from the owner's source, so a new
// constant there reaches the web on the next generation.
type enumSource struct {
	dir    string // package directory, relative to the module root
	goType string
	ts     string
	doc    string
}

var enumSources = []enumSource{
	{"internal/store", "AgentJobStatus", "AgentJobStatus", "An agent job's lifecycle status (store.AgentJobStatus)."},
	{"internal/store", "AgentJobFailureReason", "AgentJobFailureReason", "Why an agent job failed (store.AgentJobFailureReason)."},
	{"internal/store", "Role", "RoomRole", "A member's role in a room (store.Role)."},
	{"internal/store", "Visibility", "RoomVisibility", "How a room is discovered and joined (store.Visibility)."},
	{"internal/store", "SyncMode", "RoomSyncMode", "How a room's members converge their code (store.SyncMode)."},
	{"internal/rbac", "Role", "OrgRole", "An organization role as the session carries it (rbac.Role)."},
	{"internal/rbac", "Permission", "OrgPermission", "A coarse organization permission (rbac.Permission)."},
}

type enum struct {
	enumSource
	values []string // Go string values, in source order
}

type enums []enum

func (es enums) names() []string {
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.ts)
	}
	return out
}

func (es enums) values(ts string) []string {
	for _, e := range es {
		if e.ts == ts {
			return e.values
		}
	}
	panic("wiregen: unknown enum " + ts)
}

// registryEnums are value sets a Go registry holds at runtime (no constant
// group to read): the content kinds contentpkg registers.
func registryEnums() enums {
	var kinds []string
	for _, k := range contentpkg.Kinds() {
		kinds = append(kinds, k.Short)
	}
	return enums{{enumSource{goType: "contentpkg.Kinds()", ts: "ContentKind", doc: "A content component kind's short name (the contentpkg registry)."}, kinds}}
}

// externalEnums reads every enumSource's constants from its package source.
func externalEnums(root string) (enums, error) {
	out := make(enums, 0, len(enumSources))
	for _, src := range enumSources {
		vals, err := typedConsts(filepath.Join(root, src.dir), src.goType)
		if err != nil {
			return nil, err
		}
		if len(vals) == 0 {
			return nil, fmt.Errorf("%s.%s has no constants", src.dir, src.goType)
		}
		out = append(out, enum{src, vals})
	}
	return out, nil
}

// typedConsts lists the string values of the constants declared with type
// goType in the package's (non-test) source, in source order.
func typedConsts(dir, goType string) ([]string, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)
	fset := token.NewFileSet()
	var out []string
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			return nil, err
		}
		for _, decl := range f.Decls {
			gd, ok := decl.(*ast.GenDecl)
			if !ok || gd.Tok != token.CONST {
				continue
			}
			for _, spec := range gd.Specs {
				vs := spec.(*ast.ValueSpec)
				id, ok := vs.Type.(*ast.Ident)
				if !ok || id.Name != goType {
					continue
				}
				for _, v := range vs.Values {
					lit, ok := v.(*ast.BasicLit)
					if !ok || lit.Kind != token.STRING {
						return nil, fmt.Errorf("%s: a %s constant must be a string literal", fset.Position(v.Pos()), goType)
					}
					s, err := strconv.Unquote(lit.Value)
					if err != nil {
						return nil, err
					}
					out = append(out, s)
				}
			}
		}
	}
	return out, nil
}

func (es enums) render() []byte {
	var b bytes.Buffer
	b.WriteString("\n// ---- enums owned outside internal/wire ----\n")
	for _, e := range es {
		fmt.Fprintf(&b, "// %s\n", e.doc)
		fmt.Fprintf(&b, "export type %s = %s;\n", e.ts, union(e.values))
		fmt.Fprintf(&b, "export const %sValues: readonly %s[] = [%s];\n", e.ts, e.ts, strings.Join(quoteAll(e.values), ", "))
	}
	return b.Bytes()
}

// ownerTables renders rule tables computed by calling their Go owners, so the
// web asks the same question the server answers instead of re-deriving it.
func ownerTables(es enums) ([]byte, error) {
	var b bytes.Buffer
	b.WriteString("\n// ---- tables computed by their Go owners ----\n")

	var terminal []string
	for _, s := range es.values("AgentJobStatus") {
		if store.AgentJobStatus(s).Terminal() {
			terminal = append(terminal, s)
		}
	}
	b.WriteString("// The job statuses store.AgentJobStatus.Terminal reports finished; every\n// other status is live (queued or running).\n")
	fmt.Fprintf(&b, "export const AgentJobTerminalStatuses: readonly AgentJobStatus[] = [%s];\n", strings.Join(quoteAll(terminal), ", "))

	var inviters []string
	for _, r := range es.values("RoomRole") {
		if store.Role(r).CanInvite() {
			inviters = append(inviters, r)
		}
	}
	b.WriteString("// The room roles store.Role.CanInvite admits: they invite and remove\n// members and manage the room's content.\n")
	fmt.Fprintf(&b, "export const RoomRolesThatInvite: readonly RoomRole[] = [%s];\n", strings.Join(quoteAll(inviters), ", "))

	roles := es.values("OrgRole")
	perms := es.values("OrgPermission")
	grants := func(role string) []string {
		var out []string
		for _, p := range perms {
			if rbac.Can(role, rbac.Permission(p)) {
				out = append(out, p)
			}
		}
		return out
	}
	b.WriteString("// What each organization role may do (rbac.Can). The empty role is a\n// session minted without one.\n")
	b.WriteString("export const OrgRolePermissions: Readonly<Record<OrgRole | \"\", readonly OrgPermission[]>> = {\n")
	for _, r := range append([]string{""}, roles...) {
		fmt.Fprintf(&b, "  %s: [%s],\n", propKey(r), strings.Join(quoteAll(grants(r)), ", "))
	}
	b.WriteString("};\n")
	unknown := string(rbac.Normalize("\x00not-a-role"))
	b.WriteString("// The role an unrecognized role string acts as (rbac.Normalize).\n")
	fmt.Fprintf(&b, "export const OrgRoleUnknown: OrgRole = %s;\n", strconv.Quote(unknown))
	if !contains(roles, unknown) {
		return nil, fmt.Errorf("rbac.Normalize maps an unknown role to %q, which is not an rbac.Role constant", unknown)
	}
	return b.Bytes(), nil
}

func union(vals []string) string { return strings.Join(quoteAll(vals), " | ") }

func quoteAll(vals []string) []string {
	out := make([]string, len(vals))
	for i, v := range vals {
		out[i] = strconv.Quote(v)
	}
	return out
}

func propKey(s string) string {
	if s == "" {
		return `""`
	}
	return propName(s)
}

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
