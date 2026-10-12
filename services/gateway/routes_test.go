package gateway

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/rbac"
)

// readerWrites are the session routes that write yet any member, a
// read-only viewer included, may call — each with why it is not
// cost-bearing. Every other write needs more than rbac.PermRead.
var readerWrites = map[string]string{
	"DELETE /v1/my/sessions/{id}":          "a member ends one of their own sign-ins",
	"DELETE /v1/my/sessions":               "a member ends their other sign-ins",
	"POST /v1/auth/switch-tenant":          "a member re-scopes their own session to an organization they belong to",
	"POST /v1/components/{name}/install":   "a member's own opt-in to a component the rollout shows them",
	"DELETE /v1/components/{name}/install": "a member's own opt-out",
	"POST /v1/rooms/{roomId}/artifacts/{artifactId}/versions/{version}/site-ticket": "mints a read ticket for a site the member may read",
	"POST /v1/rooms/{roomId}/attachments/{attachmentId}/ticket":                     "mints a read ticket for an attachment the member may read",
	"POST /v1/content-components/{id}/versions/{version}/ack":                       "acknowledges the version of a component the member uses",
	"POST /v1/content-components/{id}/prompts/{promptId}/use":                       "counts one prompt insertion (it spends the cost budget)",
}

// publicPrefixes are where a route may take no session: sign-in, the IdP's
// SSO endpoints, and routes whose credential travels in the request (a
// share token, a ticket, a component signature).
var publicPrefixes = []string{"/v1/auth/", "/api/auth/", "/v1/public/", "/v1/sites/", "/v1/previewers/", "/v1/notifier/", "/v1/local-agent/discover"}

// TestEveryGatewayRouteGated: every route the gateway serves itself is a
// row of the route table with its edge policy, and the policy holds. The
// table is checked statically (platform routes need manage_platform,
// tenant routes manage_org, a write more than read unless listed above,
// a public route sits where a credential travels in the request), then
// walked: without a session every session route answers 401, a viewer
// gets 403 from every route needing more than read, an org admin from
// every platform route and a member from every tenant route — before any
// handler runs. Proxied routes take the same gate from their path.
func TestEveryGatewayRouteGated(t *testing.T) {
	gw := newGateway(t, "")
	seen := map[string]bool{}
	for _, rt := range gw.routes() {
		method, path, _ := strings.Cut(rt.pattern, " ")
		if seen[rt.pattern] {
			t.Errorf("%s: registered twice", rt.pattern)
		}
		seen[rt.pattern] = true
		if (rt.session == nil) == (rt.public == nil) {
			t.Errorf("%s: exactly one of a session or a public handler", rt.pattern)
			continue
		}
		if rt.public != nil {
			if rt.why == "" || !publicPath(gw, path) {
				t.Errorf("%s: a public route must say why and sit where a credential travels in the request", rt.pattern)
			}
			continue
		}
		switch {
		case strings.HasPrefix(path, "/v1/platform/") && rt.perm != rbac.PermManagePlatform:
			t.Errorf("%s: a platform route needs manage_platform, has %q", rt.pattern, rt.perm)
		case strings.HasPrefix(path, "/v1/tenant/") && rt.perm != rbac.PermManageOrg:
			t.Errorf("%s: a tenant route needs manage_org, has %q", rt.pattern, rt.perm)
		case method != http.MethodGet && rt.perm == rbac.PermRead && readerWrites[rt.pattern] == "":
			t.Errorf("%s: a write any viewer may call; give it a permission or list it in readerWrites with why", rt.pattern)
		}
	}
	for pattern := range readerWrites {
		if !seen[pattern] {
			t.Errorf("readerWrites lists %s, which is not a route", pattern)
		}
	}

	srv := httptest.NewServer(gw.Handler())
	defer srv.Close()
	token := func(role rbac.Role) string {
		return gw.Signer.Issue(auth.Claims{Subject: "vic", Tenant: "home", Role: string(role), ExpiresAtUnix: time.Now().Add(time.Hour).Unix()})
	}
	call := func(method, path, bearer string) int {
		req, _ := http.NewRequest(method, srv.URL+path, nil)
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	viewer, member, orgAdmin := token(rbac.RoleViewer), token(rbac.RoleMember), token(rbac.RoleAdmin)
	for _, rt := range gw.routes() {
		if rt.session == nil {
			continue
		}
		method, pattern, _ := strings.Cut(rt.pattern, " ")
		path := concretePath(pattern)
		if got := call(method, path, ""); got != http.StatusUnauthorized {
			t.Errorf("%s without a session = %d, want 401", rt.pattern, got)
		}
		if rt.perm != rbac.PermRead {
			if got := call(method, path, viewer); got != http.StatusForbidden {
				t.Errorf("%s as a viewer = %d, want 403", rt.pattern, got)
			}
		}
		if strings.HasPrefix(pattern, "/v1/platform/") {
			if got := call(method, path, orgAdmin); got != http.StatusForbidden {
				t.Errorf("%s as an org admin = %d, want 403", rt.pattern, got)
			}
		}
		if strings.HasPrefix(pattern, "/v1/tenant/") {
			if got := call(method, path, member); got != http.StatusForbidden {
				t.Errorf("%s as a member = %d, want 403", rt.pattern, got)
			}
		}
	}
	for _, c := range []struct {
		method, path, bearer string
	}{
		{http.MethodPost, "/v1/rooms", viewer},
		{http.MethodPost, "/v1/rooms/room-1/chat", viewer},
		{http.MethodPost, "/v1/rooms/room-1/shared-agents/x/authorization/reserve", viewer},
		{http.MethodGet, "/v1/platform/tenants", orgAdmin},
		{http.MethodGet, "/v1/tenant/audit-log", member},
	} {
		if got := call(c.method, c.path, c.bearer); got != http.StatusForbidden {
			t.Errorf("proxied %s %s = %d, want 403", c.method, c.path, got)
		}
	}
}

// TestHandlersNeverAuthenticate: a handler receives its session from the
// route table (serve) and never authenticates or checks a route's org
// permission itself; the copies (requireSuperAdminClaims, orgAdmin, the
// store-nil wrappers) drifted in status codes and fail-closed behaviour.
func TestHandlersNeverAuthenticate(t *testing.T) {
	allowed := map[string]string{
		"routes.go":     "the route table",
		"service.go":    "authenticate itself and the proxy's proxyClaims",
		"localagent.go": "dev discovery authenticates only when it is on (its row says why)",
	}
	call := regexp.MustCompile(`s\.authenticate\(r\)`)
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") || allowed[f] != "" {
			continue
		}
		raw, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		if call.Match(raw) {
			t.Errorf("%s authenticates a request itself; declare the route's policy in routes.go", f)
		}
	}
}

var pathParam = regexp.MustCompile(`\{[^}]+\}`)

// concretePath fills a pattern's wildcards with a placeholder segment.
func concretePath(pattern string) string {
	return pathParam.ReplaceAllString(pattern, "x")
}

func publicPath(gw *Service, path string) bool {
	if path == gw.publicACSPath() {
		return true
	}
	for _, p := range publicPrefixes {
		if strings.HasPrefix(path, p) {
			return true
		}
	}
	return false
}
