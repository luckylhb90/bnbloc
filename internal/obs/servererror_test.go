package obs

import (
	"bytes"
	"encoding/json"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestMiddlewareLogs5xxWithCause(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	h := Middleware(logger, NewMetrics("test"), http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/caused":
			ServerError(w, "list rooms failed", errors.New("db down"))
			http.Error(w, "list rooms failed", http.StatusInternalServerError)
		case "/silent":
			w.WriteHeader(http.StatusBadGateway)
		default:
			w.WriteHeader(http.StatusOK)
		}
	}))
	line := func(path string) map[string]any {
		buf.Reset()
		h.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest(http.MethodGet, path, nil))
		var m map[string]any
		if err := json.Unmarshal(buf.Bytes(), &m); err != nil {
			t.Fatalf("%s: log line %q: %v", path, buf.String(), err)
		}
		return m
	}
	if m := line("/caused"); m["level"] != "ERROR" || m["event"] != ServerErrorEvent || m["what"] != "list rooms failed" || m["error"] != "db down" {
		t.Fatalf("caused 5xx line = %v", m)
	}
	if m := line("/silent"); m["level"] != "ERROR" || m["what"] != "unrecorded" {
		t.Fatalf("uncaused 5xx must still be an Error line: %v", m)
	}
	if m := line("/ok"); m["level"] != "INFO" || m["event"] != nil {
		t.Fatalf("2xx line = %v", m)
	}
}

// TestNoSilent5xx is the static guard of the ServerError convention: in
// every non-test Go file under services/ and internal/ (internal/obs and
// internal/httpx own the convention and are exempt), a call that passes a
// constant 5xx status — httpx.Error(w, msg, 500), w.WriteHeader(502), ... —
// must be preceded in its block by obs.ServerError (a bare slog/log line
// does not count: the request's own log line would still say
// what=unrecorded). When the write's branch is about an error (see
// branchErr), that call must name it. A status held in a variable goes
// through httpx.WriteStatus (TestErrorStatusesThroughWriteStatus).
func TestNoSilent5xx(t *testing.T) {
	problems := scanServiceSources(t, silent5xx)
	if len(problems) > 0 {
		t.Fatalf("%d 5xx response(s) without a logged cause; add obs.ServerError(w, \"<what failed>\", <err>) before the write:\n  %s",
			len(problems), strings.Join(problems, "\n  "))
	}
}

// TestErrorStatusesThroughWriteStatus: an error response whose status is
// computed at run time (a mapping helper's result, an upstream's status)
// may be a 5xx that TestNoSilent5xx cannot see, so it is written only by
// httpx.WriteStatus, which records the cause of any 5xx. An upstream's
// status is never written through as-is (w.WriteHeader(resp.StatusCode)).
func TestErrorStatusesThroughWriteStatus(t *testing.T) {
	problems := scanServiceSources(t, variableErrorStatus)
	if len(problems) > 0 {
		t.Fatalf("%d error write(s) with a run-time status; use httpx.WriteStatus(w, status, msg, err):\n  %s",
			len(problems), strings.Join(problems, "\n  "))
	}
}

// scanServiceSources runs check over every non-test Go file under
// services/ and internal/ except the convention's owners, returning
// "<file>:<finding>" sorted.
func scanServiceSources(t *testing.T, check func(path string) ([]string, error)) []string {
	t.Helper()
	root := filepath.Join("..", "..")
	owners := map[string]bool{
		filepath.Join(root, "internal", "obs"):   true,
		filepath.Join(root, "internal", "httpx"): true,
	}
	var problems []string
	for _, dir := range []string{"services", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == "testdata" || owners[path] {
					return filepath.SkipDir
				}
				return nil
			}
			if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			found, err := check(path)
			if err != nil {
				return err
			}
			rel, _ := filepath.Rel(root, path)
			for _, f := range found {
				problems = append(problems, rel+":"+f)
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	sort.Strings(problems)
	return problems
}

// variableErrorStatus returns "<line>: <call>" for every error write
// (httpx.Error, http.Error) whose status is not a constant, and every
// WriteHeader of an upstream's StatusCode.
func variableErrorStatus(path string) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, err
	}
	var out []string
	ast.Inspect(f, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		name := callName(call)
		switch {
		case (name == "httpx.Error" || name == "http.Error") && len(call.Args) == 3 && !constantStatus(call.Args[2]):
			out = append(out, strconv.Itoa(fset.Position(call.Pos()).Line)+": "+name)
		case strings.HasSuffix(name, ".WriteHeader") && len(call.Args) == 1:
			if sel, ok := call.Args[0].(*ast.SelectorExpr); ok && sel.Sel.Name == "StatusCode" {
				out = append(out, strconv.Itoa(fset.Position(call.Pos()).Line)+": "+name+"(…StatusCode)")
			}
		}
		return true
	})
	return out, nil
}

