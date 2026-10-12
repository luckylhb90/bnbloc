package gateway

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/rbac"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// sessionGrant is a freshly minted session for a verified identity: the
// response every login path (password/OIDC, SSO, the desktop SSO code
// exchange) returns.
type sessionGrant struct {
	Token string `json:"sessionToken"`
	// RefreshToken renews the session without re-running sign-in (POST
	// /v1/auth/refresh). It is how SSO-only deployments keep sessions and
	// bridges alive: there is no direct login to fall back to.
	RefreshToken string   `json:"refreshToken"`
	Subject      string   `json:"subject"`
	Tenant       string   `json:"tenant"`
	Role         string   `json:"role"`
	Rooms        []string `json:"rooms"`
}

// defaultRefreshTTL is how long an unused refresh token stays valid.
const defaultRefreshTTL = 30 * 24 * time.Hour

// refreshReuseGrace is how long after a rotation the immediately-previous
// refresh token is still a normal use (it yields the family's current token,
// never a second rotation). Two browser tabs share one stored refresh token
// and may renew at the same moment; without the grace the slower tab would
// trip reuse detection and sign the member out everywhere.
const refreshReuseGrace = 60 * time.Second

func (s *Service) refreshTTL() time.Duration {
	if s.RefreshTTL > 0 {
		return s.RefreshTTL
	}
	return defaultRefreshTTL
}

// errTenantSuspended refuses a login into a suspended tenant.
var errTenantSuspended = errors.New("tenant suspended")

// Client labels of a refresh family: the sign-in surface it belongs to, shown
// in the member's session list. A request may name its own (optional
// "client" body field); each endpoint otherwise defaults to the surface that
// calls it.
const (
	clientWeb     = "web"
	clientDesktop = "desktop"
	clientCLI     = "cli"
	clientBridge  = "bridge"
)

// refreshOrigin describes where a new refresh family is being created.
type refreshOrigin struct {
	Client    string
	UserAgent string
	// Parent is the family a fork was made from (empty for a sign-in).
	Parent string
}

// originFor derives a refresh origin from a request: the optional client
// label (only a known one is honored) else fallback, plus the User-Agent.
func originFor(r *http.Request, client, fallback string) refreshOrigin {
	switch client = strings.TrimSpace(client); client {
	case clientWeb, clientDesktop, clientCLI, clientBridge:
	default:
		client = fallback
	}
	ua := strings.TrimSpace(r.UserAgent())
	if len(ua) > 255 {
		ua = ua[:255]
	}
	return refreshOrigin{Client: client, UserAgent: ua}
}

// sessionScope is where a verified identity lands: tenant, org role, rooms.
type sessionScope struct {
	Tenant string
	Role   string
	Rooms  []string
}

// resolveSession is the ONE place tenant resolution, room scope and the
// suspended-tenant gate run for a verified identity, so every login path,
// refresh and SSO hand-off agree (the SSO path used to be a near-copy that
// skipped the suspension gate).
func (s *Service) resolveSession(identity auth.Identity) (sessionScope, error) {
	// Resolve the tenant (organization) BEFORE fetching room memberships: rooms
	// are tenant-scoped, so the membership list must be read from the tenant the
	// login lands in. The verifier may surface the tenant from the IdP; absent
	// that, single-tenant / dev deployments land in the default tenant.
	tenantID := strings.TrimSpace(identity.Tenant)
	if tenantID == "" {
		tenantID = tenant.Default
	}
	role := ""
	if s.TenantResolver != nil {
		// Optional hook: map/provision the tenant + resolve the subject's org
		// role (RBAC). A store failure fails the sign-in rather than landing
		// it with a guessed role.
		var err error
		if tenantID, role, err = s.TenantResolver(identity, tenantID); err != nil {
			return sessionScope{}, fmt.Errorf("resolve tenant: %w", err)
		}
	}
	return s.landIn(identity.Subject, tenantID, role)
}

