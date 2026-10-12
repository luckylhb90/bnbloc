package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// isolateHome points the CLI's ~/.bnbloc at a temp dir and clears the
// credential env.
func isolateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("BNBLOC_TOKEN", "")
	t.Setenv("BNBLOC_GATEWAY", "")
	return home
}

func writeConfigFile(t *testing.T, home string, doc map[string]any) string {
	t.Helper()
	path := filepath.Join(home, ".bnbloc", "config.json")
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(doc)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

func readConfigFile(t *testing.T, path string) map[string]any {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	doc := map[string]any{}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatal(err)
	}
	return doc
}

// refreshGateway answers /v1/auth/refresh for refresh token "rt-<n>" with
// session "st-<n+1>" and rotated refresh "rt-<n+1>", and records the bearer
// of every other request.
type refreshGateway struct {
	*httptest.Server
	refreshes atomic.Int32
	bearers   []string
	refuse    bool
}

func newRefreshGateway(t *testing.T) *refreshGateway {
	g := &refreshGateway{}
	g.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/auth/refresh" {
			g.refreshes.Add(1)
			var body struct {
				RefreshToken string `json:"refreshToken"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			if g.refuse || body.RefreshToken != "rt-1" {
				http.Error(w, "invalid or expired refresh token", http.StatusUnauthorized)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"sessionToken": "st-2", "refreshToken": "rt-2", "subject": "admin@example.com", "tenant": "default", "role": "super_admin"})
			return
		}
		g.bearers = append(g.bearers, r.Header.Get("Authorization"))
		_, _ = io.WriteString(w, `{}`)
	}))
	t.Cleanup(g.Close)
	return g
}

// The stored sign-in is refreshed once per process, the rotated refresh token
// is persisted atomically (0600) without dropping other keys, and the session
// token is the bearer.
func TestStoredLoginRefreshesOnceAndPersistsRotation(t *testing.T) {
	home := isolateHome(t)
	gw := newRefreshGateway(t)
	path := writeConfigFile(t, home, map[string]any{"gateway": gw.URL, "refreshToken": "rt-1", "subject": "admin@example.com", "extra": "keep-me"})

	c := newClient(resolveConfig("", ""))
	for i := 0; i < 3; i++ {
		if err := c.call(context.Background(), apiTimeout, http.MethodGet, "/v1/platform/update-config", nil, nil); err != nil {
			t.Fatal(err)
		}
	}
	if n := gw.refreshes.Load(); n != 1 {
		t.Fatalf("refreshes = %d, want exactly 1 per process", n)
	}
	for _, b := range gw.bearers {
		if b != "Bearer st-2" {
			t.Fatalf("bearer = %q, want the refreshed session token", b)
		}
	}
	doc := readConfigFile(t, path)
	if doc["refreshToken"] != "rt-2" || doc["gateway"] != gw.URL || doc["extra"] != "keep-me" {
		t.Fatalf("config after refresh = %v, want rotated token + preserved keys", doc)
	}
	st, _ := os.Stat(path)
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %v, want 0600", st.Mode().Perm())
	}
}

// --token / BNBLOC_TOKEN beat the stored sign-in (no refresh happens).
func TestStaticTokenTakesPrecedence(t *testing.T) {
	home := isolateHome(t)
	gw := newRefreshGateway(t)
	writeConfigFile(t, home, map[string]any{"gateway": gw.URL, "refreshToken": "rt-1"})
	t.Setenv("BNBLOC_TOKEN", "env-token")
	if err := newClient(resolveConfig("", "")).call(context.Background(), apiTimeout, http.MethodGet, "/x", nil, nil); err != nil {
		t.Fatal(err)
	}
	if err := newClient(resolveConfig("", "flag-token")).call(context.Background(), apiTimeout, http.MethodGet, "/x", nil, nil); err != nil {
		t.Fatal(err)
	}
	if gw.refreshes.Load() != 0 || gw.bearers[0] != "Bearer env-token" || gw.bearers[1] != "Bearer flag-token" {
		t.Fatalf("refreshes %d bearers %v", gw.refreshes.Load(), gw.bearers)
	}
}

// A refused refresh tells the operator to sign in again.
func TestRefusedRefreshSaysRunLogin(t *testing.T) {
	home := isolateHome(t)
	gw := newRefreshGateway(t)
	gw.refuse = true
	writeConfigFile(t, home, map[string]any{"gateway": gw.URL, "refreshToken": "rt-1"})
	err := newClient(resolveConfig("", "")).call(context.Background(), apiTimeout, http.MethodGet, "/x", nil, nil)
	if err == nil || !strings.Contains(err.Error(), "bnbloc login") {
		t.Fatalf("err = %v, want a run-bnbloc-login hint", err)
	}
	if len(gw.bearers) != 0 {
		t.Fatal("request sent after a refused refresh")
	}
}

// The stored refresh token is never sent to a gateway other than its own.
func TestStoredLoginIsBoundToItsGateway(t *testing.T) {
	home := isolateHome(t)
	gw := newRefreshGateway(t)
	other := newRefreshGateway(t)
	writeConfigFile(t, home, map[string]any{"gateway": gw.URL, "refreshToken": "rt-1"})
	err := newClient(resolveConfig(other.URL, "")).call(context.Background(), apiTimeout, http.MethodGet, "/x", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	if other.refreshes.Load() != 0 || other.bearers[0] != "" {
		t.Fatalf("other gateway saw credentials: refreshes %d bearer %q", other.refreshes.Load(), other.bearers[0])
	}
	// The stored gateway is the default endpoint.
	if cfg := resolveConfig("", ""); cfg.Gateway != gw.URL || cfg.RefreshToken != "rt-1" {
		t.Fatalf("resolved = %+v", cfg)
	}
}

// `bnbloc login` advertises the gateway's start path, polls the claim with
// its nonce until the grant arrives, and stores only the refresh token.
func TestLoginPollsClaimAndStoresRefreshToken(t *testing.T) {
	home := isolateHome(t)
	claimPollInterval, claimPollTimeout = 5*time.Millisecond, 5*time.Second
	t.Cleanup(func() { claimPollInterval, claimPollTimeout = 2*time.Second, 5*time.Minute })
	var opened string
	openBrowser = func(u string) error { opened = u; return nil }
	t.Cleanup(func() { openBrowser = openInBrowser })

	var claims atomic.Int32
	var nonce, revoked atomic.Value
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/auth/logout":
			var body struct {
				RefreshToken string `json:"refreshToken"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			revoked.Store(body.RefreshToken)
			w.WriteHeader(http.StatusNoContent)
		case "/v1/auth/sso-config":
			_, _ = io.WriteString(w, `{"authProviderAvailable":true,"authProviderStartPath":"/custom/start","directLoginAvailable":false}`)
		case "/v1/auth/sso/claim":
			var body struct {
				Nonce string `json:"nonce"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			nonce.Store(body.Nonce)
			if claims.Add(1) < 3 {
				http.Error(w, "no completed sign-in", http.StatusNotFound)
				return
			}
			_, _ = io.WriteString(w, `{"sessionToken":"st","refreshToken":"rt-login","subject":"admin@example.com","role":"super_admin"}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()

	if err := runLogin([]string{"--gateway", srv.URL + "/"}); err != nil {
		t.Fatal(err)
	}
	u, err := url.Parse(opened)
	if err != nil || u.Path != "/custom/start" {
		t.Fatalf("opened %q, want the advertised start path", opened)
	}
	redirect, err := url.Parse(u.Query().Get("redirect_uri"))
	if err != nil || redirect.Path != "/sso-callback" || redirect.Scheme+"://"+redirect.Host != srv.URL {
		t.Fatalf("redirect_uri = %q", u.Query().Get("redirect_uri"))
	}
	n := redirect.Query().Get("cli_nonce")
	if len(n) < 43 || n != nonce.Load() {
		t.Fatalf("cli_nonce %q (claimed with %v), want one 256-bit nonce", n, nonce.Load())
	}
	path := filepath.Join(home, ".bnbloc", "config.json")
	doc := readConfigFile(t, path)
	if doc["refreshToken"] != "rt-login" || doc["gateway"] != srv.URL || doc["subject"] != "admin@example.com" {
		t.Fatalf("stored = %v", doc)
	}
	if _, ok := doc["sessionToken"]; ok {
		t.Fatal("session token stored")
	}
	if st, _ := os.Stat(path); st.Mode().Perm() != 0o600 {
		t.Fatalf("config mode = %v", st.Mode().Perm())
	}

	if err := runLogout(nil); err != nil {
		t.Fatal(err)
	}
	doc = readConfigFile(t, path)
	if _, ok := doc["refreshToken"]; ok || doc["gateway"] != srv.URL {
		t.Fatalf("after logout = %v, want credential gone, gateway kept", doc)
	}
	if got, _ := revoked.Load().(string); got != "rt-login" {
		t.Fatalf("logout revoked %q on the gateway, want the stored refresh token", got)
	}
}

// A login against a gateway without SSO fails clearly instead of polling.
func TestLoginRequiresSSO(t *testing.T) {
	isolateHome(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"authProviderAvailable":false,"directLoginAvailable":true}`)
	}))
	defer srv.Close()
	if err := runLogin([]string{"--gateway", srv.URL}); err == nil || !strings.Contains(err.Error(), "no SSO") {
		t.Fatalf("err = %v", err)
	}
}