// constantStatus reports an http.StatusX selector or an integer literal.
func constantStatus(e ast.Expr) bool {
	switch v := e.(type) {
	case *ast.SelectorExpr:
		x, ok := v.X.(*ast.Ident)
		return ok && x.Name == "http" && strings.HasPrefix(v.Sel.Name, "Status")
	case *ast.BasicLit:
		return v.Kind == token.INT
	}
	return false
}

// silent5xx returns "<line>: <call>" for every unlogged 5xx write in path.
func silent5xx(path string) ([]string, error) {
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return nil, err
	}
	var out []string
	var stack []ast.Node
	ast.Inspect(f, func(n ast.Node) bool {
		if n == nil {
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)
		call, ok := n.(*ast.CallExpr)
		if !ok || !has5xxArg(call) || isLogCall(call) {
			return true
		}
		if !loggedBefore(stack, call) {
			out = append(out, strconv.Itoa(fset.Position(call.Pos()).Line)+": "+callName(call))
		}
		return true
	})
	return out, nil
}

func has5xxArg(call *ast.CallExpr) bool {
	for _, a := range call.Args {
		switch v := a.(type) {
		case *ast.SelectorExpr:
			if x, ok := v.X.(*ast.Ident); ok && x.Name == "http" && is5xxName(v.Sel.Name) {
				return true
			}
		case *ast.BasicLit:
			// A literal 5xx only counts as a status where one is written
			// (512 elsewhere is a buffer size, a length cap, ...).
			if n, err := strconv.Atoi(v.Value); err == nil && v.Kind == token.INT && n >= 500 && n <= 599 && writesStatus(call) {
				return true
			}
		}
	}
	return false
}

func writesStatus(call *ast.CallExpr) bool {
	switch callName(call) {
	case "http.Error", "httpx.Error", "httpx.WriteJSON":
		return true
	}
	return strings.HasSuffix(callName(call), ".WriteHeader")
}

func is5xxName(name string) bool {
	switch name {
	case "StatusInternalServerError", "StatusNotImplemented", "StatusBadGateway", "StatusServiceUnavailable",
		"StatusGatewayTimeout", "StatusHTTPVersionNotSupported", "StatusVariantAlsoNegotiates",
		"StatusInsufficientStorage", "StatusLoopDetected", "StatusNotExtended", "StatusNetworkAuthenticationRequired":
		return true
	}
	return false
}

// isLogCall reports a logging call: obs.ServerError, slog.*, log.*, or a
// logger method (Error/Warn/Info/Debug/Printf/Println/Print).
func isLogCall(call *ast.CallExpr) bool {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	if x, ok := sel.X.(*ast.Ident); ok {
		switch x.Name {
		case "slog", "log":
			return true
		case "obs":
			return sel.Sel.Name == "ServerError"
		case "httpx":
			// httpx.Error writes the response; WriteStatus and Unavailable
			// record their own cause.
			return recordsCause(call)
		case "http":
			return false // http.Error writes the response
		}
	}
	switch sel.Sel.Name {
	case "Error", "Warn", "Info", "Debug", "Printf", "Println", "Print":
		return true
	}
	return false
}

