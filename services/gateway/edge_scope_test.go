package gateway

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/config"
	"github.com/luckylhb90/bnbloc/internal/rbac"
)

// BNBLOC_DEV_DISCOVERY never runs in production: the gateway refuses to
// start with it, and the switch reads off there anyway.
func TestDevDiscoveryRefusedInProduction(t *testing.T) {
	t.Setenv(config.EnvVar, "production")
	t.Setenv("BNBLOC_DEV_DISCOVERY", "1")
	if err := CheckProductionEnv(); err == nil || !strings.Contains(err.Error(), "BNBLOC_DEV_DISCOVERY") {
		t.Fatalf("CheckProductionEnv = %v, want the switch refused", err)
	}
	if devDiscoveryEnabled() {
		t.Fatal("dev discovery reads on in production")
	}
	gw := newGateway(t, "")
	srv := httptest.NewServer(gw.Handler())
	defer srv.Close()
	token := gw.Signer.Issue(auth.Claims{Subject: "alice", ExpiresAtUnix: time.Now().Add(time.Hour).Unix()})
	if status := doJSON(t, srv, http.MethodGet, "/v1/local-agent/discover", token, ""); status != http.StatusNotFound {
		t.Fatalf("discover in production = %d, want 404", status)
	}
	t.Setenv("BNBLOC_DEV_DISCOVERY", "")
	if err := CheckProductionEnv(); err != nil {
		t.Fatalf("production without the switch = %v", err)
	}
	t.Setenv(config.EnvVar, "")
	t.Setenv("BNBLOC_DEV_DISCOVERY", "1")
	if err := CheckProductionEnv(); err != nil || !devDiscoveryEnabled() {
		t.Fatalf("development: CheckProductionEnv = %v, enabled = %v", err, devDiscoveryEnabled())
	}
}

// POST /v1/local-agent/run creates a room and drives an agent: a viewer is
// refused at the edge like on the proxied room creation, before any core is
// touched.
func TestLocalAgentRunGated(t *testing.T) {
	gw := newGateway(t, "")
	srv := httptest.NewServer(gw.Handler())
	defer srv.Close()
	viewer := gw.Signer.Issue(auth.Claims{Subject: "vic", Role: string(rbac.RoleViewer), ExpiresAtUnix: time.Now().Add(time.Hour).Unix()})
	if status := doJSON(t, srv, http.MethodPost, "/v1/local-agent/run", viewer, `{"agentTargetId":"acme/local:codex"}`); status != http.StatusForbidden {
		t.Fatalf("run as viewer = %d, want 403", status)
	}
	member := gw.Signer.Issue(auth.Claims{Subject: "maya", Role: string(rbac.RoleMember), ExpiresAtUnix: time.Now().Add(time.Hour).Unix()})
	if status := doJSON(t, srv, http.MethodPost, "/v1/local-agent/run", member, `{"agentTargetId":"acme/local:codex"}`); status == http.StatusForbidden {
		t.Fatalf("run as member = %d, want past the gate", status)
	}
}

// Every proxied path that names a room acts in the room's own tenant: the
// room itself, its sub-resources, a join, and a search narrowed to it.
func TestProxyRoomTenantEveryRoomPath(t *testing.T) {
	var gotTenant string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTenant = r.Header.Get("X-Bnbloc-Tenant")
	}))
	defer backend.Close()
	gw := newGateway(t, backend.URL)
	gw.Backends.Realtime = backend.URL
	gw.RoomTenant = func(room string) string {
		if room == "room-1" {
			return "org-b"
		}
		return ""
	}
	srv := httptest.NewServer(gw.Handler())
	defer srv.Close()
	token := gw.Signer.Issue(auth.Claims{Subject: "jack", Tenant: "home", Role: string(rbac.RoleMember), Rooms: []string{"room-1"}, ExpiresAtUnix: time.Now().Add(time.Hour).Unix()})
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/v1/rooms/room-1"},
		{http.MethodPatch, "/v1/rooms/room-1"},
		{http.MethodDelete, "/v1/rooms/room-1"},
		{http.MethodPost, "/v1/rooms/room-1/join"},
		{http.MethodGet, "/v1/rooms/room-1/members"},
		{http.MethodGet, "/v1/rooms/room-1/history"},
		{http.MethodGet, "/v1/search?q=hello&rooms=room-1"},
	} {
		gotTenant = ""
		if status := doJSON(t, srv, c.method, c.path, token, ""); status != http.StatusOK {
			t.Fatalf("%s %s = %d", c.method, c.path, status)
		}
		if gotTenant != "org-b" {
			t.Errorf("%s %s signed tenant %q, want the room's org-b", c.method, c.path, gotTenant)
		}
	}
	// The collection and a search across rooms act in the caller's tenant.
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/v1/rooms"},
		{http.MethodGet, "/v1/search?q=hello"},
	} {
		gotTenant = ""
		doJSON(t, srv, c.method, c.path, token, "")
		if gotTenant != "home" {
			t.Errorf("%s %s signed tenant %q, want the session's home", c.method, c.path, gotTenant)
		}
	}
	// A search narrowed to a room out of reach is refused.
	if status := doJSON(t, srv, http.MethodGet, "/v1/search?q=hello&rooms=room-9", token, ""); status != http.StatusForbidden {
		t.Fatalf("search in a room out of reach = %d, want 403", status)
	}
}

// A removal, a leave or a room deletion through this edge drops its cached
// live-membership answers, so the next request re-asks the room service.
func TestRoomRemovalForgetsEdgeMembership(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	defer backend.Close()
	gw := newGateway(t, backend.URL)
	gw.Rooms = nil
	probe, client := newProbeServer(t)
	probe.set("room-1", "maya", true)
	probe.set("room-1", "jack", true)
	gw.Membership = client
	srv := httptest.NewServer(gw.Handler())
	defer srv.Close()
	tok := func(sub string) string {
		return gw.Signer.Issue(auth.Claims{Subject: sub, Tenant: "home", Role: string(rbac.RoleMember), ExpiresAtUnix: time.Now().Add(time.Hour).Unix()})
	}
	for _, sub := range []string{"maya", "jack"} {
		if status := doJSON(t, srv, http.MethodGet, "/v1/rooms/room-1/members", tok(sub), ""); status != http.StatusOK {
			t.Fatalf("%s read = %d", sub, status)
		}
	}
	// jack removes maya: her cached answer goes, jack's stays.
	if status := doJSON(t, srv, http.MethodDelete, "/v1/rooms/room-1/members/maya", tok("jack"), ""); status != http.StatusOK {
		t.Fatalf("remove = %d", status)
	}
	probe.set("room-1", "maya", false)
	if status := doJSON(t, srv, http.MethodGet, "/v1/rooms/room-1/members", tok("maya"), ""); status != http.StatusForbidden {
		t.Fatalf("maya after removal = %d, want 403", status)
	}
	before := probe.count()
	doJSON(t, srv, http.MethodGet, "/v1/rooms/room-1/members", tok("jack"), "")
	if probe.count() != before {
		t.Fatal("jack's cached membership was dropped by maya's removal")
	}
	// The room is deleted: every cached answer for it goes.
	doJSON(t, srv, http.MethodDelete, "/v1/rooms/room-1", tok("jack"), "")
	probe.set("room-1", "jack", false)
	if status := doJSON(t, srv, http.MethodGet, "/v1/rooms/room-1/members", tok("jack"), ""); status != http.StatusForbidden {
		t.Fatalf("jack after the room's deletion = %d, want 403", status)
	}
}
