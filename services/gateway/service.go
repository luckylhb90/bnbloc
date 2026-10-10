// Package gateway implements the bnbloc-gateway: the single public entry that
// exchanges an enterprise IdP token for a bnbloc session token, verifies session
// tokens, and reverse-proxies authorized requests to the owning backend service.
package gateway

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"log"
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
	"github.com/luckylhb90/bnbloc/internal/notify"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/orgsync"
	"github.com/luckylhb90/bnbloc/internal/rbac"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/shard"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
	"github.com/luckylhb90/bnbloc/internal/tenant"
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
	// LocalAgentToken is the shared secret a trusted local daemon (the bridge)
	// presents to bypass IdP auth and forward as the daemon service subject.
	LocalAgentToken string
}

// Service holds the gateway dependencies.
type Service struct {
	Verifier auth.IDPVerifier
	Signer   *auth.Signer
	Backends Backends
	// Rooms resolves a subject's room memberships at login; production reads
	// bnbloc-room, tests inject a fake.
	Rooms func(subject, tenantID string) []string
	// InRoom answers "is subject a member of roomID in ANY tenant", used by the
	// room-scope fallback for a multi-tenant member whose room lives outside
	// their token's tenant. nil disables the cross-tenant fallback (the baked
	// claim + same-tenant Rooms lookup still apply).
	InRoom func(subject, roomID string) bool
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
	TenantResolver func(id auth.Identity, defaultTenant string) (tenantID, role string)
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
	// ComponentInstalls persists per-member opt-in installs of distributed
	// components (rollout=optin extension records). nil (in-memory dev/tests)
	// leaves optin components hidden for everyone; auto ones still distribute.
	ComponentInstalls store.ComponentInstallStore
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
	// daemonBypassOnce guards the one-time evaluation + warning of whether the
	// trusted-daemon token bypass is allowed (fails closed for the dev default).
	daemonBypassOnce sync.Once
	daemonBypassOK   bool
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
	// memberCache remembers recent positive live room-membership lookups so
	// the post-login room-scope fallback does not hit bnbloc-room per request.
	memberCacheOnce sync.Once
	memberCache     *boundedCache[bool]
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
	mux.HandleFunc("POST /v1/auth/login", s.handleLogin)
	mux.HandleFunc("POST /v1/auth/refresh", s.handleRefresh)
	mux.HandleFunc("POST /v1/auth/refresh/fork", s.handleRefreshFork)
	mux.HandleFunc("POST /v1/auth/logout", s.handleLogout)
	mux.HandleFunc("GET /v1/my/sessions", s.handleMySessions)
	mux.HandleFunc("DELETE /v1/my/sessions/{id}", s.handleRevokeMySession)
	mux.HandleFunc("DELETE /v1/my/sessions", s.handleRevokeOtherSessions)
	mux.HandleFunc("POST /v1/auth/switch-tenant", s.handleSwitchTenant)
	mux.HandleFunc("POST /v1/local-agent/run", s.handleLocalAgentRun)
	mux.HandleFunc("GET /v1/local-agent/targets", s.handleLocalAgentTargets)
	mux.HandleFunc("GET /v1/local-agent/discover", s.handleLocalAgentDiscover)
	mux.HandleFunc("GET /v1/cores", s.handleCores)
	mux.HandleFunc("GET /v1/cores/{name}/artifact", s.handleCoreArtifact)
	// Member component surface: the rollout-filtered catalog + opt-in installs.
	mux.HandleFunc("GET /v1/components", s.handleComponents)
	mux.HandleFunc("POST /v1/components/{name}/install", s.handleComponentInstall)
	mux.HandleFunc("DELETE /v1/components/{name}/install", s.handleComponentUninstall)
	mux.HandleFunc("GET /v1/app/update/{target}/{currentVersion}", s.handleAppUpdate)
	mux.HandleFunc("GET /v1/app/update-policy", s.handleUpdatePolicy)
	mux.HandleFunc("GET /v1/platform/update-config", s.handlePlatformUpdateConfig)
	mux.HandleFunc("PUT /v1/platform/update-config", s.handlePlatformUpdateConfigPut)
	mux.HandleFunc("GET /v1/platform/components", s.handlePlatformComponents)
	mux.HandleFunc("POST /v1/platform/components", s.handlePlatformComponents)
	mux.HandleFunc("DELETE /v1/platform/components/{name}", s.handleUninstallComponent)
	mux.HandleFunc("PUT /v1/platform/components/{name}/tenants", s.handleSetComponentTenants)
	mux.HandleFunc("PUT /v1/platform/components/{name}/rollout", s.handleSetComponentRollout)
	mux.HandleFunc("POST /v1/platform/components/{name}/secret/rotate", s.handleRotateComponentSecret)
	mux.HandleFunc("POST /v1/platform/components/{name}/test", s.handleTestComponent)
	// A notifier's signed reply on a member's behalf (no session: the
	// component's signing secret authenticates it).
	mux.HandleFunc("POST /v1/notifier/replies", s.handleNotifierReply)
	mux.HandleFunc("POST /v1/notifier/reply-refs", s.handleNotifierReplyRef)
	mux.HandleFunc("GET /v1/platform/component-keys", s.handleComponentTrustedKeys)
	mux.HandleFunc("POST /v1/platform/component-keys", s.handleComponentTrustedKeys)
	mux.HandleFunc("DELETE /v1/platform/component-keys/{keyId}", s.handleDeleteComponentTrustedKey)
	mux.HandleFunc("GET /v1/platform/artifacts", s.handleArtifactCapability)
	mux.HandleFunc("POST /v1/platform/artifacts", s.handleArtifactUpload)
	mux.HandleFunc("GET /v1/artifacts/{key...}", s.handleArtifactDownload)
	mux.HandleFunc("GET /v1/previewers/{name}/{version}/{ticket}/{path...}", s.handlePreviewerFile)
	// Site snapshots (sites.go) and public share links (public_shares.go):
	// tickets and share tokens travel in the path, never a session.
	mux.HandleFunc("POST /v1/rooms/{roomId}/artifacts/{artifactId}/versions/{version}/site-ticket", s.handleSiteTicket)
	mux.HandleFunc("GET /v1/sites/{ticket}", s.handleSiteRootRedirect)
	mux.HandleFunc("GET /v1/sites/{ticket}/{path...}", s.handleMemberSite)
	mux.HandleFunc("GET /v1/public/shares/{token}", s.handlePublicShare)
	mux.HandleFunc("GET /v1/public/shares/{token}/content", s.handlePublicShareContent)
	mux.HandleFunc("GET /v1/public/shares/{token}/site", s.handleSiteRootRedirect)
	mux.HandleFunc("GET /v1/public/shares/{token}/site/{path...}", s.handlePublicShareSite)
	mux.HandleFunc("GET /v1/app/releases", s.handleAppReleases)
	mux.HandleFunc("GET /v1/platform/app-releases", s.handlePlatformReleases)
	mux.HandleFunc("PUT /v1/platform/app-releases/meta", s.handlePlatformReleaseMetaPut)
	mux.HandleFunc("PUT /v1/platform/app-releases/{version}", s.handlePlatformReleasePut)
	mux.HandleFunc("DELETE /v1/platform/app-releases/{version}", s.handlePlatformReleaseDelete)
	mux.HandleFunc("GET /v1/platform/versions", s.handlePlatformVersions)
	s.registerAPITokens(mux)
	s.registerSSO(mux)
	s.registerOrgSync(mux)
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
func (s *Service) handleCores(w http.ResponseWriter, r *http.Request) {
	claims, err := s.authenticate(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	view, err := s.loadMemberComponents(r.Context(), claims)
	if err != nil {
		obs.ServerError(w, "load components failed", err)
		http.Error(w, "load components failed", http.StatusInternalServerError)
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
				http.Error(w, "load components failed", http.StatusInternalServerError)
				return
			}
			info.Runtime = string(component.Runtime)
			info.RequiresDesktop = component.RequiresDesktop
			info.Rollout = component.Rollout
		}
		out = append(out, info)
	}
	writeJSON(w, http.StatusOK, map[string]any{"cores": out})
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
			h.Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Bnbloc-Acting-Tenant")
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
	// Login is unauthenticated: there is no trusted subject yet (keying on the
	// claimed identity would let anyone lock a victim out), so the guess
	// throttle is keyed on the client IP.
	if !s.guessAllow("ip:" + s.clientIP(r)) {
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}
	var body loginBody
	r.Body = http.MaxBytesReader(w, r.Body, maxAuthBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	identity, err := s.Verifier.Verify(body.IDPToken)
	if err != nil {
		http.Error(w, "invalid idp token", http.StatusUnauthorized)
		return
	}
	grant, err := s.issueSession(r.Context(), identity, originFor(r, body.Client, clientWeb))
	if errors.Is(err, errTenantSuspended) {
		http.Error(w, "tenant suspended", http.StatusForbidden)
		return
	}
	if err != nil {
		obs.ServerError(w, "session issue failed", err)
		http.Error(w, "session issue failed", http.StatusInternalServerError)
		return
	}
	writeJSON(w, http.StatusOK, grant)
}

type switchTenantBody struct {
	TenantID string `json:"tenantId"`
}

// handleSwitchTenant re-issues the caller's session token scoped to a different
// tenant they belong to. Unlike the super-admin acting-tenant header (which
// overlays one privileged identity across tenants), this is the path for an
// ORDINARY member of multiple orgs: the new token carries that tenant's role
// and its room memberships, so the whole session (rooms, agents, RBAC) follows.
// A super-admin may switch to any tenant; everyone else only to one they are a
// member of. A suspended tenant is refused (super-admin exempt).
func (s *Service) handleSwitchTenant(w http.ResponseWriter, r *http.Request) {
	claims, err := s.authenticate(r)
	if err != nil {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var body switchTenantBody
	r.Body = http.MaxBytesReader(w, r.Body, maxAuthBodyBytes)
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	target := strings.TrimSpace(body.TenantID)
	if target == "" {
		http.Error(w, "tenantId required", http.StatusBadRequest)
		return
	}
	isSuper := claims.Role == string(rbac.RoleSuperAdmin)
	// Resolve the role the caller will hold in the target tenant. A super-admin
	// keeps super_admin (cross-tenant); anyone else must be a member there.
	role := string(rbac.RoleSuperAdmin)
	if !isSuper {
		if s.TenantMemberRole == nil {
			obs.ServerError(w, "tenant switching not available", nil)
			http.Error(w, "tenant switching not available", http.StatusNotImplemented)
			return
		}
		r, ok := s.TenantMemberRole(target, claims.Subject)
		if !ok {
			http.Error(w, "not a member of that tenant", http.StatusForbidden)
			return
		}
		role = r
	}
	// A suspended tenant is closed to its members; a super-admin may still enter.
	if s.TenantSuspended != nil && !isSuper && s.TenantSuspended(target) {
		http.Error(w, "tenant suspended", http.StatusForbidden)
		return
	}
	// Room memberships are per-subject (rooms are tenant-scoped downstream), so
	// reuse the resolver; the room service filters by the injected tenant.
	var rooms []string
	if s.Rooms != nil {
		rooms = s.Rooms(claims.Subject, target)
	}
	newClaims := auth.Claims{
		Subject:       claims.Subject,
		Rooms:         rooms,
		Tenant:        target,
		Role:          role,
		Family:        claims.Family,
		ExpiresAtUnix: s.Now().Add(s.SessionTTL).Unix(),
	}
	token := s.Signer.Issue(newClaims)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"sessionToken": token, "subject": claims.Subject, "tenant": target, "role": role, "rooms": rooms,
	})
}

