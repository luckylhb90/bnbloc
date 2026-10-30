package realtimehttp

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// TestRoomPreambleOwned: a room route resolves its room, its caller and the
// caller's membership only through room / streamRoom / roomCaller
// (room_request.go). A handler that reads the path, the caller or the
// directory itself is a second copy of the preamble (the review found about
// a dozen, two of them missing the visitor check).
func TestRoomPreambleOwned(t *testing.T) {
	// Functions that read the path room or the caller for a reason other
	// than a member's room request, with why.
	allowed := map[string]string{
		"room_request.go:roomCaller":            "the owner",
		"room_request.go:requireSubject":        "the owner",
		"service.go:handleInternalPublish":      "a service route (svcauth.RequireService): no member caller",
		"app_events.go:handleInternalAppEvents": "the gateway's route (svcauth.RequireService): no member caller",
		"service.go:handleInternalHistory":      "the gateway's consented read (svcauth.RequireService): no member caller",
		"service.go:handleUnread":               "reads across the rooms the caller names, each checked with member",
		"service.go:handleEvents":               "the open stream's re-check at every heartbeat",
		"chat_send.go:PostChat":                 "the operation's own Chat check of the author, so no post path skips it",
		"dispatch.go:checkDispatchAuthority":    "the dispatch guards: the bridge owner and the parent job's requester",
	}
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	var problems []string
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		src, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		file, err := parser.ParseFile(token.NewFileSet(), f, src, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, d := range file.Decls {
			fn, ok := d.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			key := f + ":" + fn.Name.Name
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				x, _ := sel.X.(*ast.Ident)
				var what string
				switch {
				case x != nil && x.Name == "roomctx" && sel.Sel.Name == "Normalize":
					what = "roomctx.Normalize"
				case x != nil && x.Name == "svcauth" && sel.Sel.Name == "Subject":
					what = "svcauth.Subject"
				case x != nil && x.Name == "membership" && (sel.Sel.Name == "RoomScope" || sel.Sel.Name == "FreshRoomScope"):
					what = "membership." + sel.Sel.Name
				}
				if what != "" && allowed[key] == "" && !strings.HasPrefix(key, "room_request.go:") {
					problems = append(problems, key+": "+what)
				}
				return true
			})
		}
	}
	sort.Strings(problems)
	if len(problems) > 0 {
		t.Fatalf("a room route re-derives its preamble (use s.room / s.streamRoom / s.roomCaller):\n  %s", strings.Join(problems, "\n  "))
	}
}
