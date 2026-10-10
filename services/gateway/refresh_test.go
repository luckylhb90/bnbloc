package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

func postJSON(t *testing.T, url string, body any) (*http.Response, []byte) {
	t.Helper()
	raw, _ := json.Marshal(body)
	resp, err := http.Post(url, "application/json", bytes.NewReader(raw))
	if err != nil {
		t.Fatalf("POST %s: %v", url, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp, out
}

// Login returns a refresh token; refreshing mints a working session and a
// rotated refresh token; tokens are not interchangeable.
func TestRefreshTokenRenewsSession(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Header.Get("X-Bnbloc-User"))
	}))
	defer backend.Close()
	gw := newGateway(t, backend.URL)
	srv := httptest.NewServer(gw.Handler())
	defer srv.Close()

	resp, raw := postJSON(t, srv.URL+"/v1/auth/login", loginBody{IDPToken: "good"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login = %d", resp.StatusCode)
	}
	var first sessionGrant
	_ = json.Unmarshal(raw, &first)
	if first.RefreshToken == "" || first.RefreshToken == first.Token {
		t.Fatalf("login grant lacks a distinct refresh token: %+v", first)
	}

	bearer := func(token string) int {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/rooms/room-1/members", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		r, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("proxy: %v", err)
		}
		r.Body.Close()
		return r.StatusCode
	}
	if got := bearer(first.RefreshToken); got != http.StatusUnauthorized {
		t.Fatalf("refresh token as bearer = %d, want 401", got)
	}

	resp, raw = postJSON(t, srv.URL+"/v1/auth/refresh", map[string]string{"refreshToken": first.RefreshToken})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("refresh = %d %s", resp.StatusCode, raw)
	}
	var next sessionGrant
	_ = json.Unmarshal(raw, &next)
	if next.Subject != "alice" || next.Token == "" || next.RefreshToken == "" {
		t.Fatalf("refresh grant = %+v", next)
	}
	if got := bearer(next.Token); got != http.StatusOK {
		t.Fatalf("refreshed session as bearer = %d, want 200", got)
	}

	if resp, _ := postJSON(t, srv.URL+"/v1/auth/refresh", map[string]string{"refreshToken": first.Token}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("session token on /refresh = %d, want 401", resp.StatusCode)
	}
	if resp, _ := postJSON(t, srv.URL+"/v1/auth/refresh", map[string]string{}); resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("empty refresh = %d, want 400", resp.StatusCode)
	}
}

// A refresh re-runs the suspension gate: a suspended tenant cannot renew.
func TestRefreshRefusesSuspendedTenant(t *testing.T) {
	gw := newGateway(t, "")
	suspended := false
	gw.TenantSuspended = func(string) bool { return suspended }
	srv := httptest.NewServer(gw.Handler())
	defer srv.Close()
	_, raw := postJSON(t, srv.URL+"/v1/auth/login", loginBody{IDPToken: "good"})
	var g sessionGrant
	_ = json.Unmarshal(raw, &g)
	suspended = true
	if resp, _ := postJSON(t, srv.URL+"/v1/auth/refresh", map[string]string{"refreshToken": g.RefreshToken}); resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("refresh into suspended tenant = %d, want 401", resp.StatusCode)
	}
}
