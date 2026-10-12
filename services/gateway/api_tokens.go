package gateway

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/notify"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/secrettoken"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// API tokens (O1) are the credential of an unattended caller — the nightly
// smoke workflow, a deploy pipeline. A refresh token cannot serve: it
// rotates on every use and a CI secret cannot persist the rotated value, and
// a session bearer lives one hour. An API token is issued by a super-admin,
// stored only as a hash, listed, revocable, audited, and never a session by
// itself: POST /v1/auth/api-token exchanges it for a 1-hour session of its
// subject, re-running tenant resolution and the role lookup like a refresh
// (so removing a subject from BNBLOC_SUPER_ADMINS takes effect at the next
// exchange). Revoking a token stops further exchanges; a session already
// minted from it expires within SessionTTL.
//
// A token is issued for the issuing super-admin's own identity, or for a
// named service subject in the reserved "svc:" namespace — never for another
// person's identity (a super-admin cannot mint a credential that acts as a
// colleague).

// apiTokenFormat is "bnbloc_at_<32 hex id>_<secret>"; only the secret's hash
// is stored (internal/secrettoken).
var apiTokenFormat = secrettoken.Format{Prefix: "bnbloc_at_", IDLen: 32}

const (
	// ServiceSubjectPrefix is the reserved namespace of service identities.
	ServiceSubjectPrefix   = "svc:"
	defaultAPITokenTTLDays = 90
	maxAPITokenTTLDays     = 365
	maxAPITokenLabelRunes  = 128
	// apiTokenAuditKind tags API token lifecycle rows in the extension audit
	// trail (name = token id).
	apiTokenAuditKind = "api-token"
)

// serviceSubjectRe bounds a service subject: svc:<name>, lowercase.
var serviceSubjectRe = regexp.MustCompile(`^svc:[a-z0-9][a-z0-9._-]{0,62}$`)

// apiTokens returns the token store (process-local memory when none was
// wired: tests, a dev gateway without a database).
func (s *Service) apiTokens() store.APITokenStore {
	s.apiTokensOnce.Do(func() {
		if s.APITokens == nil {
			s.APITokens = store.NewMemoryAPITokens()
		}
	})
	return s.APITokens
}

type createAPITokenBody struct {
	// Subject defaults to the caller; otherwise a svc:<name> service subject.
	Subject string `json:"subject,omitempty"`
	Label   string `json:"label"`
	TTLDays int    `json:"ttlDays,omitempty"`
	// TenantID is the tenant hint the exchanged session resolves in (default
	// the caller's session tenant).
	TenantID string `json:"tenantId,omitempty"`
}

// apiTokenView is one token as listed (never the secret or its hash).
type apiTokenView struct {
	ID           string `json:"id"`
	Subject      string `json:"subject"`
	Label        string `json:"label"`
	Tenant       string `json:"tenant,omitempty"`
	CreatedBy    string `json:"createdBy"`
	CreatedAtMs  int64  `json:"createdAtMs"`
	ExpiresAtMs  int64  `json:"expiresAtMs"`
	LastUsedAtMs int64  `json:"lastUsedAtMs,omitempty"`
	RevokedAtMs  int64  `json:"revokedAtMs,omitempty"`
	RevokedBy    string `json:"revokedBy,omitempty"`
	Active       bool   `json:"active"`
}

func viewAPIToken(t store.APIToken, nowMs int64) apiTokenView {
	return apiTokenView{
		ID: t.TokenID, Subject: t.Subject, Label: t.Label, Tenant: t.TenantHint, CreatedBy: t.CreatedBy,
		CreatedAtMs: t.CreatedAtMs, ExpiresAtMs: t.ExpiresAtMs, LastUsedAtMs: t.LastUsedAtMs,
		RevokedAtMs: t.RevokedAtMs, RevokedBy: t.RevokedBy, Active: t.Active(nowMs),
	}
}

