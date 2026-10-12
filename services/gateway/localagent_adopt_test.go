package gateway

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
)

// adoptRoom: a fresh id is created; an existing id (409
// from the room service, BE-1) is adopted only when the caller is already a
// member — never re-created, never taken over. Every room call carries the
// request's effective tenant (signed), so the room lands in that tenant.
func TestAdoptRoomHandlesExistingRoomID(t *testing.T) {
	var mu sync.Mutex
	var calls []string
	room := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		calls = append(calls, r.Method+" "+r.URL.Path)
		if got := r.Header.Get("X-Bnbloc-Tenant"); got != "t" {
			t.Errorf("%s %s tenant = %q, want t", r.Method, r.URL.Path, got)
		}
		mu.Unlock()
		if r.Method == http.MethodPost && r.URL.Path == "/v1/rooms" {
			w.WriteHeader(http.StatusConflict)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer room.Close()
	members := map[string][]string{} // subject -> rooms
	s := &Service{Membership: memberOf(func(subject, room string) bool { return slices.Contains(members[subject], room) })}
	s.Backends.Room = room.URL
	ctx := context.Background()

	// Not a member of the existing room: refused, no member add.
	if err := s.adoptRoom(ctx, "ws-1", "mallory", "t"); err == nil {
		t.Fatal("adopting someone else's existing room must fail")
	}
	for _, c := range calls {
		if strings.HasSuffix(c, "/members") {
			t.Fatalf("member add attempted for a non-member: %v", calls)
		}
	}

	// Caller is a member: adopted as is, nothing else is written.
	members["alice"] = []string{"ws-1"}
	calls = nil
	if err := s.adoptRoom(ctx, "ws-1", "alice", "t"); err != nil {
		t.Fatalf("adopt existing member room: %v", err)
	}
	if len(calls) != 1 {
		t.Fatalf("calls = %v; want only the create attempt (409)", calls)
	}
}