// landIn is where a session lands once its tenant and org role are known:
// the suspended-tenant gate and the room scope. A suspended tenant is closed
// to its members; a platform super-admin is exempt (they must still reach
// the suspended org to re-activate it).
func (s *Service) landIn(subject, tenantID, role string) (sessionScope, error) {
	if s.tenantClosed(tenantID, role) {
		return sessionScope{}, errTenantSuspended
	}
	rooms := []string{}
	if s.Rooms != nil {
		if rs := s.Rooms(subject, tenantID); rs != nil {
			rooms = rs
		}
	}
	return sessionScope{Tenant: tenantID, Role: role, Rooms: rooms}, nil
}

// tenantClosed is the one suspended-tenant gate: a suspended tenant is
// closed to a member holding role there; a platform super admin is exempt.
func (s *Service) tenantClosed(tenantID, role string) bool {
	return s.TenantSuspended != nil && !rbac.Can(role, rbac.PermManagePlatform) && s.TenantSuspended(tenantID)
}

// errNotTenantMember refuses a switch into a tenant the caller does not
// belong to.
var errNotTenantMember = errors.New("not a member of that tenant")

// errSwitchUnavailable: no tenant directory to check a switch against.
var errSwitchUnavailable = errors.New("tenant switching not available")

// switchScope resolves a member's switch into target: a platform super admin
// may enter any tenant (keeping the platform role); anyone else only one
// they belong to, with their role there. It never provisions a membership.
func (s *Service) switchScope(claims auth.Claims, target string) (sessionScope, error) {
	role := claims.Role
	if !rbac.Can(claims.Role, rbac.PermManagePlatform) {
		r, err := s.memberRole(target, claims.Subject)
		if err != nil {
			return sessionScope{}, err
		}
		role = r
	}
	return s.landIn(claims.Subject, target, role)
}

// memberRole is subject's org role in tenantID from the tenant directory:
// errNotTenantMember when they do not belong to it, errSwitchUnavailable
// without a directory (fail closed). It never provisions a membership.
func (s *Service) memberRole(tenantID, subject string) (string, error) {
	if s.TenantMemberRole == nil {
		return "", errSwitchUnavailable
	}
	role, ok := s.TenantMemberRole(tenantID, subject)
	if !ok {
		return "", errNotTenantMember
	}
	return role, nil
}

// mintSession is the ONE way a session bearer is minted, for every path that
// issues one: sign-in, refresh and fork (grantFor), a tenant switch, the
// API-token exchange and the operator's bootstrap token (MintAdminToken).
// family is the refresh family the session renews through ("" for none).
func mintSession(signer *auth.Signer, subject, family string, scope sessionScope, expires time.Time) string {
	return signer.Issue(auth.Claims{
		Subject:       subject,
		Rooms:         scope.Rooms,
		Tenant:        scope.Tenant,
		Role:          scope.Role,
		Family:        family,
		ExpiresAtUnix: expires.Unix(),
	})
}

// issueSession is the ONE way the gateway turns a verified identity into a
// new sign-in: it resolves the session scope and opens a new refresh-token
// family for it. Every login path (direct, SSO browser, desktop exchange, CLI
// claim, bridge fork) calls it.
func (s *Service) issueSession(ctx context.Context, identity auth.Identity, origin refreshOrigin) (sessionGrant, error) {
	scope, err := s.resolveSession(identity)
	if err != nil {
		return sessionGrant{}, err
	}
	return s.openFamily(ctx, identity, scope, origin)
}

// openFamily records a new refresh family for an already-resolved scope and
// mints its first session + refresh token.
func (s *Service) openFamily(ctx context.Context, identity auth.Identity, scope sessionScope, origin refreshOrigin) (sessionGrant, error) {
	now := s.nowTime()
	familyID, err := randomTokenID()
	if err != nil {
		return sessionGrant{}, err
	}
	jti, err := randomTokenID()
	if err != nil {
		return sessionGrant{}, err
	}
	fam := store.RefreshFamily{
		FamilyID:       familyID,
		Subject:        identity.Subject,
		Email:          strings.TrimSpace(identity.Email),
		TenantHint:     strings.TrimSpace(identity.Tenant),
		TenantID:       scope.Tenant,
		Role:           scope.Role,
		Client:         origin.Client,
		UserAgent:      origin.UserAgent,
		ParentFamilyID: origin.Parent,
		CurrentJTI:     jti,
		// The family's identity (subject, email, the verifier's tenant hint)
		// is replayed at every refresh, which re-runs tenant resolution, role
		// lookup and the suspension gate exactly like this sign-in.
		RotatedAtMs:  now.UnixMilli(),
		CreatedAtMs:  now.UnixMilli(),
		LastUsedAtMs: now.UnixMilli(),
		ExpiresAtMs:  now.Add(s.refreshTTL()).UnixMilli(),
	}
	if err := s.refreshTokens().CreateRefreshFamily(ctx, fam); err != nil {
		return sessionGrant{}, err
	}
	return s.grantFor(fam, scope), nil
}

