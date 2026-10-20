package gateway

import (
	"context"
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// contentRouteGrants is every content route's job-grant mode (decision O6):
// a route a room tool calls acts as the job's requester; any other refuses
// ?job= outright. A new content route must be added here.
var contentRouteGrants = map[string]jobGrant{
	"GET /v1/content-components":                                   grantHonored,
	"GET /v1/components":                                           grantHonored,
	"POST /v1/content-components":                                  grantHonored,
	"POST /v1/content-components/import":                           grantHonored,
	"GET /v1/content-components/effective":                         grantHonored,
	"POST /v1/content-drafts":                                      grantHonored,
	"GET /v1/content-components/{id}":                              grantHonored,
	"POST /v1/content-components/{id}/versions":                    grantHonored,
	"GET /v1/content-components/{id}/versions/{version}":           grantHonored,
	"GET /v1/content-uses":                                         grantHonored,
	"PUT /v1/content-uses":                                         grantHonored,
	"DELETE /v1/content-uses":                                      grantHonored,
	"POST /v1/content-components/prompts":                          grantHonored,
	"POST /v1/content-components/{id}/prompts/{promptId}/use":      grantHonored,
	"POST /v1/content-components/{id}/publish":                     grantHonored,
	"DELETE /v1/content-components/{id}":                           grantRefused,
	"POST /v1/content-components/{id}/versions/{version}/ack":      grantRefused,
	"POST /v1/content-components/{id}/shares":                      grantRefused,
	"GET /v1/content-components/{id}/shares":                       grantRefused,
	"DELETE /v1/content-components/{id}/shares/rooms/{room}":       grantRefused,
	"POST /v1/content-components/{id}/update":                      grantRefused,
	"GET /v1/content-components/{id}/versions/{version}/cover":     grantRefused,
	"PUT /v1/content-components/{id}/versions/{version}/cover":     grantRefused,
	"POST /v1/content-components/{id}/versions/{version}/cover/ai": grantRefused,
	"GET /v1/content-components/{id}/versions/{version}/file":      grantRefused,
	"POST /v1/content-components/{id}/versions/{version}/demo":     grantRefused,
	"PUT /v1/content-components/{id}/favorite":                     grantRefused,
	"DELETE /v1/content-components/{id}/favorite":                  grantRefused,
	"POST /v1/content-components/{id}/use-app":                     grantRefused,
	"POST /v1/content-components/{id}/make-similar":                grantRefused,
	"POST /v1/content-components/{id}/create-room":                 grantRefused,
}

// Every content route either acts as the job's requester under a
// ?job=&room= grant or refuses it with a 400: none ignores the grant and
// acts as the calling bridge's owner. A grant is evaluated, never skipped,
// so a bridge that does not run the job (bob) is refused on every honored
// route.
func TestJobGrantHonoredOrRefused(t *testing.T) {
	registered := map[string]bool{}
	route := regexp.MustCompile(`"((?:GET|POST|PUT|PATCH|DELETE) /v1/(?:content-[^"]*|components))"`)
	files, _ := filepath.Glob("*.go")
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range route.FindAllStringSubmatch(string(raw), -1) {
			registered[m[1]] = true
		}
	}
	for p := range registered {
		if _, ok := contentRouteGrants[p]; !ok {
			t.Errorf("content route %q declares no job-grant mode (contentRouteGrants)", p)
		}
	}
	for p := range contentRouteGrants {
		if !registered[p] {
			t.Errorf("contentRouteGrants lists %q, which is not registered", p)
		}
	}

	g := newContentGateway(t)
	ctx := tenant.NewContext(context.Background(), "home")
	if err := g.jobs.PutBridgeHeartbeat(ctx, store.BridgeHeartbeat{BridgeID: "br-jack", OwnerUserID: "jack", Status: store.BridgeOnline, LastSeenAtMs: time.Now().UnixMilli()}); err != nil {
		t.Fatal(err)
	}
	if _, err := g.jobs.PutAgentJob(ctx, store.AgentJob{JobID: "job-1", RoomID: "room-1", MessageSeq: 1, RequesterUserID: "maya", OwnerUserID: "jack", Provider: "codex",
		Status: store.AgentJobRunning, LeaseBridgeID: "br-jack", CreatedAtMs: 1}); err != nil {
		t.Fatal(err)
	}
	fill := strings.NewReplacer("{id}", "cc_x", "{version}", "1.0.0", "{promptId}", "p1", "{room}", "room-1")
	for p, mode := range contentRouteGrants {
		method, path, _ := strings.Cut(p, " ")
		url := fill.Replace(path) + "?job=job-1&room=room-1"
		code, raw := g.do("bob", method, url, "application/json", []byte("{}"))
		want := http.StatusForbidden // honored: bob's bridge does not run the job
		if mode == grantRefused {
			want = http.StatusBadRequest
		}
		if code != want {
			t.Errorf("%s as another bridge with a job grant = %d %s, want %d", p, code, raw, want)
		}
	}
}

