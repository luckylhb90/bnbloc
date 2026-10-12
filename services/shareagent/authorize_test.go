package shareagenthttp

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// The authorizing owner is always the authenticated caller (X-Bnbloc-User),
// never a request-body field, so a grantee can never authorize itself. This is
// the security-critical invariant of the pre-authorization model.
func TestAuthorizeUsesCallerNotBodyAsOwner(t *testing.T) {
	svc, grants := newService(t)
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()

	// bob authorizes; even if a body tried to name someone else, the owner is bob.
	req, _ := http.NewRequest(http.MethodPut,
		srv.URL+"/v1/rooms/room-1/shared-agents/shared-agent:codex/authorization",
		strings.NewReader(`{"ownerUserId":"alice","allowedModels":["gpt-5"]}`))
	req.Header.Set("X-Bnbloc-User", "bob")
	resp := mustDo(t, req)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("authorize status = %d, want 200", resp.StatusCode)
	}
	g, err := grants.GetGrant(dctx(), roomAuthGrantID(dctx(), "room-1", "bob", "codex"))
	if err != nil {
		t.Fatalf("expected authorization stored under caller bob: %v", err)
	}
	if g.OwnerUserID != "bob" {
		t.Fatalf("owner = %q, want caller %q (body must not set owner)", g.OwnerUserID, "bob")
	}
	// No authorization exists for the body-named "alice".
	if _, err := grants.GetGrant(dctx(), roomAuthGrantID(dctx(), "room-1", "alice", "codex")); err == nil {
		t.Fatal("body-named owner must not receive an authorization")
	}
}

// A missing authenticated subject fails closed (no anonymous authorization).
func TestAuthorizeRequiresAuthenticatedCaller(t *testing.T) {
	svc, _ := newService(t)
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodPut,
		srv.URL+"/v1/rooms/room-1/shared-agents/shared-agent:codex/authorization",
		strings.NewReader(`{"allowedModels":["gpt-5"]}`))
	resp := mustDo(t, req)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 without X-Bnbloc-User", resp.StatusCode)
	}
}

// The authorization read reports authorized only while a live authorization
// exists, and reflects a revoke.
func TestAuthorizationReadReflectsLifecycle(t *testing.T) {
	svc, _ := newService(t)
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()
	base := srv.URL + "/v1/rooms/room-1/shared-agents/shared-agent:codex/authorization"

	// Before authorization: not authorized.
	if got := readAuthorized(t, base+"?owner=jack"); got {
		t.Fatal("unauthorized owner must read authorized=false")
	}
	// jack authorizes.
	put, _ := http.NewRequest(http.MethodPut, base, strings.NewReader(`{"allowedModels":["gpt-5"]}`))
	put.Header.Set("X-Bnbloc-User", "jack")
	mustDo(t, put).Body.Close()
	if got := readAuthorized(t, base+"?owner=jack"); !got {
		t.Fatal("after authorize, owner must read authorized=true")
	}
	// jack revokes.
	del, _ := http.NewRequest(http.MethodDelete, base, nil)
	del.Header.Set("X-Bnbloc-User", "jack")
	mustDo(t, del).Body.Close()
	if got := readAuthorized(t, base+"?owner=jack"); got {
		t.Fatal("after revoke, owner must read authorized=false")
	}
}

// An expired authorization reads as not authorized (TTL lapse is enforced).
func TestAuthorizationExpires(t *testing.T) {
	svc, _ := newService(t)
	// Freeze "now" past the created authorization's expiry via a moving clock.
	now := int64(1000)
	svc.Now = func() int64 { return now }
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()
	base := srv.URL + "/v1/rooms/room-1/shared-agents/shared-agent:codex/authorization"

	put, _ := http.NewRequest(http.MethodPut, base, strings.NewReader(`{"allowedModels":["gpt-5"],"ttlMs":10}`))
	put.Header.Set("X-Bnbloc-User", "jack")
	mustDo(t, put).Body.Close()
	if got := readAuthorized(t, base+"?owner=jack"); !got {
		t.Fatal("fresh authorization must be live")
	}
	now = 2000 // advance well past created(1000)+ttl(10)
	if got := readAuthorized(t, base+"?owner=jack"); got {
		t.Fatal("expired authorization must read authorized=false")
	}
}

func readAuthorized(t *testing.T, url string) bool {
	t.Helper()
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("X-Bnbloc-User", "reader")
	resp := mustDo(t, req)
	defer resp.Body.Close()
	var out struct {
		Authorized bool `json:"authorized"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out.Authorized
}