// handleCreateAPIToken issues a token (super-admin) and returns it ONCE.
func (s *Service) handleCreateAPIToken(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	var body createAPITokenBody
	if !httpx.DecodeJSON(w, r, maxAuthBodyBytes, &body) {
		return
	}
	label := strings.TrimSpace(body.Label)
	if label == "" || utf8.RuneCountInString(label) > maxAPITokenLabelRunes {
		httpx.Error(w, "label required (at most 128 characters)", http.StatusBadRequest)
		return
	}
	ttlDays := body.TTLDays
	if ttlDays == 0 {
		ttlDays = defaultAPITokenTTLDays
	}
	if ttlDays < 1 || ttlDays > maxAPITokenTTLDays {
		httpx.Error(w, "ttlDays must be between 1 and 365", http.StatusBadRequest)
		return
	}
	subject := strings.TrimSpace(body.Subject)
	email := ""
	switch {
	case subject == "" || subject == claims.Subject:
		subject = claims.Subject
		email = s.sessionEmail(r.Context(), claims)
	case !serviceSubjectRe.MatchString(subject):
		httpx.Error(w, "subject must be your own or a service subject svc:<name> (lowercase letters, digits, . _ -)", http.StatusBadRequest)
		return
	}
	tenantHint := strings.TrimSpace(body.TenantID)
	if tenantHint == "" {
		tenantHint = claims.Tenant
	}
	if len(tenantHint) > 64 {
		httpx.Error(w, "tenantId too long", http.StatusBadRequest)
		return
	}
	id, token, hash, err := apiTokenFormat.New()
	if err != nil {
		obs.ServerError(w, "issue token failed", err)
		httpx.Error(w, "issue token failed", http.StatusInternalServerError)
		return
	}
	now := s.nowTime()
	rec := store.APIToken{
		TokenID: id, SecretHash: hash, Subject: subject, Email: email, TenantHint: tenantHint, Label: label,
		CreatedBy: claims.Subject, CreatedAtMs: now.UnixMilli(), ExpiresAtMs: now.Add(time.Duration(ttlDays) * 24 * time.Hour).UnixMilli(),
	}
	if err := s.apiTokens().CreateAPIToken(r.Context(), rec); err != nil {
		obs.ServerError(w, "issue token failed", err)
		httpx.Error(w, "issue token failed", http.StatusInternalServerError)
		return
	}
	s.auditAPIToken(r.Context(), claims.Tenant, id, "issue", wire.AuditOutcomeAllow, claims.Subject, "subject="+subject+" label="+label)
	httpx.WriteJSON(w, http.StatusCreated, struct {
		apiTokenView
		Token string `json:"token"`
	}{viewAPIToken(rec, now.UnixMilli()), token})

}

// sessionEmail is the verified email of the caller's sign-in (from its
// refresh family), so a super-admin configured by email still matches when
// the token is exchanged. "" when unknown.
func (s *Service) sessionEmail(ctx context.Context, claims auth.Claims) string {
	if claims.Family == "" {
		return ""
	}
	fam, ok, err := s.refreshTokens().GetRefreshFamily(ctx, claims.Family)
	if err != nil || !ok || fam.Subject != claims.Subject {
		return ""
	}
	return fam.Email
}

func (s *Service) handleListAPITokens(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	rows, err := s.apiTokens().ListAPITokens(r.Context())
	if err != nil {
		obs.ServerError(w, "list tokens failed", err)
		httpx.Error(w, "list tokens failed", http.StatusInternalServerError)
		return
	}
	now := s.nowTime().UnixMilli()
	out := make([]apiTokenView, 0, len(rows))
	for _, t := range rows {
		out = append(out, viewAPIToken(t, now))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"tokens": out})
}

func (s *Service) handleRevokeAPIToken(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	id := strings.TrimSpace(r.PathValue("id"))
	revoked, err := s.apiTokens().RevokeAPIToken(r.Context(), id, claims.Subject, s.nowTime().UnixMilli())
	if err != nil {
		obs.ServerError(w, "revoke token failed", err)
		httpx.Error(w, "revoke token failed", http.StatusInternalServerError)
		return
	}
	if !revoked {
		httpx.Error(w, "token not found or already revoked", http.StatusNotFound)
		return
	}
	s.auditAPIToken(r.Context(), claims.Tenant, id, "revoke", wire.AuditOutcomeAllow, claims.Subject, "")
	w.WriteHeader(http.StatusNoContent)
}

// apiTokenGrant answers an exchange: a session only (no refresh token).
type apiTokenGrant struct {
	Token       string   `json:"sessionToken"`
	Subject     string   `json:"subject"`
	Tenant      string   `json:"tenant"`
	Role        string   `json:"role"`
	Rooms       []string `json:"rooms"`
	ExpiresAtMs int64    `json:"expiresAtMs"`
}

var errAPITokenRejected = errors.New("invalid, expired or revoked api token")

