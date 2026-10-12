package svcauth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func signedReq(key, user string) *http.Request {
	r := httptest.NewRequest(http.MethodGet, "/v1/rooms/r1/history", nil)
	Key(key).Assert(r, user, "", "")
	return r
}

// A request signed with the configured key and matching user passes.
func TestMiddlewareAcceptsValidSignature(t *testing.T) {
	key := "test-internal-key"
	called := false
	h := Middleware(key, http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq(key, "alice"))
	if rec.Code != http.StatusOK || !called {
		t.Fatalf("valid signature rejected: code=%d called=%v", rec.Code, called)
	}
}

// An asserted user with no signature is rejected when a key is configured — the
// core SEC8 spoofing defense.
func TestMiddlewareRejectsUnsignedIdentity(t *testing.T) {
	h := Middleware("k", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler must not run for an unsigned identity")
	}))
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.Header.Set(HeaderUser, "attacker")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unsigned identity: code=%d, want 401", rec.Code)
	}
}

// A signature minted for one user cannot be replayed to assert another.
func TestMiddlewareRejectsUserSwap(t *testing.T) {
	key := "k"
	r := signedReq(key, "alice")
	r.Header.Set(HeaderUser, "bob") // same sig/ts, different asserted user
	h := Middleware(key, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler must not run for a swapped user")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("user swap: code=%d, want 401", rec.Code)
	}
}

// A signature from a different key is rejected.
func TestMiddlewareRejectsWrongKey(t *testing.T) {
	r := signedReq("attacker-key", "alice")
	h := Middleware("real-key", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler must not run for a wrong-key signature")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("wrong key: code=%d, want 401", rec.Code)
	}
}

// A stale timestamp (outside the freshness window) is rejected even with a
// valid signature.
func TestMiddlewareRejectsStaleTimestamp(t *testing.T) {
	key := "k"
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.Header.Set(HeaderUser, "alice")
	staleMs := int64(1) // ~epoch, far outside the window
	r.Header.Set(HeaderTs, "1")
	r.Header.Set(HeaderSig, compute(key, staleMs, "alice", "", ""))
	h := Middleware(key, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("handler must not run for a stale timestamp")
	}))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("stale ts: code=%d, want 401", rec.Code)
	}
}

// With no key configured (dev mode) an unsigned identity passes through, so
// local single-host development is unaffected.
func TestMiddlewareDevModePassesThrough(t *testing.T) {
	called := false
	h := Middleware("", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.Header.Set(HeaderUser, "alice")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK || !called {
		t.Fatalf("dev mode should pass through: code=%d called=%v", rec.Code, called)
	}
}

// An empty asserted identity is always allowed (health checks, unauthenticated
// endpoints assert no user).
func TestMiddlewareAllowsEmptyIdentity(t *testing.T) {
	called := false
	h := Middleware("k", http.HandlerFunc(func(http.ResponseWriter, *http.Request) { called = true }))
	r := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, r)
	if rec.Code != http.StatusOK || !called {
		t.Fatalf("empty identity should pass: code=%d called=%v", rec.Code, called)
	}
}

// BE-5: internal routes are never reachable anonymously, and in a keyed
// deployment only with a valid signature.
func TestRequireSignedGuardsInternalRoutes(t *testing.T) {
	key := "k"
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	cases := []struct {
		name string
		key  string
		req  func() *http.Request
		want int
	}{
		{"anonymous keyed", key, func() *http.Request { return httptest.NewRequest(http.MethodPost, "/internal/x", nil) }, http.StatusUnauthorized},
		{"anonymous dev", "", func() *http.Request { return httptest.NewRequest(http.MethodPost, "/internal/x", nil) }, http.StatusUnauthorized},
		{"unsigned keyed", key, func() *http.Request {
			r := httptest.NewRequest(http.MethodPost, "/internal/x", nil)
			r.Header.Set(HeaderUser, ServiceSubject("activity"))
			return r
		}, http.StatusUnauthorized},
		{"wrong key", key, func() *http.Request { return signedReq("other", ServiceSubject("activity")) }, http.StatusUnauthorized},
		{"signed", key, func() *http.Request { return signedReq(key, ServiceSubject("activity")) }, http.StatusNoContent},
		{"asserted dev", "", func() *http.Request { return signedReq("", ServiceSubject("room")) }, http.StatusNoContent},
	}
	for _, c := range cases {
		rec := httptest.NewRecorder()
		RequireSigned(c.key, ok).ServeHTTP(rec, c.req())
		if rec.Code != c.want {
			t.Errorf("%s: code=%d want %d", c.name, rec.Code, c.want)
		}
	}
}