// The detail a member reads with ?room= lists bundle URLs the same ?room=
// downloads: the Library and the artifact route resolve the actor through
// one contentGrant, in the room's tenant.
func TestRoomGrantDetailBundleDownloadable(t *testing.T) {
	g := actingContentGateway(t)
	// jack (session tenant home) saves from room-b: his component lands in
	// the room's tenant org-b.
	form, _ := json.Marshal(expertForm("in b"))
	code, raw := g.doWith("jack", http.MethodPost, "/v1/content-components?room=room-b", nil, "application/json", form)
	if code != http.StatusCreated {
		t.Fatalf("create = %d %s", code, raw)
	}
	c := decodeDetail(t, raw).Component
	if got := g.storedTenant(c.ID); got != "org-b" {
		t.Fatalf("stored in %q, want org-b", got)
	}
	// Every bundle URL a detail read from the room lists downloads with the
	// same ?room=, for each reader that may open it there.
	downloadable := func(user string) {
		t.Helper()
		code, raw := g.do(user, http.MethodGet, "/v1/content-components/"+c.ID+"?room=room-b", "", nil)
		if code != http.StatusOK {
			t.Fatalf("%s: detail from the room = %d %s", user, code, raw)
		}
		detail := decodeDetail(t, raw)
		if len(detail.Versions) == 0 {
			t.Fatalf("%s: no versions", user)
		}
		for _, v := range detail.Versions {
			if code, raw := g.do(user, http.MethodGet, v.BundleURL+"?room=room-b", "", nil); code != http.StatusOK {
				t.Fatalf("%s: bundle %s with ?room=room-b = %d %s", user, v.BundleURL, code, raw)
			}
		}
	}
	// Unpinned, read from room-b in the room's tenant: the download is
	// answered in the same tenant (it used to be checked in jack's session
	// tenant, and 404ed).
	downloadable("jack")
	// maya is not in room-b: the same download is a 404.
	_, raw = g.do("jack", http.MethodGet, "/v1/content-components/"+c.ID+"?room=room-b", "", nil)
	if code, _ := g.do("maya", http.MethodGet, decodeDetail(t, raw).Versions[0].BundleURL+"?room=room-b", "", nil); code != http.StatusNotFound {
		t.Fatalf("download out of the room's reach = %d, want 404", code)
	}
}

// The effective tenant has one owner (tenant_scope.go): no other gateway
// file reads the acting-tenant header or resolves a room's tenant itself,
// and no file reads a ?tenantId= override (the header is the only input).
func TestEffectiveTenantSingleOwner(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	var problems []string
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		owner := f == "tenant_scope.go"
		file, err := parser.ParseFile(fset, f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			at := func(why string) { problems = append(problems, fset.Position(n.Pos()).String()+": "+why) }
			switch x := n.(type) {
			case *ast.BasicLit:
				if v, err := strconv.Unquote(x.Value); err == nil && x.Kind == token.STRING && strings.Contains(v, "X-Bnbloc-Acting-Tenant") && !owner {
					at("acting-tenant header spelled out")
				}
			case *ast.Ident:
				if x.Name == "roomTenantOr" && !owner {
					at("roomTenantOr outside its named variants")
				}
			case *ast.CallExpr:
				sel, ok := x.Fun.(*ast.SelectorExpr)
				if !ok {
					return true
				}
				switch {
				case sel.Sel.Name == "RoomTenant" && !owner:
					at("room tenant resolved outside tenant_scope.go")
				case sel.Sel.Name == "Get" && len(x.Args) == 1:
					if id, ok := x.Args[0].(*ast.Ident); ok && id.Name == "actingTenantHeader" && !owner {
						at("acting-tenant header read")
					}
					// The header is the only acting-tenant input: no ?tenantId=
					// anywhere, the owner included.
					if lit, ok := x.Args[0].(*ast.BasicLit); ok && lit.Value == `"tenantId"` {
						if call, ok := sel.X.(*ast.CallExpr); ok {
							if q, ok := call.Fun.(*ast.SelectorExpr); ok && q.Sel.Name == "Query" {
								at("?tenantId= read")
							}
						}
					}
				}
			}
			return true
		})
	}
	sort.Strings(problems)
	if len(problems) > 0 {
		t.Fatalf("the effective tenant is decided outside tenant_scope.go:\n  %s", strings.Join(problems, "\n  "))
	}
}
