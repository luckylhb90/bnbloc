package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/core/manifestcore"
	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/coreset"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/shard"
	"github.com/luckylhb90/bnbloc/internal/store"
)

type fakeVerifier struct{ subject string }

func (f fakeVerifier) Verify(token string) (auth.Identity, error) {
	if token == "good" {
		return auth.Identity{Subject: f.subject}, nil
	}
	return auth.Identity{}, auth.ErrInvalidToken
}

func newGateway(t *testing.T, roomBackend string) *Service {
	t.Helper()
	return &Service{
		Verifier: fakeVerifier{subject: "alice"},
		Signer:   auth.NewSigner([]byte("test-key")),
		Backends: Backends{Room: roomBackend},
		Rooms:    func(subject, tenantID string) []string { return []string{"room-1"} },
	}
}

func login(t *testing.T, srv *httptest.Server, idp string) (string, int) {
	t.Helper()
	body, _ := json.Marshal(loginBody{IDPToken: idp})
	resp, err := http.Post(srv.URL+"/v1/auth/login", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", resp.StatusCode
	}
	var out struct {
		SessionToken string `json:"sessionToken"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return out.SessionToken, resp.StatusCode
}

// A valid IdP token yields a session token; an invalid one is rejected.
func TestLoginIssuesToken(t *testing.T) {
	srv := httptest.NewServer(newGateway(t, "").Handler())
	defer srv.Close()
	token, _ := login(t, srv, "good")
	if token == "" {
		t.Fatal("expected session token")
	}
	if _, status := login(t, srv, "bad"); status != http.StatusUnauthorized {
		t.Fatalf("bad login status = %d, want 401", status)
	}
}

// An authenticated request within scope is proxied with X-Bnbloc-User injected.
func TestProxyInjectsSubjectAndEnforcesScope(t *testing.T) {
	// Backend echoes the injected user header.
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Header.Get("X-Bnbloc-User"))
	}))
	defer backend.Close()

	gw := newGateway(t, backend.URL)
	srv := httptest.NewServer(gw.Handler())
	defer srv.Close()
	token, _ := login(t, srv, "good")

	// In-scope room -> proxied, header injected.
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/rooms/room-1/anything", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, _ := http.DefaultClient.Do(req)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK || string(raw) != "alice" {
		t.Fatalf("proxy status=%d body=%q, want 200 'alice'", resp.StatusCode, raw)
	}

	// Out-of-scope room -> 403 fail closed.
	req2, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/rooms/room-OTHER/anything", nil)
	req2.Header.Set("Authorization", "Bearer "+token)
	resp2, _ := http.DefaultClient.Do(req2)
	resp2.Body.Close()
	if resp2.StatusCode != http.StatusForbidden {
		t.Fatalf("out-of-scope status = %d, want 403", resp2.StatusCode)
	}
}

// A client cannot spoof X-Bnbloc-User; the gateway strips and re-sets it.
func TestProxyStripsClientUserHeader(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Header.Get("X-Bnbloc-User"))
	}))
	defer backend.Close()
	gw := newGateway(t, backend.URL)
	srv := httptest.NewServer(gw.Handler())
	defer srv.Close()
	token, _ := login(t, srv, "good")

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/rooms/room-1/anything", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Bnbloc-User", "spoofed") // attacker attempt
	resp, _ := http.DefaultClient.Do(req)
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(raw) != "alice" {
		t.Fatalf("injected user = %q, want alice (spoof ignored)", raw)
	}
}

// The replica-to-replica ownership marker never passes the edge: a client
// could otherwise pin its request to whichever replica it landed on.
func TestProxyStripsForwardMarker(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, r.Header.Get(shard.ForwardedHeader))
	}))
	defer backend.Close()
	srv := httptest.NewServer(newGateway(t, backend.URL).Handler())
	defer srv.Close()
	token, _ := login(t, srv, "good")
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/rooms/room-1/anything", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set(shard.ForwardedHeader, "10.0.0.9:8082")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if len(raw) != 0 {
		t.Fatalf("backend saw forward marker %q", raw)
	}
}

// A request without a valid session token is rejected.
func TestProxyRejectsMissingToken(t *testing.T) {
	srv := httptest.NewServer(newGateway(t, "http://unused").Handler())
	defer srv.Close()
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/rooms/room-1/x", nil)
	resp, _ := http.DefaultClient.Do(req)
	resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", resp.StatusCode)
	}
}

// The /v1/rooms collection endpoint (no roomId) routes to the room backend.
// Regression: an earlier prefix "/v1/rooms/" (trailing slash) dropped it.
func TestProxyRoutesRoomCollection(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "room-backend")
	}))
	defer backend.Close()
	gw := newGateway(t, backend.URL)
	srv := httptest.NewServer(gw.Handler())
	defer srv.Close()
	token, _ := login(t, srv, "good")

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/rooms", bytes.NewReader([]byte(`{"roomId":"r1"}`)))
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("create room: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(raw) != "room-backend" {
		t.Fatalf("collection route status=%d body=%q, want 200 room-backend", resp.StatusCode, raw)
	}
}

// The /v1/users/{id}/rooms endpoint (room list) must route to the room backend.
// Regression: it returned 404 "no route" so the web client's room list was
// always empty and rooms vanished on reload.
func TestProxyRoutesUserRooms(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, `{"rooms":["room-1"]}`)
	}))
	defer backend.Close()
	gw := newGateway(t, backend.URL)
	srv := httptest.NewServer(gw.Handler())
	defer srv.Close()
	token, _ := login(t, srv, "good")

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/users/good/rooms", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("user rooms: %v", err)
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK || string(raw) != `{"rooms":["room-1"]}` {
		t.Fatalf("user-rooms route status=%d body=%q, want 200 rooms json", resp.StatusCode, raw)
	}
}

// A preflight OPTIONS from the desktop shell origin is answered before auth
// (204 with the allow-origin header), not routed to the authenticated proxy.
func TestCORSPreflightFromShellOriginBeforeAuth(t *testing.T) {
	srv := httptest.NewServer(newGateway(t, "").Handler())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodOptions, srv.URL+"/v1/auth/login", nil)
	req.Header.Set("Origin", "tauri://localhost")
	req.Header.Set("Access-Control-Request-Method", "POST")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusNoContent {
		t.Fatalf("preflight status = %d, want 204", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "tauri://localhost" {
		t.Fatalf("allow-origin = %q, want tauri://localhost", got)
	}
	// The client sends X-Bnbloc-Acting-Tenant on requests when a super-admin has
	// switched tenant; the preflight MUST allow it or the browser/webview blocks
	// every such request (including login when a stale value is persisted).
	if got := resp.Header.Get("Access-Control-Allow-Headers"); !strings.Contains(got, "X-Bnbloc-Acting-Tenant") {
		t.Fatalf("allow-headers = %q, want it to include X-Bnbloc-Acting-Tenant", got)
	}
}

// A preflight from an unlisted origin is refused (403), never proxied.
func TestCORSPreflightRejectsUnknownOrigin(t *testing.T) {
	srv := httptest.NewServer(newGateway(t, "").Handler())
	defer srv.Close()

	req, _ := http.NewRequest(http.MethodOptions, srv.URL+"/v1/auth/login", nil)
	req.Header.Set("Origin", "https://evil.example")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("preflight: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Fatalf("unknown-origin preflight status = %d, want 403", resp.StatusCode)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Fatal("unknown origin must not receive an allow-origin header")
	}
}

// A real login from the shell origin carries the allow-origin header so the
// webview can read the response.
func TestCORSLoginFromShellOriginEchoesAllowOrigin(t *testing.T) {
	srv := httptest.NewServer(newGateway(t, "").Handler())
	defer srv.Close()

	body, _ := json.Marshal(loginBody{IDPToken: "good"})
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/auth/login", bytes.NewReader(body))
	req.Header.Set("Origin", "tauri://localhost")
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("login: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("login status = %d, want 200", resp.StatusCode)
	}
	if got := resp.Header.Get("Access-Control-Allow-Origin"); got != "tauri://localhost" {
		t.Fatalf("allow-origin = %q, want tauri://localhost", got)
	}
}

// fakeCoreProvider is a no-op Provider used to register a manifest-style core
// under an arbitrary name for the /v1/cores test.
type fakeCoreProvider struct{ name string }

func (p fakeCoreProvider) Name() string { return p.name }
func (fakeCoreProvider) CreateWorkspace(context.Context, core.Endpoint) (string, error) {
	return "", nil
}
func (fakeCoreProvider) CreateSession(context.Context, core.Endpoint, core.SessionRequest) (core.SessionResult, error) {
	return core.SessionResult{}, nil
}
func (fakeCoreProvider) SendInput(context.Context, core.Endpoint, string, string, string, core.SessionOptions) error {
	return nil
}
func (fakeCoreProvider) ListTargets(context.Context, core.Endpoint) ([]core.Target, error) {
	return nil, nil
}
func (fakeCoreProvider) SessionIdle(context.Context, core.Endpoint, string, string) (bool, error) {
	return true, nil
}
func (fakeCoreProvider) LatestSession(context.Context, core.Endpoint, string) (string, error) {
	return "", nil
}
func (fakeCoreProvider) Messages(context.Context, core.Endpoint, string, string, int64) ([]core.Message, int64, error) {
	return nil, 0, nil
}
func (fakeCoreProvider) PollTurn(context.Context, core.Endpoint, string, string) (core.TurnStatus, error) {
	return core.TurnStatus{Idle: true}, nil
}

// GET /v1/cores lists every registered core: a configured core with a resolved
// endpoint is available, a registered-but-unconfigured manifest core is not,
// and a registry-registered (builtin-style) core is flagged builtin.
func TestCoresListsRegisteredWithAvailability(t *testing.T) {
	const manifestName = "test-manifest-core"
	if err := core.Reregister(manifestName, func() core.Provider { return fakeCoreProvider{name: manifestName} }, core.CoreExtension{Backend: core.BackendManifest, Source: core.SourceInstalled, Removable: true}); err != nil {
		t.Fatalf("reregister: %v", err)
	}
	t.Cleanup(func() { _ = core.Unregister(manifestName) })

	gw := newGateway(t, "")
	// A registered fake with a resolved endpoint is available; the manifest
	// core is registered but not in s.Cores (not available).
	const wired = "wired-core"
	core.Register(wired, func() core.Provider { return fakeCoreProvider{name: wired} })
	t.Cleanup(func() { _ = core.Unregister(wired) })
	wiredProvider, err := core.New(wired)
	if err != nil {
		t.Fatalf("new %s: %v", wired, err)
	}
	gw.Cores = []coreset.Entry{{
		Name:     wiredProvider.Name(),
		Provider: wiredProvider,
		Endpoint: core.Endpoint{BaseURL: "http://127.0.0.1:9999"},
	}}

	srv := httptest.NewServer(gw.Handler())
	defer srv.Close()
	token, _ := login(t, srv, "good")

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/cores", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("cores: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("cores status = %d, want 200", resp.StatusCode)
	}
	var out struct {
		Cores []struct {
			Name      string `json:"name"`
			Available bool   `json:"available"`
			Builtin   bool   `json:"builtin"`
		} `json:"cores"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	byName := map[string]struct {
		available, builtin bool
	}{}
	for _, c := range out.Cores {
		byName[c.Name] = struct{ available, builtin bool }{c.Available, c.Builtin}
	}
	if got, ok := byName[wired]; !ok || !got.available || !got.builtin {
		t.Fatalf("%s = %+v (present=%v), want available+builtin", wired, got, ok)
	}
	if _, ok := byName[manifestName]; ok {
		t.Fatalf("%s leaked without a persisted component record", manifestName)
	}

	// The endpoint requires a valid session.
	anon, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/cores", nil)
	aresp, _ := http.DefaultClient.Do(anon)
	aresp.Body.Close()
	if aresp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("anon cores status = %d, want 401", aresp.StatusCode)
	}
}

