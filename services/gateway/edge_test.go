package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/rbac"
	"github.com/luckylhb90/bnbloc/internal/store"
)

func mustParseAPIPath(t *testing.T, method, rawPath string) apiPath {
	t.Helper()
	p, err := parseAPIPath(httptest.NewRequest(method, rawPath, nil))
	if err != nil {
		t.Fatalf("parseAPIPath(%q): %v", rawPath, err)
	}
	return p
}

// BE-9: routing reads the exact room sub-resource segment, so a room whose id
// merely contains a route word reaches the right backend.
func TestRouteUsesRoomSubresourceSegment(t *testing.T) {
	s := &Service{Backends: Backends{Room: "room", Realtime: "rt", Activity: "act", ShareAgent: "share"}}
	cases := map[string]string{
		"/v1/rooms":                           "room",
		"/v1/rooms/chat-team":                 "room",
		"/v1/rooms/chat-team/members":         "room",
		"/v1/rooms/events2026/channels":       "room",
		"/v1/rooms/history-club/invites":      "room",
		"/v1/rooms/agents-guild/artifacts/a1": "room",
		"/v1/rooms/my-room/chat":              "rt",
		"/v1/rooms/my-room/events":            "rt",
		"/v1/rooms/my-room/messages/12":       "rt",
		"/v1/rooms/my-room/attachments/a":     "rt",
		"/v1/rooms/chat/agent-jobs":           "act",
		"/v1/rooms/r/agent-activity/messages": "act",
		"/v1/rooms/r/agent-catalogs":          "act",
		"/v1/rooms/r/team-plan":               "act",
		"/v1/rooms/r/shared-agents":           "share",
		"/v1/rooms/r/shared-agents-usage":     "share",
		"/v1/rooms-unread":                    "rt",
		"/v1/bridge/heartbeat":                "act",
		"/v1/my/agents":                       "act",
		"/v1/tenant/members":                  "share",
		"/v1/tenants/directory":               "share",
		"/v1/platform/tenants":                "share",
		"/v1/invites/abc/redeem":              "room",
		"/v1/users/alice/rooms":               "room",
		"/v1/devices/enroll":                  "",
		"/v1/roomsx":                          "",
		"/v1/my/other":                        "",
		"/v1/unknown":                         "",
	}
	for path, want := range cases {
		if got := s.route(mustParseAPIPath(t, http.MethodGet, path)); got != want {
			t.Errorf("route(%s) = %q, want %q", path, got, want)
		}
	}
}

// BE-9: an encoded slash cannot smuggle structure into a room id, and an
// unstorable room id is refused at the edge.
func TestParseAPIPathRejectsInvalidRoomIDs(t *testing.T) {
	for _, raw := range []string{"/v1/rooms/a%2Fchat/members", "/v1/rooms/" + strings.Repeat("x", 65) + "/chat", "/v1/rooms/a%00b/chat", "/v1/rooms//chat"} {
		if _, err := parseAPIPath(httptest.NewRequest(http.MethodGet, raw, nil)); err == nil {
			t.Errorf("parseAPIPath(%q) accepted an invalid path", raw)
		}
	}
	p := mustParseAPIPath(t, http.MethodGet, "/v1/rooms/%E4%BA%A7%E5%93%81/chat")
	if p.RoomID != "产品" || p.RoomSub != "chat" {
		t.Fatalf("decoded room = %q sub = %q", p.RoomID, p.RoomSub)
	}
}

// BE-22: a viewer's read-side room actions are allowed; cost-bearing writes
// are not.
func TestViewerMayMarkReadTypeJoinLeave(t *testing.T) {
	for _, sub := range []string{"read", "typing", "join", "leave"} {
		perm := requiredPermission(http.MethodPost, mustParseAPIPath(t, http.MethodPost, "/v1/rooms/r/"+sub))
		if !rbac.Can(string(rbac.RoleViewer), perm) {
			t.Errorf("viewer refused POST /%s (perm %s)", sub, perm)
		}
	}
	for _, path := range []string{"/v1/rooms/r/chat", "/v1/rooms/r/shared-agents/x/authorization", "/v1/rooms", "/v1/rooms/r/read/extra"} {
		perm := requiredPermission(http.MethodPost, mustParseAPIPath(t, http.MethodPost, path))
		if rbac.Can(string(rbac.RoleViewer), perm) {
			t.Errorf("viewer allowed POST %s", path)
		}
	}
	if requiredPermission(http.MethodGet, mustParseAPIPath(t, http.MethodGet, "/v1/tenant/audit")) != rbac.PermManageOrg {
		t.Error("tenant admin GET must require manage_org")
	}
}

