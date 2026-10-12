// Package gateway implements the bnbloc-gateway: the single public entry that
// exchanges an enterprise IdP token for a bnbloc session token, verifies session
// tokens, and reverse-proxies authorized requests to the owning backend service.
package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httputil"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/coreset"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/notify"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/orgsync"
	"github.com/luckylhb90/bnbloc/internal/rbac"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/sealbox"
	"github.com/luckylhb90/bnbloc/internal/shard"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Backends holds the upstream base URLs the gateway proxies to.
type Backends struct {
	Room       string
	Activity   string
	ShareAgent string
	Realtime   string
	// Relay is the relay's internal URL, read only to report its /version
	// in the deployment view (the gateway never proxies the relay); "" omits it.
	Relay string
}

// Service holds the gateway dependencies.
type Service struct {
	Verifier auth.IDPVerifier
	Signer   *auth.Signer
	Backends Backends
	// Rooms resolves a subject's room memberships at login; production reads
	// bnbloc-room, tests inject a fake.
	Rooms func(subject, tenantID string) []string
	// Membership answers a subject's membership of a room in any tenant
	// (role, human or agent host): bnbloc-room's probe behind
	// membership.RoomClient, positive answers cached briefly and forgotten
	// on a removal this edge proxies. It is the gateway's one live source
	// for room reach (after the token's baked list), human membership and
	// room roles (roomMember). nil refuses every live lookup.
	Membership membership.Directory
	// RoomMembers lists a room's human members (content shares with a
	// room); ctx carries the room's tenant. nil refuses room shares.
	RoomMembers func(ctx context.Context, roomID string) ([]string, error)
	// OrgAdmins lists an organization's org admins (content publish
	// requests notify them). nil notifies nobody.
	OrgAdmins func(tenantID string) []string
	// RoomTenant resolves a room's authoritative tenant (rooms.tenant_id) so a
	// room-scoped request executes in the ROOM's tenant, not the caller's
	// session tenant. Empty result leaves the session tenant in force. nil
	// disables room-tenant derivation (single-tenant / test).
	RoomTenant func(roomID string) string
	SessionTTL time.Duration
	// AuthACSPath is the public path the IdP posts SAML assertions to (the
	// ACS URL registered with the IdP); defaultPublicACSPath when empty. Must
	// match the auth-provider's own ACS URL, which validates the recipient.
	AuthACSPath string
	// RefreshTTL bounds a refresh token (defaultRefreshTTL when zero). Each
	// refresh rotates it, so an active session/bridge never reaches it.
	RefreshTTL time.Duration
	// TenantResolver optionally maps a verified identity to its effective tenant
	// and org role at login (SSO org mapping + RBAC). It receives the identity
	// and the default-resolved tenant, and returns the tenant + role to stamp
	// into the session token. nil keeps the resolved/default tenant with no
	// role. It must never block login on error (fail soft to the input tenant).
	TenantResolver func(id auth.Identity, defaultTenant string) (tenantID, role string, err error)
	// TenantSuspended optionally reports whether a tenant is suspended. When it
	// returns true, login is refused for that tenant's members; a super_admin is
	// exempt (they must still administer suspended orgs). nil disables the gate.
	TenantSuspended func(tenantID string) bool
	// TenantMemberRole returns a subject's org role in a specific tenant, and
	// found=false when the subject is not a member. It backs the tenant-switch
	// endpoint: a member may re-scope their session to any tenant they belong
	// to, receiving a token carrying that tenant's role. nil disables switching.
	TenantMemberRole func(tenantID, userID string) (role string, found bool)
	// Cores are the local-agent cores (manifest cores, installed adapters, ...) this gateway
	// aggregates for the outbound control path, in configured order (the first
	// is the default). Populated by the composition root from coreset.FromEnv.
	Cores []coreset.Entry
	// coresMu guards Cores against concurrent runtime install/uninstall while
	// handlers read it.
	coresMu sync.RWMutex
	Now     func() time.Time
	// WebUI, when set, serves the collaboration web client for all non-API,
	// non-health paths from the same origin as the API.
	WebUI http.Handler
	// WebBuild is the identity of the embedded web bundle (webui.Build),
	// reported by GET /v1/platform/versions; nil = unknown.
	WebBuild *wire.WebBundleVersion
	// PlatformSettings stores instance-wide configuration owned by super-admins.
	PlatformSettings store.PlatformSettingsStore
	ExtAudit         store.ExtensionAuditStore
	ExtPipe          *extpipe.Pipeline
	AllowExtUnsigned bool
	// SecretBox seals organization MCP connectors (BNBLOC_SECRET_STORE_KEY);
	// nil turns org connectors off (mcp_tools.go).
	SecretBox *sealbox.Box
	// ComponentInstalls persists per-member opt-in installs of distributed
	// components (rollout=optin extension records). nil (in-memory dev/tests)
	// leaves optin components hidden for everyone; auto ones still distribute.
	ComponentInstalls store.ComponentInstallStore
	// ContentComponents holds content components (experts, skill packs:
	// content_components.go) and ContentJobs the job read side a bridge's
	// content resolution is checked against. nil answers the content
	// routes 503.
	ContentComponents store.ContentComponentStore
	ContentJobs       ContentJobs
	// NotifierReplies resolves the reply tokens of delivered notifications
	// for POST /v1/notifier/replies (N1); uninstalling a notifier deletes
	// its tokens. nil answers the route 503.
	NotifierReplies store.NotificationReplyTokenStore
	// MemberCount returns the instance's total distinct members (adoption
	// denominators). nil or 0 renders counts without percentages.
	MemberCount func() int
	// AllowedOrigins are extra browser origins permitted for cross-origin API
	// access (CORS). The desktop shell's webview origins (tauri://localhost and
	// http://tauri.localhost on Windows) are always allowed; a deployment can
	// add more (e.g. a hosted web client on another origin). Same-origin traffic
	// (the gateway-embedded UI) never sends an Origin the browser enforces, so
	// this only affects genuine cross-origin clients like the packaged app.
	AllowedOrigins []string
	// limiter throttles cost-bearing writes per authenticated subject;
	// guessLimiter throttles brute-forceable attempts (login per client IP,
	// invite redemption per subject, public share token misses per client
	// IP); publicLimiter throttles public share reads per client IP.
	limiter       lazyLimiter
	guessLimiter  lazyLimiter
	publicLimiter lazyLimiter
	// Peers is the live gateway replica set (BNBLOC_GATEWAY_PEERS): every
	// per-replica limit above enforces 1/N of its configured budget. Nil =
	// one replica.
	Peers *shard.Set
	// TrustedProxies are the reverse proxies (CIDRs, BNBLOC_TRUSTED_PROXIES)
	// whose X-Forwarded-For the gateway believes when deriving a client IP for
	// the unauthenticated login throttle. Empty trusts no proxy: the TCP peer
	// is the client.
	TrustedProxies []netip.Prefix
	// SSOHandoffs holds pending desktop/CLI SSO hand-offs and RefreshTokens
	// the refresh-token families, both in the shared store so every gateway
	// replica sees them. nil (tests, a dev gateway without a store) falls
	// back to process-local memory stores (see refreshTokens).
	SSOHandoffs   store.SSOHandoffStore
	RefreshTokens store.RefreshTokenStore
	authStateOnce sync.Once
	// APITokens holds super-admin-issued API tokens (api_tokens.go); nil
	// falls back to a process-local memory store like RefreshTokens.
	APITokens     store.APITokenStore
	apiTokensOnce sync.Once
	// blobClient reads realtime's blob store (artifacts.go blobs()).
	blobClientOnce sync.Once
	blobClient     *realtime.HandoffClient
	// bundleCaches holds the verified previewer bundles (previewers.go) and
	// site snapshots (sites.go) by archive sha256, plus the short-lived
	// per-subject site authorizations (bundleCaches()).
	bundleCachesOnce sync.Once
	previewerBundles *tarbundle.Cache
	siteBundles      *tarbundle.Cache
	siteAuth         *boundedCache[siteVersion]
	publicSites      *boundedCache[wire.ShareResolution]
	// SuperAdmins is the configured platform super-admin list
	// (BNBLOC_SUPER_ADMINS): the default recipients of an admin alert.
	SuperAdmins []string
	// Alerts queues admin alerts into the shared notification outbox
	// (realtime delivers them). nil answers POST /v1/platform/alerts 503.
	Alerts *notify.Emitter
	// OrgSync applies the organization-sync rules (R11) at SSO sign-in and
	// backs /v1/platform/org-sync/*. nil (no tenant store) skips sync at
	// sign-in and answers the routes 501.
	OrgSync *orgsync.Syncer
}

