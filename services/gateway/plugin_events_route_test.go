package gateway

import (
	"net/http"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/rbac"
)

// G1: the member plugin hook stream and its acks go to realtime (the hook
// authority and auditor); a viewer may hold the stream and acknowledge.
func TestPluginEventRoutes(t *testing.T) {
	s := &Service{Backends: Backends{Room: "room", Realtime: "rt", Activity: "act", ShareAgent: "share"}}
	cases := map[string]string{
		"/v1/my/plugin-events":              "rt",
		"/v1/my/plugin-events/abc-1/ack":    "rt",
		"/v1/my/plugin-events/abc-1":        "",
		"/v1/my/plugin-events/abc-1/ack/x":  "",
		"/v1/my/plugin-events/abc-1/replay": "",
	}
	for path, want := range cases {
		if got := s.route(mustParseAPIPath(t, http.MethodGet, path)); got != want {
			t.Errorf("route(%s) = %q, want %q", path, got, want)
		}
	}
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/v1/my/plugin-events"},
		{http.MethodPost, "/v1/my/plugin-events/abc-1/ack"},
	} {
		if p := requiredPermission(c.method, mustParseAPIPath(t, c.method, c.path)); p != rbac.PermRead {
			t.Errorf("%s %s needs %s, want read", c.method, c.path, p)
		}
	}
}