func TestCoresHidesInstalledRegistryEntry_withoutPersistedComponentRecord(t *testing.T) {
	// Given
	const name = "registry-only-sidecar"
	if err := core.Reregister(name, func() core.Provider { return fakeCoreProvider{name: name} }, core.CoreExtension{Backend: core.BackendSidecar, Source: core.SourceInstalled, Removable: true}); err != nil {
		t.Fatalf("reregister: %v", err)
	}
	t.Cleanup(func() { _ = core.Unregister(name) })
	gw := newGateway(t, "")
	gw.PlatformSettings = store.NewMemoryPlatformSettings()
	srv := httptest.NewServer(gw.Handler())
	defer srv.Close()
	token, _ := login(t, srv, "good")
	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/cores", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	// When
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("cores: %v", err)
	}
	defer resp.Body.Close()

	// Then
	var out struct {
		Cores []struct {
			Name string `json:"name"`
		} `json:"cores"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, listed := range out.Cores {
		if listed.Name == name {
			t.Fatalf("registry-only core %q leaked to /v1/cores", name)
		}
	}
}

// GET /v1/cores filters an installed core by its per-tenant enablement overlay:
// a core scoped to tenant t1 is visible to a t1 caller and hidden from a t2
// caller; a builtin (no overlay) is visible to both.
func TestCoresTenantEnablementFilter(t *testing.T) {
	const scoped = "tenant-scoped-core"

	gw := newGateway(t, "")
	gw.PlatformSettings = store.NewMemoryPlatformSettings()
	gw.AllowExtUnsigned = true
	t.Cleanup(func() { _ = core.Unregister(scoped) })

	// A registered builtin (no tenant overlay) must stay visible to both
	// tenants while the scoped core filters.
	const builtinName = "builtin-no-overlay"
	core.Register(builtinName, func() core.Provider { return fakeCoreProvider{name: builtinName} })
	t.Cleanup(func() { _ = core.Unregister(builtinName) })

	// Seed the install record through the pipeline, then scope it to tenant t1.
	pkgBody, _ := json.Marshal(legacyManifestPackage(manifestcore.Manifest{
		Name:          scoped,
		CreateWksp:    manifestcore.Op{Method: "POST", Path: "/w"},
		CreateSession: manifestcore.Op{Method: "POST", Path: "/s"},
		SendInput:     manifestcore.Op{Method: "POST", Path: "/i"},
		ListTargets:   manifestcore.Op{Method: "GET", Path: "/t"},
		Messages:      manifestcore.Op{Method: "GET", Path: "/m", Items: &manifestcore.ItemsSpec{List: "messages", Fields: map[string]string{"text": "text"}}},
		PollTurn:      manifestcore.Op{Method: "GET", Path: "/turn"},
	}))
	if _, err := gw.legacyManifestInstallPipeline().Install(t.Context(), extpipe.InstallRequest{Body: pkgBody, Actor: "root"}); err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := gw.corePipeline().SetTenants(t.Context(), core.KindCore, scoped, []string{"t1"}, "root"); err != nil {
		t.Fatalf("set tenants: %v", err)
	}

	srv := httptest.NewServer(gw.Handler())
	defer srv.Close()

	// Mint a session token bound to a tenant directly (bypasses login/verifier).
	seesCore := func(tenant string) map[string]bool {
		token := gw.Signer.Issue(auth.Claims{Subject: "u", Tenant: tenant, Role: "member", ExpiresAtUnix: time.Now().Add(time.Hour).Unix()})
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/cores", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("cores %s: %v", tenant, err)
		}
		defer resp.Body.Close()
		var out struct {
			Cores []struct {
				Name string `json:"name"`
			} `json:"cores"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		set := map[string]bool{}
		for _, c := range out.Cores {
			set[c.Name] = true
		}
		return set
	}

	t1 := seesCore("t1")
	if !t1[scoped] {
		t.Fatalf("t1 caller should see the core scoped to t1; got %v", t1)
	}
	if !t1[builtinName] {
		t.Fatalf("t1 caller should still see the builtin core; got %v", t1)
	}
	t2 := seesCore("t2")
	if t2[scoped] {
		t.Fatalf("t2 caller must NOT see a core scoped to t1; got %v", t2)
	}
	if !t2[builtinName] {
		t.Fatalf("t2 caller should still see the builtin core; got %v", t2)
	}
}

