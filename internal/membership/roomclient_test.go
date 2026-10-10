package membership

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// RoomClient hits the tenant-agnostic /membership/ probe, returns true only when
// the body says {"member":true}, and caches the positive result.
func TestRoomClientPositiveCached(t *testing.T) {
	var calls int
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"member":true}`))
	}))
	defer srv.Close()
	c := NewRoomClient(srv.URL)
	if !c.IsMember("room-1", "alice", "default") {
		t.Fatal("expected member")
	}
	if !strings.Contains(gotPath, "/membership/") {
		t.Fatalf("probe path = %q, want tenant-agnostic /membership/", gotPath)
	}
	if !c.IsMember("room-1", "alice", "default") {
		t.Fatal("expected member (cached)")
	}
	if calls != 1 {
		t.Fatalf("probe calls = %d, want 1 (second served from cache)", calls)
	}
}

// A 200 probe whose body says {"member":false} fails closed (not a member).
func TestRoomClientMemberFalseFailsClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"member":false}`))
	}))
	defer srv.Close()
	c := NewRoomClient(srv.URL)
	if c.IsMember("room-1", "stranger", "default") {
		t.Fatal("member:false must fail closed")
	}
}

// A non-200 probe fails closed and is not cached.
func TestRoomClientFailsClosed(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	c := NewRoomClient(srv.URL)
	if c.IsMember("room-1", "stranger", "default") {
		t.Fatal("non-member must fail closed")
	}
}

// A transport error fails closed.
func TestRoomClientTransportErrorFailsClosed(t *testing.T) {
	c := NewRoomClient("http://127.0.0.1:0")
	c.HTTP.Timeout = 200 * time.Millisecond
	if c.IsMember("room-1", "alice", "default") {
		t.Fatal("transport error must fail closed")
	}
}
