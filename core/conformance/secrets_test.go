package conformance

// TestSecretsRedacted lives here because this is a package whose tests can
// import the core seam, the Core Protocol and the Sandbox Protocol together.

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"log/slog"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/core/protocol"
	sandbox "github.com/luckylhb90/bnbloc/sandbox/protocol"
)

const secretSentinel = "SECRET-7f3a"

// secretBearing is one instance of every type that carries a secret, per
// package directory. TestSecretsRedacted derives the required set from the
// sources, so a new secret-bearing type fails until it is listed here (and
// has a LogValue).
var secretBearing = map[string][]any{
	"core": {
		core.Endpoint{}, core.SandboxExecution{}, core.Execution{}, core.SessionOptions{},
		core.SessionRequest{}, &core.AgentProfile{}, core.MCPServer{}, core.NameValue{},
	},
	"core/protocol": {
		protocol.SandboxExecution{}, protocol.Execution{}, protocol.CreateSessionRequest{},
		protocol.SendInputRequest{}, protocol.AgentProfile{}, protocol.McpServer{}, protocol.NameValue{},
	},
	"sandbox/protocol": {
		sandbox.ExecToken{}, sandbox.SecretRequest{}, sandbox.CreateMachineRequest{},
		sandbox.SpaceRequest{}, sandbox.Space{}, sandbox.ListSpacesResponse{}, sandbox.ExecRequest{},
	},
}

// Every type that carries a token, an env variable, a header or a secret
// binding (directly or through a field) implements slog.LogValuer, and
// logging a fully populated value, alone or nested, never prints a secret.
func TestSecretsRedacted(t *testing.T) {
	for dir, instances := range secretBearing {
		want := secretBearingTypes(t, filepath.Join("..", "..", dir))
		var got []string
		for _, v := range instances {
			got = append(got, reflect.Indirect(reflect.ValueOf(v)).Type().Name())
		}
		sort.Strings(got)
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Errorf("%s: secret-bearing types %v, listed %v: list every one (and give it a LogValue)", dir, want, got)
		}
		for _, v := range instances {
			if _, ok := v.(slog.LogValuer); !ok {
				t.Errorf("%s.%T has no LogValue", dir, v)
				continue
			}
			filled := reflect.New(reflect.Indirect(reflect.ValueOf(v)).Type())
			fill(filled.Elem(), "")
			value := filled.Interface()
			if reflect.ValueOf(v).Kind() != reflect.Pointer {
				value = filled.Elem().Interface()
			}
			for _, h := range []func(*bytes.Buffer) slog.Handler{
				func(b *bytes.Buffer) slog.Handler { return slog.NewTextHandler(b, nil) },
				func(b *bytes.Buffer) slog.Handler { return slog.NewJSONHandler(b, nil) },
			} {
				var buf bytes.Buffer
				slog.New(h(&buf)).Info("x", "v", value, slog.Group("nested", "v", value))
				if strings.Contains(buf.String(), secretSentinel) {
					t.Errorf("%s.%T leaked a secret: %s", dir, v, buf.String())
				}
				if !strings.Contains(buf.String(), "v") {
					t.Errorf("%s.%T logged nothing: %s", dir, v, buf.String())
				}
			}
		}
	}
}

// secretBearingTypes lists the struct types of a package that have a
// core.SecretLogField field, or a field (through pointers, slices and maps)
// of such a type of the same package.
func secretBearingTypes(t *testing.T, dir string) []string {
	t.Helper()
	pkgs, err := parser.ParseDir(token.NewFileSet(), dir, func(fi fs.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, 0)
	if err != nil {
		t.Fatal(err)
	}
	structs := map[string]*ast.StructType{}
	for _, pkg := range pkgs {
		for _, f := range pkg.Files {
			for _, decl := range f.Decls {
				gd, ok := decl.(*ast.GenDecl)
				if !ok || gd.Tok != token.TYPE {
					continue
				}
				for _, spec := range gd.Specs {
					ts := spec.(*ast.TypeSpec)
					if st, ok := ts.Type.(*ast.StructType); ok && ts.Name.IsExported() {
						structs[ts.Name.Name] = st
					}
				}
			}
		}
	}
	secret := map[string]bool{}
	for changed := true; changed; {
		changed = false
		for name, st := range structs {
			if secret[name] {
				continue
			}
			for _, field := range st.Fields.List {
				hit := secret[baseIdent(field.Type)]
				for _, n := range field.Names {
					hit = hit || core.SecretLogField(n.Name)
				}
				if hit {
					secret[name], changed = true, true
					break
				}
			}
		}
	}
	out := make([]string, 0, len(secret))
	for name := range secret {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// baseIdent strips pointers, slices and map values down to a local type name.
func baseIdent(e ast.Expr) string {
	for {
		switch x := e.(type) {
		case *ast.StarExpr:
			e = x.X
		case *ast.ArrayType:
			e = x.Elt
		case *ast.MapType:
			e = x.Value
		case *ast.Ident:
			return x.Name
		default:
			return ""
		}
	}
}

// fill sets every field of v: a field whose name carries a secret (and every
// value of an env or header map / list) gets the sentinel, anything else a
// plain non-zero value, so a redaction miss shows in the log.
func fill(v reflect.Value, field string) {
	secret := core.SecretLogField(field)
	switch v.Kind() {
	case reflect.String:
		if secret {
			v.SetString(secretSentinel)
		} else {
			v.SetString("x")
		}
	case reflect.Bool:
		v.SetBool(true)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64:
		v.SetInt(1)
	case reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		v.SetUint(1)
	case reflect.Pointer:
		v.Set(reflect.New(v.Type().Elem()))
		fill(v.Elem(), field)
	case reflect.Struct:
		for i := 0; i < v.NumField(); i++ {
			if v.Type().Field(i).IsExported() {
				fill(v.Field(i), v.Type().Field(i).Name)
			}
		}
	case reflect.Slice:
		s := reflect.MakeSlice(v.Type(), 1, 1)
		fill(s.Index(0), field)
		v.Set(s)
	case reflect.Map:
		m := reflect.MakeMap(v.Type())
		k := reflect.New(v.Type().Key()).Elem()
		fill(k, "")
		val := reflect.New(v.Type().Elem()).Elem()
		fill(val, field)
		m.SetMapIndex(k, val)
		v.Set(m)
	case reflect.Interface:
		if secret {
			v.Set(reflect.ValueOf(secretSentinel))
		} else {
			v.Set(reflect.ValueOf("x"))
		}
	}
}
