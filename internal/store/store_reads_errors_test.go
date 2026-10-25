package store

import (
	"context"
	"database/sql"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// storeOp is one method of an exported store interface that takes a context.
type storeOp struct {
	Iface, Method string
	ReturnsError  bool
	Pos           token.Position
}

func (o storeOp) key() string { return o.Iface + "." + o.Method }

// storeOps parses the package's non-test sources for the methods of its
// exported interfaces whose first parameter is a context and that return a
// result: the store operations.
func storeOps(t *testing.T) []storeOp {
	t.Helper()
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var ops []storeOp
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(fset, f, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			ts, ok := n.(*ast.TypeSpec)
			if !ok {
				return true
			}
			it, ok := ts.Type.(*ast.InterfaceType)
			if !ok || !ts.Name.IsExported() {
				return true
			}
			for _, m := range it.Methods.List {
				ft, ok := m.Type.(*ast.FuncType)
				if !ok || len(m.Names) == 0 || !takesContext(ft) || ft.Results == nil {
					continue
				}
				ops = append(ops, storeOp{Iface: ts.Name.Name, Method: m.Names[0].Name, ReturnsError: returnsError(ft), Pos: fset.Position(m.Pos())})
			}
			return true
		})
	}
	return ops
}

// TestStoreReadsReturnErrors fails when a store interface operation (a method
// taking a context) returns results without an error. A read that cannot
// report a failure turns a database blip into a wrong answer ("not a
// member", "no usage", "no rows"), which is how a failed membership read
// once re-provisioned a subject and rewrote their org role.
func TestStoreReadsReturnErrors(t *testing.T) {
	var offenders []string
	for _, op := range storeOps(t) {
		if !op.ReturnsError {
			offenders = append(offenders, fmt.Sprintf("%s (%s)", op.key(), op.Pos))
		}
	}
	sort.Strings(offenders)
	if len(offenders) > 0 {
		t.Errorf("store operations must return an error (a failed read is never an empty answer):\n  %s", strings.Join(offenders, "\n  "))
	}
}

// closedDBExempt are the MySQL store operations that legitimately answer
// without touching the database on TestMySQLReadsReportFailures' minimal
// arguments, keyed <store>/<Iface.Method>, each with why.
var closedDBExempt = map[string]string{
	"Chat/ChatIndex.SearchRoomMessages": `a one-rune text is below SearchMinRunes: nothing to search`,
	"Grant/AuditLogReader.AuditLog":     `the kind filter "x" excludes borrow rows, the only ones this store holds`,
}

