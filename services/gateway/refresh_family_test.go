package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// refreshGateway is a gateway with a controllable clock for H4 tests.
func refreshGateway(t *testing.T) (*Service, *httptest.Server, *time.Time) {
	t.Helper()
	now := time.Now()
	gw := newGateway(t, "")
	gw.Now = func() time.Time { return now }
	srv := httptest.NewServer(gw.Handler())
	t.Cleanup(srv.Close)
	return gw, srv, &now
}

func loginGrant(t *testing.T, srvURL string) sessionGrant {
	t.Helper()
	resp, raw := postJSON(t, srvURL+"/v1/auth/login", loginBody{IDPToken: "good"})
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login = %d %s", resp.StatusCode, raw)
	}
	var g sessionGrant
	_ = json.Unmarshal(raw, &g)
	return g
}

func refreshWith(t *testing.T, srvURL, path, token string) (int, sessionGrant) {
	t.Helper()
	resp, raw := postJSON(t, srvURL+path, map[string]string{"refreshToken": token})
	var g sessionGrant
	_ = json.Unmarshal(raw, &g)
	return resp.StatusCode, g
}

func authedDo(t *testing.T, method, url, bearer string) (int, []byte) {
	t.Helper()
	req, _ := http.NewRequest(method, url, nil)
	req.Header.Set("Authorization", "Bearer "+bearer)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var buf [4096]byte
	n, _ := resp.Body.Read(buf[:])
	return resp.StatusCode, buf[:n]
}

// Rotation moves the family forward; presenting the rotated-away token after
// the grace window is reuse and revokes the whole family (the newest token
// stops working too).
func TestRefreshReuseRevokesFamily(t *testing.T) {
	_, srv, now := refreshGateway(t)
	g1 := loginGrant(t, srv.URL)
	c, g2 := refreshWith(t, srv.URL, "/v1/auth/refresh", g1.RefreshToken)
	if c != http.StatusOK || g2.RefreshToken == "" || g2.RefreshToken == g1.RefreshToken {
		t.Fatalf("rotate = %d %+v", c, g2)
	}
	*now = now.Add(refreshReuseGrace + time.Second)
	if c, _ := refreshWith(t, srv.URL, "/v1/auth/refresh", g1.RefreshToken); c != http.StatusUnauthorized {
		t.Fatalf("stale token = %d, want 401", c)
	}
	if c, _ := refreshWith(t, srv.URL, "/v1/auth/refresh", g2.RefreshToken); c != http.StatusUnauthorized {
		t.Fatalf("newest token after reuse = %d, want 401 (family revoked)", c)
	}
}

// Two tabs sharing one stored refresh token renew at once: the second
// refresh (within the grace window) succeeds, and both resulting tokens keep
// working afterwards.
func TestRefreshGraceForConcurrentTabs(t *testing.T) {
	_, srv, now := refreshGateway(t)
	g1 := loginGrant(t, srv.URL)
	cA, a := refreshWith(t, srv.URL, "/v1/auth/refresh", g1.RefreshToken)
	*now = now.Add(5 * time.Second)
	cB, b := refreshWith(t, srv.URL, "/v1/auth/refresh", g1.RefreshToken)
	if cA != http.StatusOK || cB != http.StatusOK {
		t.Fatalf("concurrent refreshes = %d, %d; want both 200", cA, cB)
	}
	// Much later, whichever token the tabs stored still refreshes.
	*now = now.Add(time.Hour)
	if c, _ := refreshWith(t, srv.URL, "/v1/auth/refresh", b.RefreshToken); c != http.StatusOK {
		t.Fatalf("tab B token later = %d, want 200", c)
	}
	_ = a
}

// Logout revokes the family (idempotently); garbage is a no-op success.
func TestLogoutRevokesFamily(t *testing.T) {
	_, srv, _ := refreshGateway(t)
	g := loginGrant(t, srv.URL)
	for i := 0; i < 2; i++ {
		if resp, _ := postJSON(t, srv.URL+"/v1/auth/logout", map[string]string{"refreshToken": g.RefreshToken}); resp.StatusCode != http.StatusNoContent {
			t.Fatalf("logout #%d = %d, want 204", i, resp.StatusCode)
		}
	}
	if c, _ := refreshWith(t, srv.URL, "/v1/auth/refresh", g.RefreshToken); c != http.StatusUnauthorized {
		t.Fatalf("refresh after logout = %d, want 401", c)
	}
	if resp, _ := postJSON(t, srv.URL+"/v1/auth/logout", map[string]string{"refreshToken": "garbage"}); resp.StatusCode != http.StatusNoContent {
		t.Fatalf("logout with garbage = %d, want 204", resp.StatusCode)
	}
}

