package gateway

import (
	"net/http"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/rbac"
)

// N2/N3: scheduled prompts route to realtime (beside chat publish); room
// templates route to the room service; template instantiation carries room
// creation's permission and rate limit; "run now" is rate-limited like chat.
func TestScheduleAndTemplateRoutes(t *testing.T) {
	s := &Service{Backends: Backends{Room: "room", Realtime: "rt", Activity: "act", ShareAgent: "share"}}
	cases := map[string]string{
		"/v1/rooms/r1/schedules":               "rt",
		"/v1/rooms/r1/schedules/sch_1":         "rt",
		"/v1/rooms/r1/schedules/sch_1/run":     "rt",
		"/v1/rooms/schedules/members":          "room",
		"/v1/room-templates":                   "room",
		"/v1/room-templates/tpl_1":             "room",
		"/v1/room-templates/tpl_1/instantiate": "room",
		"/v1/room-templates/from-room/r1":      "room",
		"/v1/tenant/room-templates/tpl_1":      "room",
	}
	for path, want := range cases {
		if got := s.route(mustParseAPIPath(t, http.MethodGet, path)); got != want {
			t.Errorf("route(%s) = %q, want %q", path, got, want)
		}
	}
	inst := mustParseAPIPath(t, http.MethodPost, "/v1/room-templates/tpl_1/instantiate")
	if p := requiredPermission(http.MethodPost, inst); p != rbac.PermCreateRoom {
		t.Errorf("POST instantiate needs %s, want create_room", p)
	}
	if !rateLimited(http.MethodPost, inst) {
		t.Error("instantiate must be rate-limited like room creation")
	}
	if p := requiredPermission(http.MethodDelete, mustParseAPIPath(t, http.MethodDelete, "/v1/tenant/room-templates/tpl_1")); p != rbac.PermManageOrg {
		t.Errorf("DELETE tenant/room-templates needs %s, want manage_org", p)
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
