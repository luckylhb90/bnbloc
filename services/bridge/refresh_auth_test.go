package bridge

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

type authCall struct {
	path, token, client string
}

// fakeAuthGateway answers fork/refresh/logout and records each call.
func fakeAuthGateway(t *testing.T, refreshStatus *int) (*httptest.Server, func() []authCall) {
	t.Helper()
	var mu sync.Mutex
	var calls []authCall
	n := 0
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			RefreshToken string `json:"refreshToken"`
			Client       string `json:"client"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		mu.Lock()
		defer mu.Unlock()
		calls = append(calls, authCall{r.URL.Path, body.RefreshToken, body.Client})
		switch r.URL.Path {
		case "/v1/auth/refresh/fork", "/v1/auth/refresh":
			if r.URL.Path == "/v1/auth/refresh" && refreshStatus != nil && *refreshStatus != http.StatusOK {
				http.Error(w, "revoked", *refreshStatus)
				return
			}
			n++
			_ = json.NewEncoder(w).Encode(map[string]string{
				"sessionToken": "session-" + string(rune('0'+n)), "refreshToken": "own-" + string(rune('0'+n)), "tenant": "default",
			})
		case "/v1/auth/logout":
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected call %s (dev login must not be used)", r.URL.Path)
			http.Error(w, "nope", http.StatusUnauthorized)
		}
	}))
	t.Cleanup(gw.Close)
	return gw, func() []authCall {
		mu.Lock()
		defer mu.Unlock()
		return append([]authCall(nil), calls...)
	}
}

// A handed-over refresh token is FORKED once into the bridge's own family;
// the bridge then rotates only its own token and never presents the
// handed-over one again (the member's client keeps rotating it, and a second
// rotator would trip reuse detection). A graceful stop revokes the bridge's
// own family, never the handed-over one.
func TestGatewayAuthForksHandedTokenThenRotatesOwnFamily(t *testing.T) {
	gw, calls := fakeAuthGateway(t, nil)
	b := New(Config{GatewayBaseURL: gw.URL, OwnerSubject: "alice", RefreshToken: "handed"})
	if got := b.gatewayAuth(context.Background()); got != "session-1" {
		t.Fatalf("first token = %q, want session-1", got)
	}
	b.invalidateToken()
	if got := b.gatewayAuth(context.Background()); got != "session-2" {
		t.Fatalf("second token = %q, want session-2", got)
	}
	b.releaseSession()
	want := []authCall{
		{"/v1/auth/refresh/fork", "handed", "bridge"},
		{"/v1/auth/refresh", "own-1", ""},
		{"/v1/auth/logout", "own-2", ""},
	}
	got := calls()
	if len(got) != len(want) {
		t.Fatalf("calls = %+v, want %+v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("call %d = %+v, want %+v (all: %+v)", i, got[i], want[i], got)
		}
	}
}

// When the bridge's own family is revoked (the member signed the bridge
// out), it stays signed out instead of replaying the stale handed-over token.
func TestGatewayAuthDoesNotReforkAfterOwnFamilyRevoked(t *testing.T) {
	status := http.StatusOK
	gw, calls := fakeAuthGateway(t, &status)
	b := New(Config{GatewayBaseURL: gw.URL, OwnerSubject: "alice", RefreshToken: "handed"})
	_ = b.gatewayAuth(context.Background())
	status = http.StatusUnauthorized
	b.invalidateToken()
	_ = b.gatewayAuth(context.Background())
	b.invalidateToken()
	_ = b.gatewayAuth(context.Background())
	forks := 0
	for _, c := range calls() {
		if c.path == "/v1/auth/refresh/fork" {
			forks++
		}
	}
	if forks != 1 {
		t.Fatalf("forks = %d, want exactly 1 (calls %+v)", forks, calls())
	}
}