// Handler builds the gateway HTTP mux.
func (s *Service) Handler() http.Handler {
	if s.Now == nil {
		s.Now = time.Now
	}
	if s.SessionTTL == 0 {
		s.SessionTTL = time.Hour
	}
	s.corePipeline()
	mux := http.NewServeMux()
	// Every route the gateway serves itself, with its edge policy
	// (routes.go).
	for _, rt := range s.routes() {
		mux.HandleFunc(rt.pattern, s.serve(rt))
	}
	// API traffic is authenticated and proxied to the owning backend.
	mux.HandleFunc("/v1/", s.handleProxy)
	// Everything else serves the collaboration web client when configured; the
	// gateway-embedded UI is same-origin, but the desktop shell and any hosted
	// web client are cross-origin and need CORS (handled by the wrapper below).
	mux.HandleFunc("/", s.handleRoot)
	return s.withCORS(mux)
}

// coreByName returns the configured core with the given name, or nil when no
// core matches (e.g. a stale core-qualified target id from an old client).
func (s *Service) coreByName(name string) *coreset.Entry {
	s.coresMu.RLock()
	defer s.coresMu.RUnlock()
	for i := range s.Cores {
		if s.Cores[i].Name == name {
			e := s.Cores[i]
			return &e
		}
	}
	return nil
}