// grantFor mints the session bearer and the family's current refresh token.
func (s *Service) grantFor(fam store.RefreshFamily, scope sessionScope) sessionGrant {
	now := s.nowTime()
	token := mintSession(s.Signer, fam.Subject, fam.FamilyID, scope, now.Add(s.SessionTTL))
	refresh := s.Signer.Issue(auth.Claims{
		Subject:       fam.Subject,
		Tenant:        fam.TenantHint,
		Kind:          auth.KindRefresh,
		Family:        fam.FamilyID,
		ID:            fam.CurrentJTI,
		ExpiresAtUnix: now.Add(s.refreshTTL()).Unix(),
	})
	return sessionGrant{Token: token, RefreshToken: refresh, Subject: fam.Subject, Tenant: scope.Tenant, Role: scope.Role, Rooms: scope.Rooms}
}

// randomTokenID returns 128 random bits as 32 hex characters (family ids and
// token ids).
func randomTokenID() (string, error) {
	buf := make([]byte, 16)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	return hex.EncodeToString(buf), nil
}

// errRefreshRejected covers every refused refresh token (bad signature,
// expired, unknown/revoked family, subject mismatch, reuse).
var errRefreshRejected = errors.New("invalid or expired refresh token")

// useRefreshToken verifies a presented refresh token against its family and
// records the use: with rotate it moves the family to a new current token,
// without it only validates (the fork path). It returns the family (whose
// CurrentJTI is the token to hand out), the replayed identity and the scope
// the new session lands in. Reuse of a rotated-away token revokes the family.
func (s *Service) useRefreshToken(ctx context.Context, raw string, rotate bool) (store.RefreshFamily, auth.Identity, sessionScope, error) {
	claims, err := s.Signer.VerifyRefresh(strings.TrimSpace(raw))
	if err != nil || claims.Family == "" || claims.ID == "" {
		return store.RefreshFamily{}, auth.Identity{}, sessionScope{}, errRefreshRejected
	}
	tokens := s.refreshTokens()
	fam, ok, err := tokens.GetRefreshFamily(ctx, claims.Family)
	if err != nil {
		return store.RefreshFamily{}, auth.Identity{}, sessionScope{}, err
	}
	if !ok || fam.Subject != claims.Subject {
		return store.RefreshFamily{}, auth.Identity{}, sessionScope{}, errRefreshRejected
	}
	identity := auth.Identity{Subject: fam.Subject, Email: fam.Email, Tenant: fam.TenantHint}
	scope, err := s.resolveSession(identity)
	if err != nil {
		return store.RefreshFamily{}, auth.Identity{}, sessionScope{}, err
	}
	now := s.nowTime()
	use := store.RefreshUse{
		FamilyID:     fam.FamilyID,
		PresentedJTI: claims.ID,
		NowMs:        now.UnixMilli(),
		GraceMs:      refreshReuseGrace.Milliseconds(),
		ExpiresAtMs:  now.Add(s.refreshTTL()).UnixMilli(),
		TenantID:     scope.Tenant,
		Role:         scope.Role,
	}
	if rotate {
		if use.NewJTI, err = randomTokenID(); err != nil {
			return store.RefreshFamily{}, auth.Identity{}, sessionScope{}, err
		}
	}
	fam, err = tokens.UseRefreshFamily(ctx, use)
	if errors.Is(err, store.ErrRefreshReuse) {
		log.Printf("bnbloc-gateway: refresh token reuse for subject %q (family %s, client %q): family revoked", fam.Subject, fam.FamilyID, fam.Client)
		s.auditSessionRevoke(ctx, fam, "reuse_revoked", fam.Subject, "revoked")
		return store.RefreshFamily{}, auth.Identity{}, sessionScope{}, errRefreshRejected
	}
	if errors.Is(err, store.ErrRefreshInvalid) {
		return store.RefreshFamily{}, auth.Identity{}, sessionScope{}, errRefreshRejected
	}
	if err != nil {
		return store.RefreshFamily{}, auth.Identity{}, sessionScope{}, err
	}
	return fam, identity, scope, nil
}

