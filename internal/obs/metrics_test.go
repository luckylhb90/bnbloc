package obs

import (
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

// The metrics handler renders request counts, a latency histogram, and named
// counters in Prometheus text format.
func TestMetricsExposition(t *testing.T) {
	m := NewMetrics("svc")
	m.observeRequest("POST", "/v1/rooms/:id/chat", 200, 12.5)
	m.observeRequest("POST", "/v1/rooms/:id/chat", 200, 3.0)
	m.observeRequest("POST", "/v1/rooms/:id/chat", 429, 1.0)
	m.Inc("rate_limited_total")

	rec := httptest.NewRecorder()
	m.Handler()(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()

	if !strings.Contains(body,
		`bnbloc_http_requests_total{service="svc",method="POST",route="/v1/rooms/:id/chat",status="200"} 2`) {
		t.Fatalf("missing 200 request count:\n%s", body)
	}
	if !strings.Contains(body,
		`bnbloc_http_requests_total{service="svc",method="POST",route="/v1/rooms/:id/chat",status="429"} 1`) {
		t.Fatalf("missing 429 request count:\n%s", body)
	}
	// Both 200s are <= 25ms, so that bucket has 2.
	if !strings.Contains(body, `le="25"} 2`) {
		t.Fatalf("latency bucket le=25 should have 2:\n%s", body)
	}
	if !strings.Contains(body, `bnbloc_rate_limited_total{service="svc"} 1`) {
		t.Fatalf("missing named counter:\n%s", body)
	}
}

// routeLabel collapses every non-route-word segment, so ids and secrets never
// become labels and the label space is bounded by the route vocabulary (BE-3).
func TestRouteLabel(t *testing.T) {
	cases := map[string]string{
		"/v1/rooms/test/chat":           "/v1/rooms/:id/chat",
		"/v1/rooms/abc-123":             "/v1/rooms/:id",
		"/v1/rooms/chat/events":         "/v1/rooms/:id/events",
		"/v1/rooms/test/members/jack":   "/v1/rooms/:id/members/:id",
		"/v1/rooms-unread":              "/v1/rooms-unread",
		"/healthz":                      "/:id",
		"/":                             "/",
		"/v1/invites/SECRETCODE/redeem": "/v1/invites/:id/redeem",
		"/relay/stream/session-key-123": "/relay/stream/:id",
		"/v1/rooms/r/shared-agents/acme/codex/access":      "/v1/rooms/:id/shared-agents/:id/:id/access",
		"/v1/rooms/r/messages/42":                          "/v1/rooms/:id/messages/:id",
		"/v1/app/update/darwin-aarch64/1.2.3":              "/v1/app/update/:id/:id",
		"/v1/rooms/r/zzz-invented":                         "/v1/rooms/:id/:id",
		"/a/b/c/d/e/f/g/h/i/j/k":                           "/:id/:id/:id/:id/:id/:id/:id/:id/...",
		"/v1/tenant/team/agents":                           "/v1/tenant/team/agents",
		"/v1/borrow/quota-requests/qr_abc/approve":         "/v1/borrow/quota-requests/:id/approve",
		"/v1/borrow/quota-requests/decline/decline":        "/v1/borrow/quota-requests/:id/decline",
		"/v1/my/plugin-events/ack/ack":                     "/v1/my/plugin-events/:id/ack",
		"/v1/rooms/r/schedules/sch_1/run":                  "/v1/rooms/:id/schedules/:id/run",
		"/v1/rooms/r/schedules/run":                        "/v1/rooms/:id/schedules/:id",
		"/v1/room-templates/tpl_1/instantiate":             "/v1/room-templates/:id/instantiate",
		"/v1/tenant/room-templates/tpl_1":                  "/v1/tenant/room-templates/:id",
		"/v1/rooms/r/artifacts/art_1/versions/3/content":   "/v1/rooms/:id/artifacts/:id/versions/:id/content",
		"/v1/content-components":                           "/v1/content-components",
		"/v1/content-components/import":                    "/v1/content-components/import",
		"/v1/content-components/effective":                 "/v1/content-components/effective",
		"/v1/content-components/cc_1/versions/1.2.0/ack":   "/v1/content-components/:id/versions/:id/ack",
		"/v1/content-uses":                                 "/v1/content-uses",
		"/v1/artifacts/builtin/9f2c":                       "/v1/artifacts/:id/:id",
		"/v1/content-components/cc_builtin_expert-builder": "/v1/content-components/:id",
		"/v1/rooms/r/artifacts/art_1/versions/3/thumbnail": "/v1/rooms/:id/artifacts/:id/versions/:id/thumbnail",
		"/v1/rooms/r/artifacts/a/versions/3/site-ticket":   "/v1/rooms/:id/artifacts/:id/versions/:id/site-ticket",
		"/v1/public/shares/bsh_x_secret":                   "/v1/public/shares/:id",
		"/v1/public/shares/bsh_x_secret/content":           "/v1/public/shares/:id/content",
		"/v1/public/shares/bsh_x_secret/site/assets/a.js":  "/v1/public/shares/:id/site/:id/:id",
		"/v1/sites/ticket.sig/index.html":                  "/v1/sites/:id/:id",
		"/internal/v1/blobs/presign":                       "/internal/v1/blobs/:id",
		"/internal/v1/shares/resolve":                      "/internal/v1/shares/resolve",
		"/v1/platform-agents":                              "/v1/platform-agents",
		"/v1/platform/platform-agents":                     "/v1/platform/platform-agents",
		"/v1/platform/platform-agents/usage":               "/v1/platform/platform-agents/usage",
		"/v1/rooms/r/platform-agents/codex":                "/v1/rooms/:id/platform-agents/:id",
		"/v1/rooms/r/agent-catalogs":                       "/v1/rooms/:id/agent-catalogs",
		"/internal/v1/rooms/r/agent-hosts/alice":           "/internal/v1/rooms/:id/agent-hosts/:id",
		"/v1/my/profile":                                   "/v1/my/profile",
		"/internal/v1/plugin-events":                       "/internal/v1/plugin-events",
		"/internal/v1/members/maya/desktop":                "/internal/v1/members/:id/desktop",
		"/internal/v1/members/maya/notifications":          "/internal/v1/members/:id/notifications",
		"/internal/v1/rooms/r/schedules/sch_1/post":        "/internal/v1/rooms/:id/schedules/:id/post",
		"/internal/v1/rooms/r/dispatch":                    "/internal/v1/rooms/:id/dispatch",
		"/v1/rooms/r/dispatch":                             "/v1/rooms/:id/dispatch",
		// users is an id collection, so the batch lookup reads as the only
		// three-segment users route; a user named "profiles" stays an id.
		"/v1/users/profiles":       "/v1/users/:id",
		"/v1/users/profiles/rooms": "/v1/users/:id/rooms",
	}
	for in, want := range cases {
		if got := routeLabel(in); got != want {
			t.Errorf("routeLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

// BE-3: client-chosen path segments and methods never grow the label space.
func TestRouteLabelCardinalityIsBounded(t *testing.T) {
	seen := map[string]bool{}
	for i := 0; i < 1000; i++ {
		seen[routeLabel("/v1/invites/code"+strconv.Itoa(i)+"/redeem")] = true
		seen[routeLabel("/v1/rooms/r/x"+strconv.Itoa(i))] = true
		seen[methodLabel("M"+strconv.Itoa(i))] = true
	}
	if len(seen) != 3 {
		t.Fatalf("distinct labels = %d (%v), want 3", len(seen), seen)
	}
}