// A suspended tenant refuses login for its members, but a platform super-admin
// is exempt (they must reach a suspended org to re-activate it). This is the
// suspend enforcement boundary — without the exemption a super-admin could lock
// themselves out of the org they need to fix.
func TestLoginRejectsSuspendedTenantExceptSuperAdmin(t *testing.T) {
	svc := newGateway(t, "")
	// Resolver maps "boss" -> org_admin of the suspended tenant, "root" ->
	// super_admin. The suspended check reports acme as suspended.
	svc.Verifier = fakeVerifier{subject: "boss"}
	svc.TenantResolver = func(id auth.Identity, _ string) (string, string, error) {
		if id.Subject == "root" {
			return "acme", "super_admin", nil
		}
		return "acme", "org_admin", nil
	}
	svc.TenantSuspended = func(tenantID string) bool { return tenantID == "acme" }
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()

	// A member of the suspended tenant is refused.
	if _, status := login(t, srv, "good"); status != http.StatusForbidden {
		t.Fatalf("suspended-tenant member login = %d, want 403", status)
	}
	// A super-admin still gets in.
	svc.Verifier = fakeVerifier{subject: "root"}
	if _, status := login(t, srv, "good"); status != http.StatusOK {
		t.Fatalf("super-admin login to suspended tenant = %d, want 200", status)
	}
}