// defaultCore returns the first configured core (the default), or nil when no
// core is configured.
func (s *Service) defaultCore() *coreset.Entry {
	s.coresMu.RLock()
	defer s.coresMu.RUnlock()
	if len(s.Cores) == 0 {
		return nil
	}
	e := s.Cores[0]
	return &e
}

// handleCores lists every registered core with its availability and unified
// descriptor (backend/source/removable) so clients present a validated picker
// and management UI. It is a read of the gateway's own state (no backend, no
// room scope) but requires a valid session like other reads. "available" means
// the gateway has a configured core entry whose endpoint resolved.
func (s *Service) handleCores(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	view, err := s.loadMemberComponents(r.Context(), claims)
	if err != nil {
		obs.ServerError(w, "load components failed", err)
		httpx.Error(w, "load components failed", http.StatusInternalServerError)
		return
	}
	// A core is available when its endpoint resolves. Re-discover first so a
	// daemon started after gateway boot flips to available without a restart
	// (matches the targets handler). Discovery runs outside coresMu.
	s.coresMu.RLock()
	snapshot := append([]coreset.Entry(nil), s.Cores...)
	s.coresMu.RUnlock()
	avail := make(map[string]bool, len(snapshot))
	for _, entry := range s.rediscoverCores(r.Context(), snapshot) {
		if entry.Endpoint.OK() {
			avail[entry.Name] = true
		}
	}
	type coreInfo struct {
		Name            string `json:"name"`
		Available       bool   `json:"available"`
		Builtin         bool   `json:"builtin"`
		Backend         string `json:"backend"`
		Removable       bool   `json:"removable"`
		Runtime         string `json:"runtime,omitempty"`
		RequiresDesktop bool   `json:"requiresDesktop,omitempty"`
		Rollout         string `json:"rollout,omitempty"`
	}
	names := core.Names() // already sorted
	out := make([]coreInfo, 0, len(names))
	for _, n := range names {
		if !view.coreVisible(s, n) {
			continue
		}
		info := coreInfo{Name: n, Available: avail[n]}
		if d, ok := core.Describe(n); ok {
			info.Builtin = d.Source == core.SourceBuiltin
			info.Backend = string(d.Backend)
			info.Removable = d.Removable
		}
		if rec, ok := view.record(core.KindCore, n); ok {
			component, err := componentFromExtRecord(rec)
			if err != nil {
				obs.ServerError(w, "load components failed", err)
				httpx.Error(w, "load components failed", http.StatusInternalServerError)
				return
			}
			info.Runtime = string(component.Runtime)
			info.RequiresDesktop = component.RequiresDesktop
			info.Rollout = component.Rollout
		}
		out = append(out, info)
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"cores": out})
}

// allowedOrigin reports whether an Origin header may access the API. The
// desktop shell's webview origins are always allowed; deployments may add more.
func (s *Service) allowedOrigin(origin string) bool {
	if origin == "" {
		return false
	}
	// Tauri webview origins: tauri://localhost (macOS/Linux) and
	// http://tauri.localhost (Windows WebView2).
	switch origin {
	case "tauri://localhost", "http://tauri.localhost":
		return true
	}
	for _, o := range s.AllowedOrigins {
		if o == origin {
			return true
		}
	}
	return false
}