// BE-16: X-Forwarded-For is believed only through configured trusted proxies.
func TestClientIPTrustsOnlyConfiguredProxies(t *testing.T) {
	req := func(remote string, xff ...string) *http.Request {
		r := httptest.NewRequest(http.MethodPost, "/v1/auth/login", nil)
		r.RemoteAddr = remote
		for _, v := range xff {
			r.Header.Add("X-Forwarded-For", v)
		}
		return r
	}
	untrusting := &Service{}
	if got := untrusting.clientIP(req("203.0.113.9:5000", "1.2.3.4")); got != "203.0.113.9" {
		t.Fatalf("no trusted proxies: clientIP = %s, want the TCP peer", got)
	}
	s := &Service{TrustedProxies: []netip.Prefix{netip.MustParsePrefix("10.0.0.0/8")}}
	// Client spoofs a left-most entry; the edge proxy appends the real peer.
	if got := s.clientIP(req("10.0.0.2:443", "6.6.6.6, 198.51.100.7")); got != "198.51.100.7" {
		t.Fatalf("spoofed XFF: clientIP = %s, want 198.51.100.7", got)
	}
	// Two trusted hops in front of the client.
	if got := s.clientIP(req("10.0.0.2:443", "198.51.100.7", "10.1.1.1")); got != "198.51.100.7" {
		t.Fatalf("chained proxies: clientIP = %s, want 198.51.100.7", got)
	}
	// An untrusted peer's XFF is ignored entirely.
	if got := s.clientIP(req("198.51.100.8:1", "1.1.1.1")); got != "198.51.100.8" {
		t.Fatalf("untrusted peer: clientIP = %s", got)
	}
	// Malformed entries stop the walk at the last trustworthy hop.
	if got := s.clientIP(req("10.0.0.2:443", "garbage")); got != "10.0.0.2" {
		t.Fatalf("malformed XFF: clientIP = %s, want the proxy", got)
	}
}

// BE-13: the daemon token is a member-level credential, not a bypass: platform
// and tenant-admin paths are refused, room scope still applies, and a wrong
// token is simply unauthorized.
func TestDaemonTokenGoesThroughScopeAndRBAC(t *testing.T) {
	var hits []string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits = append(hits, r.URL.Path+" as "+r.Header.Get("X-Bnbloc-User"))
	}))
	defer backend.Close()
	const daemonToken = "a-strong-daemon-token-for-tests"
	gw := &Service{
		Signer:   auth.NewSigner([]byte("k")),
		Backends: Backends{Room: backend.URL, ShareAgent: backend.URL, Activity: backend.URL, LocalAgentToken: daemonToken},
		Rooms: func(subject, _ string) []string {
			if subject == daemonServiceSubject {
				return []string{"daemon-room"}
			}
			return nil
		},
	}
	srv := httptest.NewServer(gw.Handler())
	defer srv.Close()
	do := func(method, path, token string) int {
		req, _ := http.NewRequest(method, srv.URL+path, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := do(http.MethodGet, "/v1/platform/tenants", daemonToken); c != http.StatusForbidden {
		t.Errorf("daemon on platform path: %d, want 403", c)
	}
	if c := do(http.MethodPost, "/v1/tenant/members", daemonToken); c != http.StatusForbidden {
		t.Errorf("daemon on tenant-admin path: %d, want 403", c)
	}
	if c := do(http.MethodPost, "/v1/rooms/other-room/agent-activity/messages", daemonToken); c != http.StatusForbidden {
		t.Errorf("daemon outside its rooms: %d, want 403", c)
	}
	if c := do(http.MethodPost, "/v1/rooms/daemon-room/agent-activity/messages", daemonToken+"x"); c != http.StatusUnauthorized {
		t.Errorf("wrong daemon token: %d, want 401", c)
	}
	if c := do(http.MethodPost, "/v1/rooms/daemon-room/agent-activity/messages", daemonToken); c != http.StatusOK {
		t.Errorf("daemon in its room: %d, want 200", c)
	}
	if len(hits) != 1 || hits[0] != "/v1/rooms/daemon-room/agent-activity/messages as "+daemonServiceSubject {
		t.Fatalf("backend hits = %v", hits)
	}
}

// BE-25: a URL-borne session token is accepted only by the SSE stream, never
// by other routes (an attachment read takes a ticket instead,
// TestAttachmentTicketReadsOneAttachment).
func TestAccessTokenQueryOnlyOnStream(t *testing.T) {
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {}))
	defer backend.Close()
	gw := newGateway(t, backend.URL)
	gw.Backends.Realtime = backend.URL
	srv := httptest.NewServer(gw.Handler())
	defer srv.Close()
	token := gw.Signer.Issue(auth.Claims{Subject: "alice", Rooms: []string{"room-1"}, ExpiresAtUnix: time.Now().Add(time.Hour).Unix()})
	get := func(path string) int {
		resp, err := http.Get(srv.URL + path + "?access_token=" + url.QueryEscape(token))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		return resp.StatusCode
	}
	if c := get("/v1/rooms/room-1/events"); c != http.StatusOK {
		t.Errorf("GET events with access_token: %d, want 200", c)
	}
	for _, bad := range []string{
		"/v1/rooms/room-1/attachments/att-1", "/v1/rooms/room-1/artifacts/art_1/versions/2/content",
		"/v1/rooms/room-1/members", "/v1/rooms/room-1/history", "/v1/users/alice/rooms",
	} {
		if c := get(bad); c != http.StatusUnauthorized {
			t.Errorf("GET %s with access_token: %d, want 401", bad, c)
		}
	}
}

