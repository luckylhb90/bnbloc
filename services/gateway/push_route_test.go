package gateway

import (
	"net/http"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/rbac"
)

// N4: the browser push routes go to realtime, and a member's own
// subscription is writable by a viewer (personal, not cost-bearing).
func TestWebPushRoutes(t *testing.T) {
	s := &Service{Backends: Backends{Room: "room", Realtime: "rt", Activity: "act", ShareAgent: "share"}}
	cases := map[string]string{
		"/v1/push/vapid-public-key":         "rt",
		"/v1/my/push-subscriptions":         "rt",
		"/v1/my/push-subscriptions/extra-x": "",
	}
	for path, want := range cases {
		if got := s.route(mustParseAPIPath(t, http.MethodGet, path)); got != want {
			t.Errorf("route(%s) = %q, want %q", path, got, want)
		}
	}
	for _, method := range []string{http.MethodPost, http.MethodDelete} {
		if p := requiredPermission(method, mustParseAPIPath(t, method, "/v1/my/push-subscriptions")); p != rbac.PermRead {
			t.Errorf("%s /v1/my/push-subscriptions needs %s, want read", method, p)
		}
	}
}