// handleAPITokenExchange trades an API token for a session. The token is
// the credential (no bearer). Throttled per client IP like login.
func (s *Service) handleAPITokenExchange(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Token string `json:"token"`
	}
	if !httpx.DecodeJSON(w, r, maxAuthBodyBytes, &body) {
		return
	}
	if strings.TrimSpace(body.Token) == "" {
		httpx.Error(w, "token required", http.StatusBadRequest)
		return
	}
	id, secret, ok := apiTokenFormat.Parse(body.Token)
	if !ok {
		httpx.Error(w, errAPITokenRejected.Error(), http.StatusUnauthorized)
		return
	}
	tok, found, err := s.apiTokens().GetAPIToken(r.Context(), id)
	if err != nil {
		obs.ServerError(w, "session issue failed", err)
		httpx.Error(w, "session issue failed", http.StatusInternalServerError)
		return
	}
	if !found {
		httpx.Error(w, errAPITokenRejected.Error(), http.StatusUnauthorized)
		return
	}
	now := s.nowTime()
	if !secrettoken.Verify(secret, tok.SecretHash) {
		s.auditAPIToken(r.Context(), tokenTenant(tok), id, "exchange", wire.AuditOutcomeDeny, tok.Subject, "secret mismatch")
		httpx.Error(w, errAPITokenRejected.Error(), http.StatusUnauthorized)
		return
	}
	if !tok.Active(now.UnixMilli()) {
		s.auditAPIToken(r.Context(), tokenTenant(tok), id, "exchange", wire.AuditOutcomeDeny, tok.Subject, "revoked or expired")
		httpx.Error(w, errAPITokenRejected.Error(), http.StatusUnauthorized)
		return
	}
	scope, err := s.resolveSession(auth.Identity{Subject: tok.Subject, Email: tok.Email, Tenant: tok.TenantHint})
	if errors.Is(err, errTenantSuspended) {
		s.auditAPIToken(r.Context(), tokenTenant(tok), id, "exchange", wire.AuditOutcomeDeny, tok.Subject, "tenant suspended")
	}
	if err != nil {
		writeSessionErr(w, err)
		return
	}
	_ = s.apiTokens().TouchAPIToken(r.Context(), id, now.UnixMilli())
	expires := now.Add(s.SessionTTL)
	session := mintSession(s.Signer, tok.Subject, "", scope, expires)
	s.auditAPIToken(r.Context(), scope.Tenant, id, "exchange", wire.AuditOutcomeAllow, tok.Subject, "")
	httpx.WriteJSON(w, http.StatusOK, apiTokenGrant{
		Token: session, Subject: tok.Subject, Tenant: scope.Tenant, Role: scope.Role, Rooms: scope.Rooms, ExpiresAtMs: expires.UnixMilli(),
	})

}

func (s *Service) auditAPIToken(ctx context.Context, tenantID, id, action, outcome, actor, detail string) {
	s.audit(ctx, tenantID, store.ExtAuditRecord{
		Kind: apiTokenAuditKind, Name: id, Action: action, Actor: actor, Outcome: outcome, Detail: detail,
	})
}

// handlePlatformAlert queues an admin_alert notification (super-admin):
// recipients default to every configured platform super-admin. It is queued
// in the tenant the request acts in (a super admin's acting tenant,
// tenant_scope.go) through every notifier component visible there; delivery is asynchronous (realtime's outbox worker).
func (s *Service) handlePlatformAlert(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	if s.Alerts == nil {
		obs.ServerError(w, "alerts unavailable (no notification store)", nil)
		httpx.Error(w, "alerts unavailable (no notification store)", http.StatusServiceUnavailable)
		return
	}
	var body wire.AdminAlertRequest
	if !httpx.DecodeJSON(w, r, maxAuthBodyBytes, &body) {
		return
	}
	recipients := body.Recipients
	if len(recipients) == 0 {
		recipients = s.SuperAdmins
	}
	dedup, err := randomTokenID()
	if err != nil {
		obs.ServerError(w, "alert failed", err)
		httpx.Error(w, "alert failed", http.StatusInternalServerError)
		return
	}
	tid := claims.Tenant
	res, err := s.Alerts.SendAlert(tenant.NewContext(r.Context(), tid), notify.Alert{
		Title: body.Title, Text: body.Text, URL: body.URL, Recipients: recipients, Actor: claims.Subject, DedupKey: dedup,
	})
	switch {
	case errors.Is(err, notify.ErrAlertNoRecipient):
		httpx.Error(w, "no recipients (pass recipients, or configure BNBLOC_SUPER_ADMINS)", http.StatusBadRequest)
		return
	case errors.Is(err, notify.ErrAlertInvalid):
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return
	case errors.Is(err, notify.ErrAlertNoNotifier):
		httpx.Error(w, "nothing reaches the recipients in tenant "+tid+" (no notifier component, notification plugin or browser push subscription); install a notifier (docs/notifications.md)", http.StatusConflict)
		return
	case err != nil:
		obs.ServerError(w, "alert failed", err)
		httpx.Error(w, "alert failed", http.StatusInternalServerError)
		return
	}
	s.audit(r.Context(), tid, store.ExtAuditRecord{
		Kind: wire.AuditKindAdminAlert, Name: res.ID, Action: "alert", Actor: claims.Subject, Outcome: wire.AuditOutcomeQueued,
		Detail: "notifiers=" + strings.Join(res.Notifiers, ",") + " recipients=" + strings.Join(res.Recipients, ","),
	})
	httpx.WriteJSON(w, http.StatusAccepted, res)
}

// tokenTenant is the tenant an API token's refused exchange is recorded in:
// the one it would have landed in.
func tokenTenant(tok store.APIToken) string {
	if t := strings.TrimSpace(tok.TenantHint); t != "" {
		return t
	}
	return tenant.Default
}