// TestMySQLReadsReportFailures calls every store operation of every MySQL
// store (all Backends fields, plus the stores built outside it) on a closed
// database, with a tenant context and minimal arguments, and requires the
// error result: a failed query, scan or iteration is reported, never answered
// as "absent", "empty" or zero. It runs without a database server.
func TestMySQLReadsReportFailures(t *testing.T) {
	db, err := sql.Open("mysql", "closed:closed@tcp(127.0.0.1:1)/closed?parseTime=true")
	if err != nil {
		t.Fatal(err)
	}
	_ = db.Close() // every query now fails
	b := newMySQLBackends(NewDB(db, nil, 0, 0))
	stores := map[string]any{"Retention": NewMySQLRetention(NewDB(db, nil, 0, 0))}
	bv := reflect.ValueOf(b).Elem()
	for i := 0; i < bv.NumField(); i++ {
		f := bv.Field(i)
		if f.Kind() != reflect.Interface || f.IsNil() {
			continue
		}
		stores[bv.Type().Field(i).Name] = f.Interface()
	}

	// Which operations each store's concrete type implements: every method
	// of an interface whose whole method set (by name) the type has.
	byIface := map[string][]storeOp{}
	for _, op := range storeOps(t) {
		byIface[op.Iface] = append(byIface[op.Iface], op)
	}
	covered := map[string]bool{}
	exempted := map[string]bool{}
	var failures []string
	names := make([]string, 0, len(stores))
	for n := range stores {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, name := range names {
		v := reflect.ValueOf(stores[name])
		called := map[string]bool{}
		for iface, ops := range byIface {
			implements := true
			for _, op := range ops {
				if !v.MethodByName(op.Method).IsValid() {
					implements = false
					break
				}
			}
			if !implements {
				continue
			}
			for _, op := range ops {
				covered[op.key()] = true
				if !op.ReturnsError || called[op.Method] {
					continue
				}
				called[op.Method] = true
				if _, ok := closedDBExempt[name+"/"+op.key()]; ok {
					exempted[name+"/"+op.key()] = true
					continue
				}
				if msg := callOnClosedDB(v.MethodByName(op.Method)); msg != "" {
					failures = append(failures, fmt.Sprintf("%s (%T, %s.%s): %s", name, stores[name], iface, op.Method, msg))
				}
			}
		}
	}
	sort.Strings(failures)
	if len(failures) > 0 {
		t.Errorf("store operations on a failing database must return an error:\n  %s", strings.Join(failures, "\n  "))
	}
	var uncovered []string
	for _, ops := range byIface {
		for _, op := range ops {
			if !covered[op.key()] {
				uncovered = append(uncovered, op.key())
			}
		}
	}
	sort.Strings(uncovered)
	if len(uncovered) > 0 {
		t.Errorf("store operations no MySQL store here implements; add the store to TestMySQLReadsReportFailures:\n  %s", strings.Join(uncovered, "\n  "))
	}
	for k := range closedDBExempt {
		if !exempted[k] {
			t.Errorf("closedDBExempt entry %s names no store operation; remove it", k)
		}
	}
}

// callOnClosedDB calls m with a tenant context and minimal arguments and
// returns why the call failed the contract ("" = it returned an error).
func callOnClosedDB(m reflect.Value) (msg string) {
	ctx, cancel := context.WithTimeout(tenant.NewContext(context.Background(), "t-closed"), 5*time.Second)
	defer cancel()
	mt := m.Type()
	args := []reflect.Value{reflect.ValueOf(ctx)}
	for i := 1; i < mt.NumIn(); i++ {
		in := mt.In(i)
		if mt.IsVariadic() && i == mt.NumIn()-1 {
			break
		}
		args = append(args, minimalArg(in))
	}
	defer func() {
		if r := recover(); r != nil {
			msg = fmt.Sprintf("panicked: %v", r)
		}
	}()
	out := m.Call(args)
	if last := out[len(out)-1]; last.IsNil() {
		return "returned no error"
	}
	return ""
}

// minimalArg is a non-empty value of t: "x" strings, 1 numbers, one-element
// slices, allocated pointers, and functions returning zero values.
func minimalArg(t reflect.Type) reflect.Value {
	switch t.Kind() {
	case reflect.String:
		return reflect.ValueOf("x").Convert(t)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return reflect.ValueOf(1).Convert(t)
	case reflect.Slice:
		s := reflect.MakeSlice(t, 1, 1)
		s.Index(0).Set(minimalArg(t.Elem()))
		return s
	case reflect.Pointer:
		p := reflect.New(t.Elem())
		p.Elem().Set(minimalArg(t.Elem()))
		return p
	case reflect.Map:
		return reflect.MakeMap(t)
	case reflect.Struct:
		// Fill exported fields so a request or patch is not empty (an empty
		// one is legitimately a no-op).
		s := reflect.New(t).Elem()
		for i := 0; i < t.NumField(); i++ {
			if f := s.Field(i); f.CanSet() && t.Field(i).Type != t {
				f.Set(minimalArg(t.Field(i).Type))
			}
		}
		return s
	case reflect.Func:
		return reflect.MakeFunc(t, func([]reflect.Value) []reflect.Value {
			out := make([]reflect.Value, t.NumOut())
			for i := range out {
				out[i] = reflect.Zero(t.Out(i))
			}
			return out
		})
	default:
		return reflect.Zero(t)
	}
}

func takesContext(ft *ast.FuncType) bool {
	if ft.Params == nil || len(ft.Params.List) == 0 {
		return false
	}
	sel, ok := ft.Params.List[0].Type.(*ast.SelectorExpr)
	return ok && sel.Sel.Name == "Context"
}

func returnsError(ft *ast.FuncType) bool {
	for _, r := range ft.Results.List {
		if id, ok := r.Type.(*ast.Ident); ok && id.Name == "error" {
			return true
		}
	}
	return false
}