// BE-12: the SSO callback only lands same-origin or on an allowed origin.
func TestSSORelayTargetRejectsOpenRedirects(t *testing.T) {
	s := &Service{AllowedOrigins: []string{"https://app.example.com"}}
	r := httptest.NewRequest(http.MethodPost, "https://gw.example.com/api/auth/acs", nil)
	for _, ok := range []string{"", "/", "/sso-callback", "/sso-callback?desktop_nonce=n", "https://gw.example.com/sso-callback", "https://app.example.com/cb"} {
		if _, err := s.ssoRelayTarget(r, ok); err != nil {
			t.Errorf("relay %q rejected: %v", ok, err)
		}
	}
	for _, bad := range []string{"https://evil.example/cb", "//evil.example/cb", "/\\evil.example", "javascript:alert(1)", "tauri://localhost/x", "https://user@gw.example.com/", "sso-callback", "http://app.example.com.evil/"} {
		if _, err := s.ssoRelayTarget(r, bad); err == nil {
			t.Errorf("relay %q accepted", bad)
		}
	}
}

func ssoGateway(t *testing.T) *Service {
	t.Helper()
	return &Service{
		Signer:     auth.NewSigner([]byte("k")),
		Now:        time.Now,
		SessionTTL: time.Hour,
		TenantResolver: func(id auth.Identity, def string) (string, string, error) {
			if id.Subject == "carol" {
				return "frozen", "member", nil
			}
			return def, "member", nil
		},
		TenantSuspended: func(tenantID string) bool { return tenantID == "frozen" },
	}
}

func completeSSOFor(t *testing.T, s *Service, subject, relay string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	s.completeSSO(rec, httptest.NewRequest(http.MethodPost, "https://gw.example.com/api/auth/acs", nil), auth.Identity{Subject: subject}, relay)
	return rec
}

// BE-12: SSO shares the login path's suspended-tenant gate.
func TestSSORefusesSuspendedTenant(t *testing.T) {
	if rec := completeSSOFor(t, ssoGateway(t), "carol", "/sso-callback"); rec.Code != http.StatusForbidden {
		t.Fatalf("suspended-tenant SSO: %d, want 403", rec.Code)
	}
	if rec := completeSSOFor(t, ssoGateway(t), "alice", "https://evil.example/cb"); rec.Code != http.StatusBadRequest {
		t.Fatalf("open-redirect SSO: %d, want 400", rec.Code)
	}
}