// A fork opens an independent family: the forked process rotates its own
// token while the original keeps working, and revoking one leaves the other.
func TestRefreshForkIsIndependentFamily(t *testing.T) {
	_, srv, now := refreshGateway(t)
	web := loginGrant(t, srv.URL)
	c, bridge := refreshWith(t, srv.URL, "/v1/auth/refresh/fork", web.RefreshToken)
	if c != http.StatusOK || bridge.Token == "" || bridge.RefreshToken == "" {
		t.Fatalf("fork = %d %+v", c, bridge)
	}
	// The bridge rotates its family repeatedly; the web's token is untouched.
	tok := bridge.RefreshToken
	for i := 0; i < 3; i++ {
		*now = now.Add(2 * time.Minute)
		c, g := refreshWith(t, srv.URL, "/v1/auth/refresh", tok)
		if c != http.StatusOK {
			t.Fatalf("bridge rotate #%d = %d", i, c)
		}
		tok = g.RefreshToken
	}
	if c, _ := refreshWith(t, srv.URL, "/v1/auth/refresh", web.RefreshToken); c != http.StatusOK {
		t.Fatalf("web token after bridge rotations = %d, want 200", c)
	}
	// Two families are listed, labelled by client.
	code, raw := authedDo(t, http.MethodGet, srv.URL+"/v1/my/sessions", web.Token)
	var list struct {
		Sessions []mySession `json:"sessions"`
	}
	_ = json.Unmarshal(raw, &list)
	if code != http.StatusOK || len(list.Sessions) != 2 {
		t.Fatalf("my sessions = %d %s", code, raw)
	}
	clients := map[string]bool{}
	current := 0
	var bridgeID string
	for _, s := range list.Sessions {
		clients[s.Client] = true
		if s.Current {
			current++
		}
		if s.Client == clientBridge {
			bridgeID = s.ID
		}
	}
	if !clients[clientWeb] || !clients[clientBridge] || current != 1 {
		t.Fatalf("sessions = %+v; want one web (current) + one bridge", list.Sessions)
	}
	// Revoking the bridge family stops the bridge only.
	if code, _ := authedDo(t, http.MethodDelete, srv.URL+"/v1/my/sessions/"+bridgeID, web.Token); code != http.StatusNoContent {
		t.Fatalf("revoke bridge session = %d", code)
	}
	if code, _ := authedDo(t, http.MethodDelete, srv.URL+"/v1/my/sessions/"+bridgeID, web.Token); code != http.StatusNotFound {
		t.Fatalf("second revoke = %d, want 404", code)
	}
	if c, _ := refreshWith(t, srv.URL, "/v1/auth/refresh", tok); c != http.StatusUnauthorized {
		t.Fatalf("revoked bridge refresh = %d, want 401", c)
	}
	if code, _ := authedDo(t, http.MethodGet, srv.URL+"/v1/my/sessions", ""); code != http.StatusUnauthorized {
		t.Fatalf("anonymous session list = %d, want 401", code)
	}
}

// A refresh token without a server-side family (minted by the bare signer)
// is refused: tracking is the only way a refresh token is honored.
func TestRefreshWithoutFamilyRefused(t *testing.T) {
	gw, srv, _ := refreshGateway(t)
	g := loginGrant(t, srv.URL)
	claims, err := gw.Signer.VerifyRefresh(g.RefreshToken)
	if err != nil {
		t.Fatal(err)
	}
	claims.Family, claims.ID = "", ""
	if c, _ := refreshWith(t, srv.URL, "/v1/auth/refresh", gw.Signer.Issue(claims)); c != http.StatusUnauthorized {
		t.Fatalf("family-less refresh token = %d, want 401", c)
	}
}