// writeSessionErr answers a failure to issue a session — a sign-in, a
// refresh or fork, an SSO hand-off, an API-token exchange, a tenant
// switch: the one mapping of the session errors. A suspended tenant and a
// tenant the caller does not belong to are 403 everywhere (a client
// treats a 401 or 403 refresh as the end of the sign-in); a rejected
// refresh token is 401.
func writeSessionErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errTenantSuspended), errors.Is(err, errNotTenantMember):
		httpx.Error(w, err.Error(), http.StatusForbidden)
	case errors.Is(err, errRefreshRejected):
		httpx.Error(w, err.Error(), http.StatusUnauthorized)
	case errors.Is(err, errSwitchUnavailable):
		httpx.Unavailable(w, "tenant switching")
	default:
		obs.ServerError(w, "session issue failed", err)
		httpx.Error(w, "session issue failed", http.StatusInternalServerError)
	}
}

type refreshBody struct {
	RefreshToken string `json:"refreshToken"`
	// Client optionally labels a NEW family (fork); ignored by refresh.
	Client string `json:"client,omitempty"`
}

// decodeRefreshBody reads a {refreshToken} body (400 when missing).
func decodeRefreshBody(w http.ResponseWriter, r *http.Request) (refreshBody, bool) {
	var body refreshBody
	if !httpx.DecodeJSON(w, r, maxAuthBodyBytes, &body) {
		return refreshBody{}, false
	}
	if strings.TrimSpace(body.RefreshToken) == "" {
		httpx.Error(w, "refreshToken required", http.StatusBadRequest)
		return refreshBody{}, false
	}
	return body, true
}

// handleRefresh mints a fresh session and rotates the refresh token. The
// refresh token is the credential (no bearer). It re-resolves the session
// scope, so a suspended tenant or a changed role takes effect at the next
// refresh; presenting a rotated-away token (outside the short grace) revokes
// the whole family. No rate limit: the token is an HMAC-signed credential,
// not a guessable secret.
func (s *Service) handleRefresh(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeRefreshBody(w, r)
	if !ok {
		return
	}
	fam, _, scope, err := s.useRefreshToken(r.Context(), body.RefreshToken, true)
	if err != nil {
		writeSessionErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, s.grantFor(fam, scope))
}

// handleRefreshFork opens a NEW refresh family for the same sign-in, leaving
// the presented one untouched. A process that is handed a member's refresh
// token (the desktop bridge, an operator-configured bridge) forks once at
// start and rotates only its own family, so it never rotates the token the
// handing client keeps using — which would trip reuse detection and sign
// the member out.
func (s *Service) handleRefreshFork(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeRefreshBody(w, r)
	if !ok {
		return
	}
	parent, identity, scope, err := s.useRefreshToken(r.Context(), body.RefreshToken, false)
	if err != nil {
		writeSessionErr(w, err)
		return
	}
	// The fork remembers its parent so the member's session list nests the
	// agent under the device that runs it, and "sign out other devices" from
	// that device keeps its own agent.
	origin := originFor(r, body.Client, clientBridge)
	origin.Parent = parent.FamilyID
	grant, err := s.openFamily(r.Context(), identity, scope, origin)
	if err != nil {
		writeSessionErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, grant)
}

