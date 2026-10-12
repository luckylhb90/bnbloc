package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/store"
)

// roleMembership is a membership.Directory over a test predicate: role
// answers a subject's role in a room ("" = not a member) and whether the
// membership is a human's.
type roleMembership func(subject, room string) (role string, human bool)

func (f roleMembership) Lookup(room, subject string) (membership.Member, bool) {
	role, human := f(subject, room)
	if role == "" {
		return membership.Member{}, false
	}
	return membership.Member{Role: store.Role(role), AgentHost: !human}, true
}

func (f roleMembership) Fresh(room, subject string) (membership.Member, bool) {
	return f.Lookup(room, subject)
}

func (roleMembership) Forget(string, string) {}

// memberOf is a roleMembership where every member is a human "member".
func memberOf(in func(subject, room string) bool) roleMembership {
	return func(subject, room string) (string, bool) {
		if in(subject, room) {
			return string(store.RoleMember), true
		}
		return "", false
	}
}

// probeServer fakes bnbloc-room's membership probe for a real
// membership.RoomClient (so the gateway's cache and Forget wiring are the
// production ones), counting probes.
type probeServer struct {
	mu     sync.Mutex
	member map[string]bool // "room\x00subject"
	probes int
}

func newProbeServer(t *testing.T) (*probeServer, *membership.RoomClient) {
	t.Helper()
	ps := &probeServer{member: map[string]bool{}}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/") // v1 rooms {room} membership {subject}
		if len(parts) != 5 || parts[3] != "membership" {
			http.NotFound(w, r)
			return
		}
		ps.mu.Lock()
		ps.probes++
		ok := ps.member[parts[2]+"\x00"+parts[4]]
		ps.mu.Unlock()
		_ = json.NewEncoder(w).Encode(map[string]any{"member": ok, "role": "member", "tenantId": "home"})
	}))
	t.Cleanup(srv.Close)
	return ps, membership.NewRoomClient(srv.URL)
}

func (ps *probeServer) set(room, subject string, member bool) {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	ps.member[room+"\x00"+subject] = member
}

func (ps *probeServer) count() int {
	ps.mu.Lock()
	defer ps.mu.Unlock()
	return ps.probes
}