// withCORS adds cross-origin headers for allowed origins and answers preflight
// OPTIONS before authentication (a preflight carries no credentials, so routing
// it to the authenticated proxy would wrongly 401). Auth still uses a Bearer
// token, not cookies, so credentials mode is not enabled.
func (s *Service) withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		origin := r.Header.Get("Origin")
		if s.allowedOrigin(origin) {
			h := w.Header()
			h.Set("Access-Control-Allow-Origin", origin)
			h.Add("Vary", "Origin")
			h.Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
			// X-Bnbloc-Acting-Tenant is the super-admin global tenant switch the
			// client may send on any request; it must be allowed here or the
			// browser/webview preflight blocks every request that carries it.
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, "+actingTenantHeader)
			h.Set("Access-Control-Max-Age", "600")
		}
		if r.Method == http.MethodOptions {
			// Preflight: succeed for allowed origins, else 403. Never fall
			// through to auth/proxy.
			if s.allowedOrigin(origin) {
				w.WriteHeader(http.StatusNoContent)
			} else {
				w.WriteHeader(http.StatusForbidden)
			}
			return
		}
		next.ServeHTTP(w, r)
	})
}

// handleRoot serves the web client (SPA) when a WebUI handler is configured,
// falling back to a 404 otherwise. API paths never reach here.
func (s *Service) handleRoot(w http.ResponseWriter, r *http.Request) {
	if s.WebUI == nil {
		http.NotFound(w, r)
		return
	}
	s.WebUI.ServeHTTP(w, r)
}

type loginBody struct {
	IDPToken string `json:"idpToken"`
	// Client optionally labels the new refresh family (web|desktop|cli|bridge;
	// default web) in the member's session list.
	Client string `json:"client,omitempty"`
}

// handleLogin exchanges a verified IdP token for a signed bnbloc session token
// carrying the subject and current room memberships.
func (s *Service) handleLogin(w http.ResponseWriter, r *http.Request) {
	var body loginBody
	if !httpx.DecodeJSON(w, r, maxAuthBodyBytes, &body) {
		return
	}
	identity, err := s.Verifier.Verify(body.IDPToken)
	if err != nil {
		httpx.Error(w, "invalid idp token", http.StatusUnauthorized)
		return
	}
	grant, err := s.issueSession(r.Context(), identity, originFor(r, body.Client, clientWeb))
	if err != nil {
		writeSessionErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, grant)
}

type switchTenantBody struct {
	TenantID string `json:"tenantId"`
}

// handleSwitchTenant re-issues the caller's session scoped to a different
// tenant they belong to. Unlike the super-admin acting-tenant header (which
// overlays one privileged identity across tenants), this is the path for an
// ORDINARY member of multiple orgs: the new token carries that tenant's role
// and its room memberships, so the whole session (rooms, agents, RBAC)
// follows. A super-admin may switch to any tenant; everyone else only to one
// they are a member of (switchScope). The choice persists on the session's
// refresh family (its tenant hint), so a refresh, and a bridge or CLI
// session forked from it, land in the switched tenant too.
func (s *Service) handleSwitchTenant(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	var body switchTenantBody
	if !httpx.DecodeJSON(w, r, maxAuthBodyBytes, &body) {
		return
	}
	target := strings.TrimSpace(body.TenantID)
	if target == "" {
		httpx.Error(w, "tenantId required", http.StatusBadRequest)
		return
	}
	scope, err := s.switchScope(claims, target)
	if err != nil {
		writeSessionErr(w, err)
		return
	}
	now := s.nowTime()
	if claims.Family != "" {
		_, err := s.refreshTokens().SwitchRefreshFamily(r.Context(), store.RefreshSwitch{
			FamilyID: claims.Family, Subject: claims.Subject, TenantHint: target,
			TenantID: scope.Tenant, Role: scope.Role, NowMs: now.UnixMilli(),
		})
		switch {
		case errors.Is(err, store.ErrRefreshInvalid):
			httpx.Error(w, "the sign-in has ended", http.StatusUnauthorized)
			return
		case err != nil:
			obs.ServerError(w, "persist tenant switch failed", err)
			httpx.Error(w, "persist tenant switch failed", http.StatusInternalServerError)
			return
		}
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"sessionToken": mintSession(s.Signer, claims.Subject, claims.Family, scope, now.Add(s.SessionTTL)),
		"subject":      claims.Subject, "tenant": scope.Tenant, "role": scope.Role, "rooms": scope.Rooms,
	})

}