// DT-4: a desktop-initiated flow gets a one-time code bound to its nonce (never
// the session token); the code redeems once, only with that nonce.
func TestDesktopSSOHandsOffNonceBoundOneTimeCode(t *testing.T) {
	s := ssoGateway(t)
	rec := completeSSOFor(t, s, "alice", "/sso-callback?desktop_nonce=nonce-1")
	if rec.Code != http.StatusFound {
		t.Fatalf("status = %d", rec.Code)
	}
	loc, _ := url.Parse(rec.Header().Get("Location"))
	frag, _ := url.ParseQuery(loc.Fragment)
	code := frag.Get("ssoCode")
	if code == "" || frag.Get("sessionToken") != "" || strings.Contains(loc.RawQuery, "desktop_nonce") {
		t.Fatalf("desktop redirect = %s, want only an ssoCode fragment", loc)
	}
	srv := httptest.NewServer(s.Handler())
	defer srv.Close()
	exchange := func(code, nonce string) (int, sessionGrant) {
		body, _ := json.Marshal(map[string]string{"code": code, "nonce": nonce})
		resp, err := http.Post(srv.URL+"/v1/auth/sso/exchange", "application/json", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var g sessionGrant
		raw, _ := io.ReadAll(resp.Body)
		_ = json.Unmarshal(raw, &g)
		return resp.StatusCode, g
	}
	// A different nonce (another desktop / login CSRF) cannot redeem it, and
	// the attempt burns the code.
	if c, _ := exchange(code, "attacker-nonce"); c != http.StatusUnauthorized {
		t.Fatalf("wrong nonce: %d, want 401", c)
	}
	if c, _ := exchange(code, "nonce-1"); c != http.StatusUnauthorized {
		t.Fatalf("burned code: %d, want 401", c)
	}
	// A fresh code redeems once with its own nonce.
	rec = completeSSOFor(t, s, "alice", "https://gw.example.com/sso-callback?desktop_nonce=nonce-2")
	loc, _ = url.Parse(rec.Header().Get("Location"))
	frag, _ = url.ParseQuery(loc.Fragment)
	c, g := exchange(frag.Get("ssoCode"), "nonce-2")
	if c != http.StatusOK || g.Subject != "alice" {
		t.Fatalf("exchange = %d %+v", c, g)
	}
	if claims, err := s.Signer.Verify(g.Token); err != nil || claims.Subject != "alice" {
		t.Fatalf("exchanged token invalid: %v", err)
	}
	if c, _ := exchange(frag.Get("ssoCode"), "nonce-2"); c != http.StatusUnauthorized {
		t.Fatalf("replayed code: %d, want 401", c)
	}
}

func TestSSOCodeExpires(t *testing.T) {
	s := ssoGateway(t)
	now := time.Now()
	s.Now = func() time.Time { return now }
	code, err := s.parkDesktopHandoff(context.Background(), auth.Identity{Subject: "alice"}, "n")
	if err != nil {
		t.Fatal(err)
	}
	s.Now = func() time.Time { return now.Add(ssoCodeTTL + time.Second) }
	if _, ok, _ := s.redeemDesktopHandoff(context.Background(), code, "n"); ok {
		t.Fatal("expired code redeemed")
	}
}

// H3: a hand-off parked by one gateway replica is redeemed by another that
// shares the store, exactly once.
func TestSSOHandoffRedeemsOnAnotherReplica(t *testing.T) {
	shared := store.NewMemorySSOHandoff()
	a, b := ssoGateway(t), ssoGateway(t)
	a.SSOHandoffs, b.SSOHandoffs = shared, shared
	rec := completeSSOFor(t, a, "alice", "/sso-callback?desktop_nonce=nonce-x")
	loc, _ := url.Parse(rec.Header().Get("Location"))
	frag, _ := url.ParseQuery(loc.Fragment)
	code := frag.Get("ssoCode")
	if code == "" {
		t.Fatalf("no code in %s", loc)
	}
	id, ok, err := b.redeemDesktopHandoff(context.Background(), code, "nonce-x")
	if err != nil || !ok || id.Subject != "alice" {
		t.Fatalf("redeem on replica b = %+v %v %v", id, ok, err)
	}
	if _, ok, _ := a.redeemDesktopHandoff(context.Background(), code, "nonce-x"); ok {
		t.Fatal("code redeemed twice across replicas")
	}
}

// A browser (non-desktop) SSO flow keeps the fragment session hand-off.
func TestBrowserSSOKeepsFragmentSession(t *testing.T) {
	rec := completeSSOFor(t, ssoGateway(t), "alice", "/sso-callback")
	loc, _ := url.Parse(rec.Header().Get("Location"))
	frag, _ := url.ParseQuery(loc.Fragment)
	if loc.Path != "/sso-callback" || frag.Get("sessionToken") == "" || frag.Get("refreshToken") == "" || frag.Get("subject") != "alice" {
		t.Fatalf("browser redirect = %s", loc)
	}
}

// BE-18: the membership fallback caches positive results only, and both edge
// caches are bounded.
func TestSubjectInRoomCachesPositiveOnly(t *testing.T) {
	calls := 0
	member := false
	s := &Service{Rooms: func(string, string) []string {
		calls++
		if member {
			return []string{"r"}
		}
		return nil
	}}
	if s.subjectInRoom("alice", "r", "t") || s.subjectInRoom("alice", "r", "t") {
		t.Fatal("non-member admitted")
	}
	if calls != 2 {
		t.Fatalf("denials must not be cached: calls=%d", calls)
	}
	member = true
	if !s.subjectInRoom("alice", "r", "t") || !s.subjectInRoom("alice", "r", "t") {
		t.Fatal("member refused")
	}
	if calls != 3 {
		t.Fatalf("positive result should be cached: calls=%d", calls)
	}
}

func TestBoundedCacheStaysBoundedAndExpires(t *testing.T) {
	c := newBoundedCache[int](time.Minute, 100)
	now := time.Now()
	c.now = func() time.Time { return now }
	for i := 0; i < 1000; i++ {
		c.put(time.Duration(i).String(), i)
	}
	if n := c.len(); n > 100 {
		t.Fatalf("cache grew to %d, cap 100", n)
	}
	c.put("x", 1)
	now = now.Add(2 * time.Minute)
	if _, ok := c.get("x"); ok {
		t.Fatal("expired entry returned")
	}
}
