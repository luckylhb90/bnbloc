package bridge

import (
	"bytes"
	"context"
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
)

// The bridge's config never prints a credential: the gateway and refresh
// tokens, the sync secret and the sandbox admin bearer.
func TestConfigLogRedactsSecrets(t *testing.T) {
	cfg := Config{GatewayBaseURL: "https://bnbloc.example.com", OwnerSubject: "maya", GatewayToken: "gw-secret-1",
		RefreshToken: "refresh-secret-2", SyncSecret: "sync-secret-3",
		Sandbox: SandboxConfig{Mode: SandboxBorrowed, URL: "http://127.0.0.1:9", Token: "sandbox-secret-4", VMExec: "/v", Machine: "m", WorkspacesRoot: "/w"}}
	var buf bytes.Buffer
	slog.New(slog.NewJSONHandler(&buf, nil)).Info("bridge config", "config", cfg)
	for _, secret := range []string{"gw-secret-1", "refresh-secret-2", "sync-secret-3", "sandbox-secret-4"} {
		if strings.Contains(buf.String(), secret) {
			t.Fatalf("a secret reached the log: %s", buf.String())
		}
	}
	if !strings.Contains(buf.String(), `"gatewayToken":true`) {
		t.Fatalf("the log should still say a token is set: %s", buf.String())
	}
}

// Every gateway request goes through one sender: an attachment download
// answers a structured gatewayError (a 404 is a gone attachment, not text
// to parse).
func TestGatewayRequestsShareOneSender(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/rooms/r/attachments/gone":
			http.Error(w, "attachment not found", http.StatusNotFound)
		case "/v1/rooms/r/attachments/a":
			if r.Header.Get("Authorization") != "Bearer t" {
				http.Error(w, "unauthorized", http.StatusUnauthorized)
				return
			}
			w.Header().Set("Content-Type", "image/png")
			w.Header().Set("Content-Disposition", `inline; filename="shot.png"`)
			_, _ = w.Write([]byte("png"))
		}
	}))
	defer srv.Close()
	b := New(Config{GatewayBaseURL: srv.URL, GatewayToken: "t", OwnerSubject: "maya"})
	data, ct, name, err := b.gatewayGETAttachment(context.Background(), "/v1/rooms/r/attachments/a")
	if err != nil || string(data) != "png" || ct != "image/png" || name != "shot.png" {
		t.Fatalf("attachment = %q %q %q %v", data, ct, name, err)
	}
	_, _, _, err = b.gatewayGETAttachment(context.Background(), "/v1/rooms/r/attachments/gone")
	var ge *gatewayError
	if !errors.As(err, &ge) || ge.Status != http.StatusNotFound {
		t.Fatalf("missing attachment error = %v, want a gatewayError 404", err)
	}

	// Statically: only send builds a request (and the room stream, which
	// reads an unbounded SSE body by design).
	fset := token.NewFileSet()
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Name.Name == "send" || fn.Name.Name == "consumeRoom" {
				continue
			}
			ast.Inspect(fn, func(n ast.Node) bool {
				if sel, ok := n.(*ast.SelectorExpr); ok && (sel.Sel.Name == "NewRequestWithContext" || sel.Sel.Name == "NewRequest") {
					if id, ok := sel.X.(*ast.Ident); ok && id.Name == "http" {
						t.Errorf("%s: %s builds its own gateway request (use b.send / doRaw / do)", fset.Position(sel.Pos()), fn.Name.Name)
					}
				}
				return true
			})
		}
	}
}