// The X-Bnbloc-Acting-Tenant header re-scopes a super-admin's requests to
// another tenant, and is IGNORED for a non-super-admin (who stays bound to
// their own tenant). The backend sees the authoritative X-Bnbloc-Tenant.
func TestActingTenantHeaderHonoredOnlyForSuperAdmin(t *testing.T) {
	var gotTenant string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTenant = r.Header.Get("X-Bnbloc-Tenant")
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	svc := newGateway(t, "")
	svc.Backends.ShareAgent = backend.URL
	role := "super_admin"
	svc.TenantResolver = func(id auth.Identity, _ string) (string, string, error) { return "home", role, nil }
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()

	call := func() string {
		token, _ := login(t, srv, "good")
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/v1/tenant/usage", nil)
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("X-Bnbloc-Acting-Tenant", "acme")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("request: %v", err)
		}
		resp.Body.Close()
		return gotTenant
	}

	// Super-admin: the acting-tenant header re-scopes to acme.
	if got := call(); got != "acme" {
		t.Fatalf("super-admin acting tenant = %q, want acme", got)
	}
	// org_admin: the header is ignored; the backend sees the home tenant.
	role = "org_admin"
	if got := call(); got != "home" {
		t.Fatalf("org_admin acting tenant = %q, want home (header must be ignored)", got)
	}
}