// handleProxy verifies the caller, enforces room scope, org RBAC, and rate
// limits, injects the authenticated subject as X-Bnbloc-User, and
// reverse-proxies to the backend that owns the path. Every decision reads the
// same parsed apiPath; an account-scope mismatch fails closed.
func (s *Service) handleProxy(w http.ResponseWriter, r *http.Request) {
	p, err := parseAPIPath(r)
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	claims, ok := s.proxyClaims(r, p)
	if !ok {
		httpx.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// Enforce room scope: a token may only reach rooms it is a member of.
	// Exempt POST /v1/rooms/{id}/join: joining a shared room is exactly how a
	// non-member gains membership, so it cannot require prior membership. The
	// room service still refuses to join a private room without an invite.
	// The room itself (/v1/rooms/{id}) answers a non-member too (a shared
	// room's preview, 404 for a private one), so only sub-resources need the
	// caller's reach.
	if p.roomScoped() {
		isJoin := r.Method == http.MethodPost && p.RoomSub == "join" && len(p.segs) == 4
		// An org admin revoking a public share link need not be a room
		// member (the room service re-checks the org role).
		isAdminRevoke := p.isArtifactShareRevoke(r.Method) && rbac.Can(claims.Role, rbac.PermManageOrg)
		// The baked token claim reflects membership at login. A room created or
		// joined after login is not in it, so on a miss fall back to a live
		// membership lookup instead of forcing re-login (the stale-scope desync).
		// The fallback only fires on the miss path, so the common case stays free.
		if !isJoin && !isAdminRevoke && !s.roomReachable(claims, p.RoomID) {
			httpx.Error(w, "room scope not authorized", http.StatusForbidden)
			return
		}
	}
	if !s.edgeGate(w, r, p, claims) {
		return
	}
	// The effective tenant (tenant_scope.go): proxyClaims already applied a
	// super admin's acting tenant; a path that names a room acts in the
	// room's own tenant (inRoomTenant, the reach checked above where the
	// route needs it), and a search narrowed to one room in that room's
	// (roomClaims). Strip the client-supplied acting-tenant header so it
	// never reaches a backend as an unsigned scope hint; proxyAs sets the
	// authoritative, signed X-Bnbloc-Tenant from the claims.
	if p.namesRoom() {
		claims = s.inRoomTenant(claims, p.RoomID)
	} else if room := p.searchRoom(r); room != "" {
		var ok bool
		if claims, ok = s.roomClaims(claims, room); !ok {
			httpx.Error(w, "room scope not authorized", http.StatusForbidden)
			return
		}
	}
	r.Header.Del(actingTenantHeader)
	if p.isRoomDispatch(r.Method) {
		// The only route the edge maps onto an internal one (isRoomDispatch).
		r.URL.Path = "/internal/v1/rooms/" + p.RoomID + "/dispatch"
		r.URL.RawPath = "/internal/v1/rooms/" + url.PathEscape(p.RoomID) + "/dispatch"
	}
	s.proxyAs(w, r, p, claims)
	// A membership this request may have ended (a removal, a leave, the
	// room's deletion) leaves no cached positive answer at this edge.
	if user, ok := p.endsMembership(r.Method, claims.Subject); ok && s.Membership != nil {
		s.Membership.Forget(p.RoomID, user)
	}
}

// edgeGate applies the coarse org RBAC and rate-limit policy of a proxied
// request (requiredPermission, rateLimited) through the route table's own
// checks (permitted, spend). It answers 403/429 itself.
func (s *Service) edgeGate(w http.ResponseWriter, r *http.Request, p apiPath, claims auth.Claims) bool {
	// A viewer is read-only, so a cost-bearing or org-admin write is refused
	// at the edge before it reaches a backend. Room-role checks
	// (owner/admin/member) remain the room/shareagent services' job.
	if !s.permitted(w, claims, requiredPermission(r.Method, p)) {
		return false
	}
	// Invite redemption is authenticated, so the code-guessing throttle is keyed
	// on the subject: one account cannot brute-force invite codes, and users
	// behind a shared proxy/NAT never share a bucket.
	if isInviteRedeem(r.Method, p) && !s.guessAllow("subject:"+claims.Subject) {
		httpx.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return false
	}
	// Throttle cost-bearing writes (chat, borrow, room creation) per subject
	// so a client cannot spam messages or drive unbounded agent turns.
	// Reads/SSE are never limited.
	budget := noBudget
	if rateLimited(r.Method, p) {
		budget = costBudget
	}
	return s.spend(w, claims, budget)
}

// proxyClaims authenticates a proxied request: a valid session token, acting
// in its effective tenant (actingTenant).
func (s *Service) proxyClaims(r *http.Request, p apiPath) (auth.Claims, bool) {
	token := headerBearer(r)
	if token == "" && p.isAttachmentDownload(r.Method) {
		// An <img>/<a download> cannot set headers: an attachment read
		// carries a ticket for that one attachment (attachment_tickets.go).
		return s.attachmentTicketClaims(r, p)
	}
	if token == "" && p.isEventStream(r.Method) {
		// A browser's EventSource cannot set headers either, so ONLY the
		// room event stream accepts the session token as an access_token
		// query parameter. Everywhere else a URL-borne session (which leaks
		// into logs, history, and Referer) is not a credential.
		token = strings.TrimSpace(r.URL.Query().Get("access_token"))
	}
	c, err := s.Signer.Verify(token)
	if err != nil {
		return auth.Claims{}, false
	}
	c.Tenant = actingTenant(r, c)
	return c, true
}

// rateAllow lazily builds the per-subject limiter and consumes one token.
func (s *Service) rateAllow(subject string) bool {
	return s.limiter.get(defaultChatRate, defaultChatBurst, s.Peers.Size).allow(subject)
}

// guessAllow lazily builds the slow guess limiter and consumes one token for
// key. It bounds brute-forceable attempts: "ip:<addr>" for unauthenticated
// login and the API-token exchange (no trusted subject yet; one shared
// bucket), "subject:<id>" for authenticated invite
// redemption, "share-ip:<addr>" for public share token misses.
func (s *Service) guessAllow(key string) bool {
	return s.guessLimiter.get(defaultGuessRate, defaultGuessBurst, s.Peers.Size).allow(key)
}

// guessBlocked reports, without consuming, whether key's guess budget is
// spent.
func (s *Service) guessBlocked(key string) bool {
	return s.guessLimiter.get(defaultGuessRate, defaultGuessBurst, s.Peers.Size).blocked(key)
}

// publicAllow consumes one public-read token for a client IP.
func (s *Service) publicAllow(ip string) bool {
	return s.publicLimiter.get(defaultPublicRate, defaultPublicBurst, s.Peers.Size).allow("ip:" + ip)
}

// clientIP derives the caller's IP for the per-IP limits. X-Forwarded-For is
// client-writable, so it is believed only as far as a chain of TRUSTED proxies
// vouches for it: when the TCP peer is a trusted proxy, the entries are walked
// right to left (each appended by the proxy in front of it) and the first
// address that is not itself a trusted proxy is the client. With no trusted
// proxies configured the TCP peer is the client. Trusting the left-most entry
// (the previous behavior) let any caller pick their own rate-limit bucket.
func (s *Service) clientIP(r *http.Request) string {
	peer := r.RemoteAddr
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		peer = host
	}
	if !s.trustedProxy(peer) {
		return peer
	}
	var hops []string
	for _, h := range r.Header.Values("X-Forwarded-For") {
		hops = append(hops, strings.Split(h, ",")...)
	}
	client := peer
	for i := len(hops) - 1; i >= 0; i-- {
		hop := strings.TrimSpace(hops[i])
		if _, err := netip.ParseAddr(hop); err != nil {
			// A malformed entry ends the trustworthy part of the chain.
			return client
		}
		client = hop
		if !s.trustedProxy(hop) {
			return hop
		}
	}
	return client
}