// handleLogout revokes the presented refresh token's family. Idempotent and
// unauthenticated by bearer (the refresh token is the credential): an
// invalid, expired or already-revoked token is a successful no-op, so a
// client can always complete its local sign-out.
func (s *Service) handleLogout(w http.ResponseWriter, r *http.Request) {
	body, ok := decodeRefreshBody(w, r)
	if !ok {
		return
	}
	claims, err := s.Signer.VerifyRefresh(strings.TrimSpace(body.RefreshToken))
	if err == nil && claims.Family != "" {
		revoked, err := s.refreshTokens().RevokeRefreshFamily(r.Context(), claims.Subject, claims.Family, s.nowTime().UnixMilli())
		if err != nil {
			obs.ServerError(w, "logout failed", err)
			httpx.Error(w, "logout failed", http.StatusInternalServerError)
			return
		}
		if revoked {
			s.auditSessionRevokeByID(r.Context(), claims.Family, "logout", claims.Subject)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

// mySession is one active sign-in in the member's session list.
type mySession struct {
	ID           string `json:"id"`
	Client       string `json:"client"`
	UserAgent    string `json:"userAgent,omitempty"`
	Tenant       string `json:"tenant,omitempty"`
	CreatedAtMs  int64  `json:"createdAtMs"`
	LastUsedAtMs int64  `json:"lastUsedAtMs"`
	ExpiresAtMs  int64  `json:"expiresAtMs"`
	// ParentID is the session a forked one (a desktop's agent bridge) was
	// forked from; absent for a direct sign-in.
	ParentID string `json:"parentId,omitempty"`
	// Current marks the family the caller's own session bearer came from.
	Current bool `json:"current"`
}

// handleMySessions lists the caller's active refresh families.
func (s *Service) handleMySessions(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	fams, err := s.refreshTokens().ListRefreshFamilies(r.Context(), claims.Subject, s.nowTime().UnixMilli())
	if err != nil {
		obs.ServerError(w, "list sessions failed", err)
		httpx.Error(w, "list sessions failed", http.StatusInternalServerError)
		return
	}
	out := make([]mySession, 0, len(fams))
	for _, f := range fams {
		out = append(out, mySession{
			ID: f.FamilyID, Client: f.Client, UserAgent: f.UserAgent, Tenant: f.TenantID,
			CreatedAtMs: f.CreatedAtMs, LastUsedAtMs: f.LastUsedAtMs, ExpiresAtMs: f.ExpiresAtMs,
			ParentID: f.ParentFamilyID, Current: f.FamilyID == claims.Family,
		})
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"sessions": out})
}

// handleRevokeMySession revokes one of the caller's own families (404 when
// the caller has no active family with that id), together with the sessions
// forked from it (the device's agent bridge). The family's session bearers
// stay valid until they expire (at most SessionTTL); it can no longer be
// refreshed.
func (s *Service) handleRevokeMySession(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	id := r.PathValue("id")
	revoked, err := s.refreshTokens().RevokeRefreshFamily(r.Context(), claims.Subject, id, s.nowTime().UnixMilli())
	if err != nil {
		obs.ServerError(w, "revoke session failed", err)
		httpx.Error(w, "revoke session failed", http.StatusInternalServerError)
		return
	}
	if !revoked {
		httpx.Error(w, "session not found", http.StatusNotFound)
		return
	}
	s.auditSessionRevokeByID(r.Context(), id, "revoke", claims.Subject)
	if _, err := s.revokeMySessionsWhere(r.Context(), claims.Subject, func(f store.RefreshFamily) bool {
		return f.ParentFamilyID == id
	}); err != nil {
		obs.ServerError(w, "revoke session failed", err)
		httpx.Error(w, "revoke session failed", http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleRevokeOtherSessions is DELETE /v1/my/sessions?others=1: it revokes
// every active family of the caller except the one the calling bearer came
// from and the sessions forked from it (this device's own agent bridge). The
// explicit others=1 guards against a bare DELETE reading as "revoke all".
// Answers {"revoked": n}.
func (s *Service) handleRevokeOtherSessions(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	if r.URL.Query().Get("others") != "1" {
		httpx.Error(w, "others=1 required", http.StatusBadRequest)
		return
	}
	if claims.Family == "" {
		// Without a family the current sign-in is unknown, so "others" is too.
		httpx.Error(w, "the current session has no sign-in record; sign in again", http.StatusConflict)
		return
	}
	n, err := s.revokeMySessionsWhere(r.Context(), claims.Subject, func(f store.RefreshFamily) bool {
		return f.FamilyID != claims.Family && f.ParentFamilyID != claims.Family
	})
	if err != nil {
		obs.ServerError(w, "revoke sessions failed", err)
		httpx.Error(w, "revoke sessions failed", http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"revoked": n})
}

// revokeMySessionsWhere revokes the subject's active families selected by
// pick. A member holds a handful of sign-ins, so selecting over the list is
// simpler than a bespoke store filter; a sign-in created after the list is
// read is a new sign-in and is (correctly) left alone.
func (s *Service) revokeMySessionsWhere(ctx context.Context, subject string, pick func(store.RefreshFamily) bool) (int, error) {
	tokens := s.refreshTokens()
	nowMs := s.nowTime().UnixMilli()
	fams, err := tokens.ListRefreshFamilies(ctx, subject, nowMs)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, f := range fams {
		if !pick(f) {
			continue
		}
		revoked, err := tokens.RevokeRefreshFamily(ctx, subject, f.FamilyID, nowMs)
		if err != nil {
			return n, err
		}
		if revoked {
			n++
			s.auditSessionRevoke(ctx, f, "revoke", subject, "revoked")
		}
	}
	return n, nil
}

// refreshTokens returns the refresh-family store. The composition root wires
// the shared (MySQL) store; a Service built without one (tests, a dev gateway
// whose store failed to open) gets a process-local memory store.
func (s *Service) refreshTokens() store.RefreshTokenStore {
	s.authStateOnce.Do(s.initAuthState)
	return s.RefreshTokens
}

// ssoHandoffStore returns the SSO hand-off store (see refreshTokens).
func (s *Service) ssoHandoffStore() store.SSOHandoffStore {
	s.authStateOnce.Do(s.initAuthState)
	return s.SSOHandoffs
}

func (s *Service) initAuthState() {
	if s.RefreshTokens == nil {
		s.RefreshTokens = store.NewMemoryRefreshToken()
	}
	if s.SSOHandoffs == nil {
		s.SSOHandoffs = store.NewMemorySSOHandoff()
	}
}

// ssoCodeTTL bounds how long a desktop SSO hand-off code stays redeemable: long
// enough for the browser -> deep link -> app hop, short enough that a leaked
// code is useless.
const ssoCodeTTL = 60 * time.Second

// cliGrantTTL bounds how long a CLI sign-in grant waits to be claimed. The CLI
// polls every few seconds, so it normally claims within one poll; the margin
// covers a slow network or a briefly suspended laptop.
const cliGrantTTL = 2 * time.Minute

// SSO hand-offs park a verified identity for a sign-in that did not start in
// the browser completing it. Two lookups share the durable table (so any
// gateway replica can complete what another parked):
//
//   - desktop: the bnbloc://sso deep link carries only a one-time code (never a
//     session token), bound to the nonce the desktop generated when it started
//     the flow, so a link crafted from someone else's login (login CSRF) cannot
//     be redeemed by a desktop that did not start that exact flow.
//   - CLI: nothing travels back through the browser at all; the CLI claims the
//     grant directly with the nonce it generated.
//
// Only hashes of the code / nonce are stored, and no token: the session (and
// its refresh family) is minted when the hand-off is redeemed. Every entry is
// single-use and expires.

// handoffKey hashes a hand-off secret into its table key, domain-separated so
// a desktop code can never collide with a CLI nonce.
func handoffKey(domain, secret string) string {
	sum := sha256.Sum256([]byte(domain + "\x00" + secret))
	return hex.EncodeToString(sum[:])
}

func nonceHash(nonce string) string {
	sum := sha256.Sum256([]byte(nonce))
	return hex.EncodeToString(sum[:])
}

const (
	handoffDesktopCode = "desktop-code"
	handoffCLINonce    = "cli-nonce"
)

// parkDesktopHandoff records identity under a fresh random code bound to
// nonce and returns the code.
func (s *Service) parkDesktopHandoff(ctx context.Context, identity auth.Identity, nonce string) (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", err
	}
	code := base64.RawURLEncoding.EncodeToString(buf)
	now := s.nowTime()
	err := s.ssoHandoffStore().PutSSOHandoff(ctx, store.SSOHandoff{
		KeyHash: handoffKey(handoffDesktopCode, code), NonceHash: nonceHash(nonce),
		Subject: identity.Subject, Email: identity.Email, TenantHint: identity.Tenant,
		ExpiresAtMs: now.Add(ssoCodeTTL).UnixMilli(),
	}, now.UnixMilli())
	if err != nil {
		return "", err
	}
	return code, nil
}

// redeemDesktopHandoff consumes code (single use, even on a nonce mismatch, so
// a code cannot be brute-forced against nonces) and returns its identity when
// the code is live and was issued for nonce.
func (s *Service) redeemDesktopHandoff(ctx context.Context, code, nonce string) (auth.Identity, bool, error) {
	h, ok, err := s.ssoHandoffStore().TakeSSOHandoff(ctx, handoffKey(handoffDesktopCode, code), s.nowTime().UnixMilli())
	if err != nil || !ok {
		return auth.Identity{}, false, err
	}
	if subtle.ConstantTimeCompare([]byte(nonceHash(nonce)), []byte(h.NonceHash)) != 1 {
		return auth.Identity{}, false, nil
	}
	return auth.Identity{Subject: h.Subject, Email: h.Email, Tenant: h.TenantHint}, true, nil
}

// parkCLIHandoff records identity for the CLI flow that generated nonce.
// store.ErrSSOHandoffPending when a grant for the nonce is still pending.
func (s *Service) parkCLIHandoff(ctx context.Context, identity auth.Identity, nonce string) error {
	now := s.nowTime()
	key := handoffKey(handoffCLINonce, nonce)
	return s.ssoHandoffStore().PutSSOHandoff(ctx, store.SSOHandoff{
		KeyHash: key, NonceHash: nonceHash(nonce),
		Subject: identity.Subject, Email: identity.Email, TenantHint: identity.Tenant,
		ExpiresAtMs: now.Add(cliGrantTTL).UnixMilli(),
	}, now.UnixMilli())
}

// claimCLIHandoff consumes the identity parked for nonce, if it is live. The
// lookup is by the nonce's hash, so possession of the nonce is the whole
// credential.
func (s *Service) claimCLIHandoff(ctx context.Context, nonce string) (auth.Identity, bool, error) {
	h, ok, err := s.ssoHandoffStore().TakeSSOHandoff(ctx, handoffKey(handoffCLINonce, nonce), s.nowTime().UnixMilli())
	if err != nil || !ok {
		return auth.Identity{}, false, err
	}
	return auth.Identity{Subject: h.Subject, Email: h.Email, Tenant: h.TenantHint}, true, nil
}

// bootstrapTokenTTL bounds a minted bootstrap session.
const bootstrapTokenTTL = 15 * time.Minute

// MintAdminToken issues a short-lived platform super-admin session for
// subject (the operator's `bnbloc-gateway mint-admin-token`), which must be
// listed in superAdmins (case-insensitive) — the same list login consults.
// The signing key must be the running gateway's stable key, or the token
// would not verify there. It mints through mintSession like every session.
func MintAdminToken(subject string, superAdmins []string, signingKey string, now time.Time) (string, error) {
	subject = strings.TrimSpace(subject)
	if signingKey == "" {
		return "", fmt.Errorf("BNBLOC_SESSION_SIGNING_KEY is not set (the running gateway could not verify the token)")
	}
	listed := false
	for _, a := range superAdmins {
		if strings.TrimSpace(a) == subject {
			listed = true
		}
	}
	if subject == "" || !listed {
		return "", fmt.Errorf("%q is not listed in BNBLOC_SUPER_ADMINS", subject)
	}
	scope := sessionScope{Tenant: tenant.Default, Role: string(rbac.RoleSuperAdmin), Rooms: []string{}}
	return mintSession(auth.NewSigner([]byte(signingKey)), subject, "", scope, now.Add(bootstrapTokenTTL)), nil
}