// handleProxy verifies the caller, enforces room scope, org RBAC, and rate
// limits, injects the authenticated subject as X-Bnbloc-User, and
// reverse-proxies to the backend that owns the path. Every decision reads the
// same parsed apiPath; an account-scope mismatch fails closed.
func (s *Service) handleProxy(w http.ResponseWriter, r *http.Request) {
	p, err := parseAPIPath(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	claims, ok := s.proxyClaims(r, p)
	if !ok {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	// Enforce room scope: a token may only reach rooms it is a member of.
	// Exempt POST /v1/rooms/{id}/join: joining a shared room is exactly how a
	// non-member gains membership, so it cannot require prior membership. The
	// room service still refuses to join a private room without an invite.
	if p.roomScoped() {
		isJoin := r.Method == http.MethodPost && p.RoomSub == "join" && len(p.segs) == 4
		// An org admin revoking a public share link need not be a room
		// member (the room service re-checks the org role).
		isAdminRevoke := p.isArtifactShareRevoke(r.Method) && rbac.Can(claims.Role, rbac.PermManageOrg)
		// The baked token claim reflects membership at login. A room created or
		// joined after login is not in it, so on a miss fall back to a live
		// membership lookup instead of forcing re-login (the stale-scope desync).
		// The fallback only fires on the miss path, so the common case stays free.
		if !isJoin && !isAdminRevoke && !claims.HasRoom(p.RoomID) && !s.subjectInRoom(claims.Subject, p.RoomID, claims.Tenant) {
			http.Error(w, "room scope not authorized", http.StatusForbidden)
			return
		}
	}
	// Coarse org RBAC: a viewer is read-only, so a cost-bearing or org-admin
	// write is refused at the edge before it reaches a backend. Room-role checks
	// (owner/admin/member) remain the room/shareagent services' job.
	if perm := requiredPermission(r.Method, p); perm != rbac.PermRead {
		if !rbac.Can(claims.Role, perm) {
			http.Error(w, "insufficient org role", http.StatusForbidden)
			return
		}
	}
	// Invite redemption is authenticated, so the code-guessing throttle is keyed
	// on the subject: one account cannot brute-force invite codes, and users
	// behind a shared proxy/NAT never share a bucket.
	if isInviteRedeem(r.Method, p) && !s.guessAllow("subject:"+claims.Subject) {
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}
	// Throttle cost-bearing writes (chat, borrow) per subject so a client cannot
	// spam messages or drive unbounded agent turns. Reads/SSE are never limited.
	if rateLimited(r.Method, p) && !s.rateAllow(claims.Subject) {
		http.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}
	// Cross-tenant scope for platform super-admins. A super_admin may act inside
	// any tenant two ways, both honored ONLY for super_admins (a non-super-admin
	// is always bound to their own tenant; the header/param are ignored):
	//   1. X-Bnbloc-Acting-Tenant header — the GLOBAL switch: the client sets it
	//      so the whole session (rooms, agents, admin) acts as that tenant.
	//   2. ?tenantId= query — a per-call override (the Platform console uses it
	//      to scope one panel), which wins over the header when both are set.
	effectiveTenant := claims.Tenant
	if rbac.Can(claims.Role, rbac.PermManagePlatform) {
		if h := strings.TrimSpace(r.Header.Get(actingTenantHeader)); h != "" {
			effectiveTenant = h
		}
		if t := strings.TrimSpace(r.URL.Query().Get("tenantId")); t != "" {
			effectiveTenant = t
		}
	}
	// Strip the client-supplied acting-tenant header so it never reaches a
	// backend as an unsigned scope hint; proxyAs sets the authoritative,
	// signed X-Bnbloc-Tenant from effectiveTenant.
	r.Header.Del(actingTenantHeader)
	// Room-scoped tenant derivation (the origin fix for cross-tenant access):
	// a room carries its own authoritative tenant (rooms.tenant_id), and the
	// caller was already authorized for the room by membership above. So a
	// room-scoped request executes in the ROOM's tenant, not the session's —
	// letting a multi-tenant member (or the bridge acting as one) transact in a
	// room that lives in their non-primary org. Tenant-level paths keep the
	// session tenant. A super-admin acting-tenant override (set above) wins, so
	// the platform console can still target a specific org.
	if p.roomScoped() && s.RoomTenant != nil {
		if rt := s.RoomTenant(p.RoomID); rt != "" {
			effectiveTenant = rt
		}
	}
	s.proxyAs(w, r, p, claims.Subject, effectiveTenant)
}

// proxyClaims authenticates a proxied request. A trusted local daemon presents
// the shared LocalAgentToken and acts as the fixed daemon subject with ordinary
// MEMBER rights: it goes through the same room scope, org RBAC (so platform and
// tenant-admin paths are refused), and tenant derivation as every session —
// the shared secret is a credential, never a bypass. Otherwise the caller needs
// a valid session token.
func (s *Service) proxyClaims(r *http.Request, p apiPath) (auth.Claims, bool) {
	if c, ok := s.daemonClaims(r); ok {
		return c, true
	}
	token := headerBearer(r)
	if token == "" && (p.isEventStream(r.Method) || p.isAttachmentDownload(r.Method) || p.isArtifactDownload(r)) {
		// Browsers' EventSource and <img>/<a download> cannot set headers, so
		// ONLY these GET endpoints (the room event stream, an attachment, a
		// deliverable download) accept the session token as an access_token
		// query parameter. Everywhere else a URL-borne token (which leaks
		// into logs, history, and Referer) is not a credential.
		token = strings.TrimSpace(r.URL.Query().Get("access_token"))
	}
	c, err := s.Signer.Verify(token)
	return c, err == nil
}

// daemonClaims recognizes the trusted local daemon's bearer (constant-time
// compare) when the bypass is allowed (a strong configured token).
func (s *Service) daemonClaims(r *http.Request) (auth.Claims, bool) {
	want := s.Backends.LocalAgentToken
	got := headerBearer(r)
	if want == "" || got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(want)) != 1 || !s.daemonTokenBypassAllowed() {
		return auth.Claims{}, false
	}
	return auth.Claims{Subject: daemonServiceSubject, Tenant: tenant.Default, Role: string(rbac.RoleMember)}, true
}

// rateAllow lazily builds the per-subject limiter and consumes one token. The
// trusted daemon subject is exempt (it is the bridge, not a spamming client).
func (s *Service) rateAllow(subject string) bool {
	if subject == daemonServiceSubject {
		return true
	}
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

// actingTenantHeader is the client-supplied global tenant switch for platform
// super-admins. It is advisory (honored only for super_admins) and is always
// stripped before proxying so a backend only ever sees the signed tenant.
const actingTenantHeader = "X-Bnbloc-Acting-Tenant"

// daemonTokenBypassAllowed reports whether the shared-secret daemon-token
// bypass may be honored. It fails closed when the token is the well-known dev
// default ("dev-local-agent-token") unless dev discovery is explicitly enabled,
// so a production deployment that forgot to set a strong token cannot be
// impersonated by anyone who knows the default. Evaluated once, with a loud
// warning.
func (s *Service) daemonTokenBypassAllowed() bool {
	s.daemonBypassOnce.Do(func() {
		tok := strings.TrimSpace(s.Backends.LocalAgentToken)
		if tok == "" || tok == devLocalAgentToken {
			if devDiscoveryEnabled() {
				log.Printf("bnbloc-gateway: WARNING daemon-token bypass using dev default token; enabled only because BNBLOC_DEV_DISCOVERY is set (dev mode)")
				s.daemonBypassOK = true
			} else {
				log.Printf("bnbloc-gateway: WARNING BNBLOC_LOCAL_AGENT_TOKEN is unset or the dev default; DISABLING the daemon-token bypass (fail closed). Set a strong BNBLOC_LOCAL_AGENT_TOKEN to enable it")
				s.daemonBypassOK = false
			}
			return
		}
		s.daemonBypassOK = true
	})
	return s.daemonBypassOK
}

// devLocalAgentToken is the well-known development default daemon token. When
// the configured token equals this (or is unset) the bypass is dev-only.
const devLocalAgentToken = "dev-local-agent-token"

// daemonServiceSubject is the fixed identity a trusted local daemon forwards as.
const daemonServiceSubject = "local-agent-daemon"

// proxyAs reverse-proxies to the owning backend, injecting the authoritative
// X-Bnbloc-User and X-Bnbloc-Tenant. Scope enforcement happens in the caller.
func (s *Service) proxyAs(w http.ResponseWriter, r *http.Request, p apiPath, subject, tenantID string) {
	target := s.route(p)
	if target == "" {
		http.Error(w, "no route", http.StatusNotFound)
		return
	}
	upstream, err := url.Parse(target)
	if err != nil {
		obs.ServerError(w, "bad upstream", err)
		http.Error(w, "bad upstream", http.StatusInternalServerError)
		return
	}
	r.Header.Del("X-Bnbloc-User")
	r.Header.Del(svcauth.HeaderTenant)
	r.Header.Del(svcauth.HeaderSig)
	r.Header.Del(svcauth.HeaderTs)
	// The ownership-forward marker is replica-to-replica only: a client that
	// set it would pin its request to whichever replica it landed on.
	r.Header.Del(shard.ForwardedHeader)
	r.Header.Set("X-Bnbloc-User", subject)
	// Every authenticated request carries a tenant; default for legacy tokens
	// and the trusted daemon so backends always have a scope to enforce.
	if tenantID == "" {
		tenantID = tenant.Default
	}
	r.Header.Set(svcauth.HeaderTenant, tenantID)
	// Sign the identity assertion (user + tenant) so the backend can verify it
	// originated from the gateway and was not spoofed by another workload on the
	// internal network (no-op when BNBLOC_INTERNAL_KEY is unset — dev mode).
	svcauth.Sign(r, svcauth.KeyFromEnv(), subject)
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
// header. Query-string tokens are accepted only by the proxy for the SSE and
// attachment-download endpoints (see proxyClaims).
func (s *Service) authenticate(r *http.Request) (auth.Claims, error) {
	return s.Signer.Verify(headerBearer(r))
}

// NewRoomMembershipResolver returns a Rooms resolver that queries bnbloc-room for
// a subject's room memberships at login. It fails soft (empty list) so login
// still succeeds; room-scoped requests then fail closed until membership loads.
func NewRoomMembershipResolver(roomBaseURL string, client *http.Client) func(string, string) []string {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	// The resolver must carry the caller's tenant: rooms are tenant-scoped, so
	// a membership lookup without it would search the default tenant and fail
	// closed for any user whose workspace lives in another org.
	return func(subject, tenantID string) []string {
		if roomBaseURL == "" {
			return nil
		}
		req, err := http.NewRequest(http.MethodGet, roomBaseURL+"/v1/users/"+url.PathEscape(subject)+"/rooms", nil)
		if err != nil {
			return nil
		}
		req.Header.Set("X-Bnbloc-User", subject)
		if tenantID != "" {
			req.Header.Set(svcauth.HeaderTenant, tenantID)
		}
		svcauth.Sign(req, svcauth.KeyFromEnv(), subject)
		resp, err := client.Do(req)
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

// NewRoomMembershipProbe returns an InRoom resolver that asks bnbloc-room
// whether subject is a member of roomID in ANY tenant (the cross-tenant
// room-scope fallback). It signs the request as the subject so the room service
// trusts the identity. Fails closed (false) on any error or when unconfigured.
func NewRoomMembershipProbe(roomBaseURL string, client *http.Client) func(string, string) bool {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	return func(subject, roomID string) bool {
		if roomBaseURL == "" {
			return false
		}
		req, err := http.NewRequest(http.MethodGet, roomBaseURL+"/v1/rooms/"+url.PathEscape(roomID)+"/membership/"+url.PathEscape(subject), nil)
		if err != nil {
			return false
		}
		req.Header.Set("X-Bnbloc-User", subject)
		svcauth.Sign(req, svcauth.KeyFromEnv(), subject)
		resp, err := client.Do(req)
		if err != nil {
			return false
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return false
		}
		var out struct {
			Member bool `json:"member"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			return false
		}
		return out.Member
	}
}

// NewRoomTenantResolver returns a RoomTenant resolver that asks bnbloc-room for
// a room's authoritative tenant (tenant-agnostic, keyed on room id). A short
// positive cache keeps the per-request lookup cheap; a room's tenant is
// immutable, so caching is safe. Returns "" on any error (leaves the session
// tenant in force — fail toward the existing behavior, never toward a wrong
// tenant). Signs as the daemon service subject since this is an internal,
// non-user-specific lookup.
func NewRoomTenantResolver(roomBaseURL string, client *http.Client) func(string) string {
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
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
		req, err := http.NewRequest(http.MethodGet, roomBaseURL+"/v1/rooms/"+url.PathEscape(roomID)+"/tenant", nil)
		if err != nil {
			return ""
		}
		req.Header.Set("X-Bnbloc-User", daemonServiceSubject)
		svcauth.Sign(req, svcauth.KeyFromEnv(), daemonServiceSubject)
		resp, err := client.Do(req)
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

// subjectInRoom reports whether subject is currently a member of roomID via a
// live lookup, used as the room-scope fallback when the baked token claim
// predates the membership (room created or joined after login). Fails closed
// (false) when no resolver is configured or the lookup errors.
func (s *Service) subjectInRoom(subject, roomID, tenantID string) bool {
	s.memberCacheOnce.Do(func() { s.memberCache = newBoundedCache[bool](memberCacheTTL, roomCacheMaxEntries) })
	key := subject + "\x00" + roomID + "\x00" + tenantID
	if _, ok := s.memberCache.get(key); ok {
		return true
	}
	if s.lookupSubjectInRoom(subject, roomID, tenantID) {
		// Positive results only: a denial is never cached, so a fresh join
		// takes effect immediately. A removal can lag by at most memberCacheTTL
		// at the edge; every backend re-checks membership itself.
		s.memberCache.put(key, true)
		return true
	}
	return false
}

// lookupSubjectInRoom performs the live membership lookup behind subjectInRoom.
func (s *Service) lookupSubjectInRoom(subject, roomID, tenantID string) bool {
	if s.Rooms != nil {
		for _, id := range s.Rooms(subject, tenantID) {
			if id == roomID {
				return true
			}
		}
	}
	// Cross-tenant fallback: a member whose room lives in a tenant other than
	// their token's (a multi-tenant user, or the bridge acting as one) is not
	// found by the tenant-scoped Rooms lookup above. Room ids are globally
	// unique, so a tenant-agnostic membership probe is authoritative.
	if s.InRoom != nil && s.InRoom(subject, roomID) {
		return true
	}
	return false
}