// trustedProxy reports whether ip lies in a configured trusted-proxy prefix.
func (s *Service) trustedProxy(ip string) bool {
	addr, err := netip.ParseAddr(ip)
	if err != nil {
		return false
	}
	addr = addr.Unmap()
	for _, p := range s.TrustedProxies {
		if p.Contains(addr) {
			return true
		}
	}
	return false
}

// proxyAs reverse-proxies to the owning backend, asserting the session's
// subject, effective tenant and org role. Scope enforcement happens in the
// caller.
func (s *Service) proxyAs(w http.ResponseWriter, r *http.Request, p apiPath, claims auth.Claims) {
	target := s.route(p)
	if target == "" {
		httpx.Error(w, "no route", http.StatusNotFound)
		return
	}
	upstream, err := url.Parse(target)
	if err != nil {
		obs.ServerError(w, "bad upstream", err)
		httpx.Error(w, "bad upstream", http.StatusInternalServerError)
		return
	}
	// The ownership-forward marker is replica-to-replica only: a client that
	// set it would pin its request to whichever replica it landed on.
	r.Header.Del(shard.ForwardedHeader)
	// Assert the session's identity (subject, effective tenant, org role)
	// in place of anything the client sent, signed so the backend can verify
	// it originated here and was not spoofed by another workload on the
	// internal network (unsigned when BNBLOC_INTERNAL_KEY is unset — dev).
	svcauth.Key(svcauth.KeyFromEnv()).Assert(r, claims.Subject, claims.Tenant, claims.Role)
	proxy := httputil.NewSingleHostReverseProxy(upstream)
	// Flush immediately so Server-Sent Events (room /events) stream to the
	// client as they arrive instead of being buffered by the reverse proxy;
	// -1 flushes after each write. Non-stream responses are unaffected.
	proxy.FlushInterval = -1
	proxy.ErrorHandler = obs.ProxyError("backend proxy failed")
	proxy.ServeHTTP(w, r)
}