// A member of multiple tenants can switch: the re-issued token carries the
// target tenant and the caller's role THERE. A non-member is refused. This is
// the ordinary-user counterpart to the super-admin acting-tenant header.
func TestSwitchTenantReissuesScopedToken(t *testing.T) {
	svc := newGateway(t, "")
	svc.TenantResolver = func(_ auth.Identity, _ string) (string, string, error) { return "home", "member", nil }
	svc.TenantMemberRole = func(tenantID, userID string) (string, bool) {
		if tenantID == "acme" && userID == "alice" {
			return "org_admin", true
		}
		return "", false
	}
	srv := httptest.NewServer(svc.Handler())
	defer srv.Close()

	token, _ := login(t, srv, "good") // subject "alice", tenant home, role member

	switchTo := func(tid string) (int, auth.Claims) {
		body, _ := json.Marshal(map[string]string{"tenantId": tid})
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/v1/auth/switch-tenant", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("switch: %v", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return resp.StatusCode, auth.Claims{}
		}
		var out struct {
			SessionToken string `json:"sessionToken"`
		}
		_ = json.NewDecoder(resp.Body).Decode(&out)
		claims, err := svc.Signer.Verify(out.SessionToken)
		if err != nil {
			t.Fatalf("verify reissued token: %v", err)
		}
		return resp.StatusCode, claims
	}

	status, claims := switchTo("acme")
	if status != http.StatusOK {
		t.Fatalf("switch to member tenant = %d, want 200", status)
	}
	if claims.Tenant != "acme" || claims.Role != "org_admin" {
		t.Fatalf("reissued claims = tenant %q role %q, want acme/org_admin", claims.Tenant, claims.Role)
	}
	if status, _ := switchTo("globex"); status != http.StatusForbidden {
		t.Fatalf("switch to non-member tenant = %d, want 403", status)
	}
}

// subjectInRoom's cross-tenant fallback: the tenant-scoped Rooms lookup misses
// a room in another tenant, but the tenant-agnostic InRoom probe finds it, so a
// multi-tenant member (or the bridge acting as one) is authorized. Without
// InRoom, the miss stays a miss (guard stays strict for real non-members).
func TestSubjectInRoomCrossTenantFallback(t *testing.T) {
	svc := &Service{
		Rooms:  func(subject, tenantID string) []string { return []string{"acme-room"} },
		InRoom: func(subject, roomID string) bool { return roomID == "demo-clean" && subject == "maya" },
	}
	if !svc.subjectInRoom("maya", "acme-room", "acme") {
		t.Fatal("same-tenant member must pass via Rooms")
	}
	if !svc.subjectInRoom("maya", "demo-clean", "acme") {
		t.Fatal("cross-tenant member must pass via InRoom fallback")
	}
	if svc.subjectInRoom("maya", "other-room", "acme") {
		t.Fatal("genuine non-member must still be refused")
	}
	// With no InRoom hook, the cross-tenant room stays a miss.
	noProbe := &Service{Rooms: func(string, string) []string { return []string{"acme-room"} }}
	if noProbe.subjectInRoom("maya", "demo-clean", "acme") {
		t.Fatal("without InRoom, cross-tenant room must not pass")
	}
}
