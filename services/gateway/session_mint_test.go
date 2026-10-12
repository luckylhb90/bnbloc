package gateway

import (
	"bytes"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/rbac"
)

// A member's tenant switch persists on the sign-in's refresh family (its
// tenant hint, decision O5): the next refresh, and a bridge's fork of the
// sign-in, land in the switched tenant with the role there.
func TestTenantSwitchPersistsAcrossRefreshAndFork(t *testing.T) {
	gw, srv, _ := refreshGateway(t)
	gw.TenantResolver = func(id auth.Identity, _ string) (string, string) {
		if id.Tenant == "acme" {
			return "acme", "org_admin"
		}
		return "home", "member"
	}
	gw.TenantMemberRole = func(tenantID, userID string) (string, bool) {
		return "org_admin", tenantID == "acme" && userID == "alice"
	}
	g := loginGrant(t, srv.URL)
	if g.Tenant != "home" {
		t.Fatalf("login tenant = %q", g.Tenant)
	}
	body, _ := json.Marshal(map[string]string{"tenantId": "acme"})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/auth/switch-tenant", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+g.Token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var switched sessionGrant
	_ = json.NewDecoder(resp.Body).Decode(&switched)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || switched.Tenant != "acme" {
		t.Fatalf("switch = %d %+v", resp.StatusCode, switched)
	}
	claims, err := gw.Signer.Verify(switched.Token)
	if err != nil || claims.Family == "" || claims.Tenant != "acme" || claims.Role != "org_admin" {
		t.Fatalf("switched session = %+v %v (family kept, tenant acme)", claims, err)
	}
	for _, path := range []string{"/v1/auth/refresh/fork", "/v1/auth/refresh"} {
		code, next := refreshWith(t, srv.URL, path, g.RefreshToken)
		if code != http.StatusOK || next.Tenant != "acme" || next.Role != "org_admin" {
			t.Fatalf("%s after the switch = %d %+v, want acme/org_admin", path, code, next)
		}
	}
}

func TestMintAdminToken(t *testing.T) {
	now := time.Now()
	tok, err := MintAdminToken("Admin@Corp.example", []string{"admin@corp.example"}, "signing-key", now)
	if err != nil {
		t.Fatal(err)
	}
	c, err := auth.NewSigner([]byte("signing-key")).Verify(tok)
	if err != nil || c.Role != string(rbac.RoleSuperAdmin) || c.ExpiresAtUnix > now.Add(bootstrapTokenTTL).Unix() {
		t.Fatalf("minted claims = %+v err=%v", c, err)
	}
	if _, err := MintAdminToken("mallory", []string{"admin@corp.example"}, "signing-key", now); err == nil {
		t.Fatal("minted a token for an unlisted subject")
	}
	if _, err := MintAdminToken("admin@corp.example", []string{"admin@corp.example"}, "", now); err == nil {
		t.Fatal("minted a token without a stable signing key")
	}
	// Subjects are case-sensitive: a case variant of a listed subject is
	// another identity.
	if _, err := MintAdminToken("Admin@Corp.example", []string{"admin@corp.example"}, "signing-key", now); err == nil {
		t.Fatal("minted a token for a case variant of a listed subject")
	}
}

// Every session bearer is minted by mintSession: an Issue of auth.Claims
// without a Kind (a session, not a refresh token or a ticket) anywhere else
// in the gateway is a second minting path (gw H5).
func TestSessionsMintedOnlyByMintSession(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	cmd, _ := filepath.Glob("cmd/*.go")
	files = append(files, cmd...)
	fset := token.NewFileSet()
	var problems []string
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
			if !ok || fn.Body == nil || fn.Name.Name == "mintSession" {
				continue
			}
			ast.Inspect(fn.Body, func(n ast.Node) bool {
				call, ok := n.(*ast.CallExpr)
				if !ok {
					return true
				}
				sel, ok := call.Fun.(*ast.SelectorExpr)
				if !ok || sel.Sel.Name != "Issue" || len(call.Args) != 1 {
					return true
				}
				lit, ok := call.Args[0].(*ast.CompositeLit)
				if !ok {
					return true
				}
				for _, el := range lit.Elts {
					if kv, ok := el.(*ast.KeyValueExpr); ok {
						if k, ok := kv.Key.(*ast.Ident); ok && k.Name == "Kind" {
							return true
						}
					}
				}
				problems = append(problems, fset.Position(call.Pos()).String()+" in "+fn.Name.Name)
				return true
			})
		}
	}
	if len(problems) > 0 {
		t.Fatalf("session tokens minted outside mintSession:\n  %s", strings.Join(problems, "\n  "))
	}
}
