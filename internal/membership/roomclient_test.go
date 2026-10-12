package membership

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// RoomClient hits the tenant-agnostic /membership/ probe, returns a member
// only when the body says {"member":true} with the room's tenant, and caches
// the positive result.
func TestRoomClientPositiveCached(t *testing.T) {
	var calls int
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"member":true,"role":"member","tenantId":"org-a"}`))
	}))
	defer srv.Close()
	c := NewRoomClient(srv.URL)
	m, ok := c.Lookup("room-1", "alice")
	if !ok || m.Tenant != "org-a" || m.Role != store.RoleMember {
		t.Fatalf("lookup = %+v %v, want a member of org-a", m, ok)
	}
	if !strings.Contains(gotPath, "/membership/") {
		t.Fatalf("probe path = %q, want tenant-agnostic /membership/", gotPath)
	}
	if _, ok := c.Lookup("room-1", "alice"); !ok {
		t.Fatal("expected member (cached)")
	}
	if calls != 1 {
		t.Fatalf("probe calls = %d, want 1 (second served from cache)", calls)
	}
	// Fresh always asks; Forget drops the cached answer.
	if _, ok := c.Fresh("room-1", "alice"); !ok || calls != 2 {
		t.Fatalf("fresh: calls = %d, want 2", calls)
	}
	c.Forget("room-1", "")
	if _, ok := c.Lookup("room-1", "alice"); !ok || calls != 3 {
		t.Fatalf("after Forget(room): calls = %d, want 3 (cache dropped)", calls)
	}
	c.Forget("room-1", "alice")
	if _, ok := c.Lookup("room-1", "alice"); !ok || calls != 4 {
		t.Fatalf("after Forget(room, user): calls = %d, want 4 (cache dropped)", calls)
	}
}

// A 200 probe whose body says {"member":false}, or a member answer without
// the room's tenant, fails closed (not a member).
func TestRoomClientMemberFalseFailsClosed(t *testing.T) {
	for _, body := range []string{`{"member":false}`, `{"member":true,"role":"member"}`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(body))
		}))
		c := NewRoomClient(srv.URL)
		if _, ok := c.Lookup("room-1", "stranger"); ok {
			t.Fatalf("%s must fail closed", body)
		}
		srv.Close()
	}
}

// A non-200 probe fails closed and is not cached.
func TestRoomClientFailsClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := NewRoomClient(srv.URL)
	if _, ok := c.Lookup("room-1", "stranger"); ok {
		t.Fatal("non-member must fail closed")
	}
}

// A transport error fails closed.
func TestRoomClientTransportErrorFailsClosed(t *testing.T) {
	c := NewRoomClient("http://127.0.0.1:0")
	c.HTTP.Timeout = 200 * time.Millisecond
	if _, ok := c.Lookup("room-1", "alice"); ok {
		t.Fatal("transport error must fail closed")
	}
}

// MayChat is human and not a visitor; IsHuman excludes an agent host.
func TestMemberAbilities(t *testing.T) {
	for _, tc := range []struct {
		m              Member
		human, mayChat bool
	}{
		{Member{Role: store.RoleOwner}, true, true},
		{Member{Role: store.RoleAdmin}, true, true},
		{Member{Role: store.RoleMember}, true, true},
		{Member{Role: store.RoleVisitor}, true, false},
		{Member{Role: store.RoleVisitor, AgentHost: true}, false, false},
	} {
		if tc.m.IsHuman() != tc.human || tc.m.MayChat() != tc.mayChat {
			t.Fatalf("%+v: IsHuman=%v MayChat=%v, want %v %v", tc.m, tc.m.IsHuman(), tc.m.MayChat(), tc.human, tc.mayChat)
		}
	}
}

// RoomScope admits a member in the room's own tenant only, and fails closed
// without a directory.
func TestRoomScope(t *testing.T) {
	s := NewStatic()
	s.Add("room-1", "maya")
	s.SetRoomTenant("room-1", "org-a")
	in := func(tid string) context.Context { return tenant.NewContext(context.Background(), tid) }
	if m, err := RoomScope(in("org-a"), s, "room-1", "maya", AnyMember); err != nil || m.Tenant != "org-a" {
		t.Fatalf("member in the room's tenant = %+v %v", m, err)
	}
	if _, err := RoomScope(in("org-b"), s, "room-1", "maya", AnyMember); !errors.Is(err, ErrOtherTenant) {
		t.Fatalf("another tenant = %v, want ErrOtherTenant", err)
	}
	if _, err := RoomScope(in("org-a"), s, "room-1", "jack", AnyMember); !errors.Is(err, ErrNotMember) {
		t.Fatalf("non-member = %v, want ErrNotMember", err)
	}
	if _, err := RoomScope(in("org-a"), nil, "room-1", "maya", AnyMember); !errors.Is(err, ErrNotMember) {
		t.Fatalf("nil directory = %v, want ErrNotMember (fail closed)", err)
	}
	s.AddRole("room-1", "vic", store.RoleVisitor)
	_ = s.PutAgentHost(context.Background(), "", "room-1", "host")
	for _, tc := range []struct {
		user string
		need Need
		want error
	}{
		{"maya", Chat, nil}, {"maya", Human, nil},
		{"vic", AnyMember, nil}, {"vic", Human, nil}, {"vic", Chat, ErrMayNotChat},
		{"host", AnyMember, nil}, {"host", Human, ErrNotHuman}, {"host", Chat, ErrMayNotChat},
	} {
		if _, err := RoomScope(in("org-a"), s, "room-1", tc.user, tc.need); !errors.Is(err, tc.want) || (err != nil && !IsRefusal(err)) {
			t.Fatalf("%s need %d = %v, want %v", tc.user, tc.need, err, tc.want)
		}
	}
}