// BE-5: a signature binds the tenant, so a signed publish cannot be replayed
// into another tenant.
func TestRequireSignedRejectsTamperedTenant(t *testing.T) {
	r := httptest.NewRequest(http.MethodPost, "/internal/x", nil)
	Key("k").Assert(r, ServiceSubject("activity"), "acme", "")
	r.Header.Set(HeaderTenant, "globex")
	rec := httptest.NewRecorder()
	RequireSigned("k", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("tampered tenant must not reach the handler")
	})).ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d, want 401", rec.Code)
	}
}

// OPS-5: production refuses the unverified dev mode.
func TestCheckKeyRequiresKeyInProduction(t *testing.T) {
	t.Setenv("BNBLOC_ENV", "production")
	if err := CheckKey(""); err != ErrKeyRequired {
		t.Fatalf("CheckKey(\"\") in production = %v, want ErrKeyRequired", err)
	}
	if err := CheckKey("k"); err != nil {
		t.Fatalf("CheckKey(k) in production = %v", err)
	}
	t.Setenv("BNBLOC_ENV", "")
	if err := CheckKey(""); err != nil {
		t.Fatalf("CheckKey(\"\") in dev = %v, want nil", err)
	}
}

// The org role is signed like the tenant: a member cannot raise the role
// the gateway asserted for them.
func TestTamperedRoleRejected(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/v1/tenant/x", nil)
	Key("k").Assert(r, "maya", "acme", "member")
	r.Header.Set(HeaderRole, "org_admin")
	rec := httptest.NewRecorder()
	Middleware("k", http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("tampered role must not reach the handler")
	})).ServeHTTP(rec, r)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("code=%d, want 401", rec.Code)
	}
}

// Assert replaces any assertion already on the request and defaults the
// tenant, so a forwarded or forged header never survives.
func TestAssertReplacesForgedHeaders(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/x", nil)
	r.Header.Set(HeaderUser, "mallory")
	r.Header.Set(HeaderTenant, "globex")
	r.Header.Set(HeaderRole, "super_admin")
	Key("k").Assert(r, "maya", "", "")
	if Subject(r) != "maya" || r.Header.Get(HeaderTenant) != "default" || r.Header.Get(HeaderRole) != "" {
		t.Fatalf("assertion = %q %q %q", Subject(r), r.Header.Get(HeaderTenant), r.Header.Get(HeaderRole))
	}
	rec := httptest.NewRecorder()
	Middleware("k", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if Role(r) != "" {
			t.Errorf("role = %q", Role(r))
		}
	})).ServeHTTP(rec, r)
	if rec.Code != http.StatusOK {
		t.Fatalf("code=%d", rec.Code)
	}
}

// RequireService admits only the named services, never a member identity
// the gateway signed.
func TestRequireServiceAdmitsOnlyNamedServices(t *testing.T) {
	ok := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusNoContent) })
	h := RequireService("k", []string{"room", "activity"}, ok)
	for subject, want := range map[string]int{
		ServiceSubject("room"):     http.StatusNoContent,
		ServiceSubject("activity"): http.StatusNoContent,
		ServiceSubject("gateway"):  http.StatusForbidden,
		"maya":                     http.StatusForbidden,
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, signedReq("k", subject))
		if rec.Code != want {
			t.Errorf("%s: code=%d want %d", subject, rec.Code, want)
		}
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, signedReq("other", ServiceSubject("room")))
	if rec.Code != http.StatusUnauthorized {
		t.Errorf("wrong key: code=%d want 401", rec.Code)
	}
}
