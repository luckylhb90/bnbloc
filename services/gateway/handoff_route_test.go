package gateway

import (
	"net/http"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/rbac"
)

// F4: handoff export/import/provenance route to the room service; an import
// creates a room, so it needs room creation's permission and rate limit, while
// the export (a read) needs only read at the edge — the room service enforces
// owner/admin.
func TestHandoffRoutes(t *testing.T) {
	s := &Service{Backends: Backends{Room: "room", Realtime: "rt", Activity: "act", ShareAgent: "share"}}
	for _, path := range []string{"/v1/rooms/r1/export", "/v1/rooms/import", "/v1/rooms/r1/handoff"} {
		if got := s.route(mustParseAPIPath(t, http.MethodGet, path)); got != "room" {
			t.Errorf("route(%s) = %q, want room", path, got)
		}
	}
	imp := mustParseAPIPath(t, http.MethodPost, "/v1/rooms/import")
	if p := requiredPermission(http.MethodPost, imp); p != rbac.PermCreateRoom {
		t.Errorf("POST /v1/rooms/import needs %s, want create_room", p)
	}
	if !rateLimited(http.MethodPost, imp) {
		t.Error("POST /v1/rooms/import must be rate limited like room creation")
	}
	if p := requiredPermission(http.MethodGet, mustParseAPIPath(t, http.MethodGet, "/v1/rooms/r1/export")); p != rbac.PermRead {
		t.Errorf("GET export needs %s, want read", p)
	}
}
