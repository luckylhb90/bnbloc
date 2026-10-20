package gateway

import (
	"net/http"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/rbac"
)

// N2: scheduled prompts route to realtime (beside chat publish); "run now"
// is rate-limited like chat. Room templates are gone: no /v1/room-templates
// path reaches a backend (room packages replaced them).
func TestScheduleRoutes(t *testing.T) {
	s := &Service{Backends: Backends{Room: "room", Realtime: "rt", Activity: "act", ShareAgent: "share"}}
	cases := map[string]string{
		"/v1/rooms/r1/schedules":           "rt",
		"/v1/rooms/r1/schedules/sch_1":     "rt",
		"/v1/rooms/r1/schedules/sch_1/run": "rt",
		"/v1/rooms/schedules/members":      "room",
		"/v1/room-templates":               "",
	}
	for path, want := range cases {
		if got := s.route(mustParseAPIPath(t, http.MethodGet, path)); got != want {
			t.Errorf("route(%s) = %q, want %q", path, got, want)
		}
	}
	if !rateLimited(http.MethodPost, mustParseAPIPath(t, http.MethodPost, "/v1/rooms/r1/schedules/sch_1/run")) {
		t.Error("schedule run-now must be rate-limited like chat")
	}
	if rateLimited(http.MethodPost, mustParseAPIPath(t, http.MethodPost, "/v1/rooms/r1/schedules")) {
		t.Error("creating a schedule posts nothing and is not rate-limited")
	}
	if p := requiredPermission(http.MethodPost, mustParseAPIPath(t, http.MethodPost, "/v1/rooms/r1/schedules")); p != rbac.PermDriveAgent {
		t.Errorf("POST schedules needs %s, want drive_agent (a viewer cannot post)", p)
	}
}