// headerBearer returns the Authorization bearer token ("" when absent).
func headerBearer(r *http.Request) string {
	h := strings.TrimSpace(r.Header.Get("Authorization"))
	if !strings.HasPrefix(h, "Bearer ") {
		return ""
	}
	return strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
}

// authenticate verifies the request's session token from the Authorization
// header and resolves the tenant the request acts in (actingTenant,
// tenant_scope.go): every handler the gateway serves itself reads
// claims.Tenant as the effective tenant, never the bare session claim.
// Query-string tokens are accepted only by the proxy for the SSE and
// attachment-download endpoints (see proxyClaims).
func (s *Service) authenticate(r *http.Request) (auth.Claims, error) {
	c, err := s.Signer.Verify(headerBearer(r))
	if err != nil {
		return auth.Claims{}, err
	}
	c.Tenant = actingTenant(r, c)
	return c, nil
}

// NewRoomMembershipResolver returns a Rooms resolver that queries bnbloc-room for
// a subject's room memberships at login. It fails soft (empty list) so login
// still succeeds; room-scoped requests then fall back to the live lookup.
func NewRoomMembershipResolver(roomBaseURL string) func(string, string) []string {
	// The resolver must carry the caller's tenant: rooms are tenant-scoped, so
	// a membership lookup without it would search the default tenant and fail
	// closed for any user whose workspace lives in another org.
	return func(subject, tenantID string) []string {
		if roomBaseURL == "" {
			return nil
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resp, err := callBackend(ctx, http.MethodGet, roomBaseURL+"/v1/users/"+url.PathEscape(subject)+"/rooms", nil, subject, tenantID)
		if err != nil {
			return nil
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil
		}
		var out struct {
			Rooms []string `json:"rooms"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return nil
		}
		return out.Rooms
	}
}

// NewRoomTenantResolver returns a RoomTenant resolver that asks bnbloc-room for
// a room's authoritative tenant (tenant-agnostic, keyed on room id). A short
// positive cache keeps the per-request lookup cheap; a room's tenant is
// immutable, so caching is safe. Returns "" on any error: the request then
// keeps the caller's tenant, which a backend refuses for the room
// (membership.RoomScope) — never a wrong tenant. Signs as the gateway's own
// service subject: an internal, non-user-specific lookup.
func NewRoomTenantResolver(roomBaseURL string) func(string) string {
	// Bounded + expiring: a room id can be freed by deletion and re-created in
	// another tenant, and the set of rooms ever seen must not grow forever.
	cache := newBoundedCache[string](roomTenantCacheTTL, roomCacheMaxEntries)
	return func(roomID string) string {
		if roomBaseURL == "" || roomID == "" {
			return ""
		}
		if t, ok := cache.get(roomID); ok {
			return t
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		resp, err := callBackend(ctx, http.MethodGet, roomBaseURL+"/v1/rooms/"+url.PathEscape(roomID)+"/tenant", nil, gatewayServiceSubject, "")
		if err != nil {
			return ""
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return ""
		}
		var out struct {
			TenantID string `json:"tenantId"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.TenantID == "" {
			return ""
		}
		cache.put(roomID, out.TenantID)
		return out.TenantID
	}
}
