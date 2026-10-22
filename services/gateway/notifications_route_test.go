package gateway

import (
	"net/http"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/rbac"
)

// F5: member notification routes go to realtime (which produces and
// delivers notifications); every other /v1/tenant and /v1/my path keeps its
// backend. The old tenant webhook routes are gone.
func TestNotificationRoutes(t *testing.T) {
	s := &Service{Backends: Backends{Room: "room", Realtime: "rt", Activity: "act", ShareAgent: "share"}}
	cases := map[string]string{
		"/v1/my/notification-prefs":                 "rt",
		"/v1/my/notifiers":                          "rt",
		"/v1/my/notifiers/corp/settings":            "",
		"/v1/my/notifiers/corp/test":                "rt",
		"/v1/my/notifiers/corp/other":               "",
		"/v1/my/agents":                             "act",
		"/v1/my/unread":                             "rt",
		"/v1/my/unread/extra":                       "",
		"/v1/my/other":                              "",
		"/v1/tenant/notification-webhook":           "share",
		"/v1/tenant/settings":                       "share",
		"/v1/tenant/members":                        "share",
		"/v1/my/notification-prefs/extra-segment-x": "",
	}
	for path, want := range cases {
		if got := s.route(mustParseAPIPath(t, http.MethodGet, path)); got != want {
			t.Errorf("route(%s) = %q, want %q", path, got, want)
		}
	}
}

// A member's own prefs and notifier test sends are allowed for a viewer
// (personal, not cost-bearing).
func TestNotificationPermissions(t *testing.T) {
	for _, c := range []struct{ method, path string }{
		{http.MethodPut, "/v1/my/notification-prefs"},
		{http.MethodPost, "/v1/my/notifiers/corp/test"},
	} {
		if p := requiredPermission(c.method, mustParseAPIPath(t, c.method, c.path)); p != rbac.PermRead {
			t.Errorf("%s %s needs %s, want read", c.method, c.path, p)
		}
	}
}