// recordsCause reports a call that records a 5xx's cause on the request's
// log line: obs.ServerError, or an httpx writer that calls it. A bare
// slog/log line is not one: it is a second, unstructured line, and the
// request's own line would still say what=unrecorded.
func recordsCause(call *ast.CallExpr) bool {
	switch callName(call) {
	case "obs.ServerError", "httpx.WriteStatus", "httpx.Unavailable":
		return true
	}
	return false
}

// loggedBefore reports whether a statement before the write, in the block
// that holds it, records the cause (recordsCause) naming the branch's error
// (when the write is in an `if <err> != nil` branch).
func loggedBefore(stack []ast.Node, call *ast.CallExpr) bool {
	errName := branchErr(stack)
	for i := len(stack) - 1; i >= 1; i-- {
		var list []ast.Stmt
		switch b := stack[i-1].(type) {
		case *ast.BlockStmt:
			list = b.List
		case *ast.CaseClause:
			list = b.Body
		case *ast.CommClause:
			list = b.Body
		default:
			continue
		}
		stmt, ok := stack[i].(ast.Stmt)
		if !ok {
			continue
		}
		for _, s := range list {
			if s == stmt {
				return false
			}
			if logs(s, errName) {
				return true
			}
		}
		return false
	}
	return false
}

// branchErr names the error the write's branch is about: the innermost
// enclosing `if` (then-branch) or `case` whose condition tests an error,
// else — for a switch's default — the error its other cases test, else the
// enclosing function's error parameter. "" when there is none (e.g. an
// unconfigured feature answering 503).
func branchErr(stack []ast.Node) string {
	for i := len(stack) - 1; i >= 1; i-- {
		switch p := stack[i-1].(type) {
		case *ast.IfStmt:
			if stack[i] == p.Body {
				if n := errIn(p.Cond); n != "" {
					return n
				}
			}
		case *ast.CaseClause:
			for _, e := range p.List {
				if n := errIn(e); n != "" {
					return n
				}
			}
		case *ast.SwitchStmt:
			for _, c := range p.Body.List {
				for _, e := range c.(*ast.CaseClause).List {
					if n := errIn(e); n != "" {
						return n
					}
				}
			}
		case *ast.FuncDecl:
			return errParam(p.Type)
		case *ast.FuncLit:
			return errParam(p.Type)
		}
	}
	return ""
}

// errIn returns X of the first `X != nil`, errors.Is(X, …) or
// errors.As(X, …) in cond ("" if none).
func errIn(cond ast.Expr) string {
	name := ""
	ast.Inspect(cond, func(n ast.Node) bool {
		if name != "" {
			return false
		}
		switch v := n.(type) {
		case *ast.BinaryExpr:
			if y, ok := v.Y.(*ast.Ident); ok && v.Op == token.NEQ && y.Name == "nil" {
				name = exprString(v.X)
			}
		case *ast.CallExpr:
			if fn := callName(v); (fn == "errors.Is" || fn == "errors.As") && len(v.Args) > 0 {
				name = exprString(v.Args[0])
			}
		}
		return name == ""
	})
	return name
}

func errParam(ft *ast.FuncType) string {
	for _, f := range ft.Params.List {
		if id, ok := f.Type.(*ast.Ident); ok && id.Name == "error" && len(f.Names) > 0 {
			return f.Names[0].Name
		}
	}
	return ""
}

func logs(s ast.Stmt, errName string) bool {
	es, ok := s.(*ast.ExprStmt)
	if !ok {
		return false
	}
	call, ok := es.X.(*ast.CallExpr)
	if !ok || !recordsCause(call) {
		return false
	}
	if errName == "" {
		return true
	}
	named := false
	for _, a := range call.Args {
		ast.Inspect(a, func(n ast.Node) bool {
			if e, ok := n.(ast.Expr); ok && exprString(e) == errName {
				named = true
			}
			return !named
		})
	}
	return named
}

func exprString(e ast.Expr) string {
	switch v := e.(type) {
	case *ast.Ident:
		return v.Name
	case *ast.SelectorExpr:
		return exprString(v.X) + "." + v.Sel.Name
	}
	return ""
}

func callName(call *ast.CallExpr) string {
	switch f := call.Fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return exprString(f)
	}
	return "call"
}
