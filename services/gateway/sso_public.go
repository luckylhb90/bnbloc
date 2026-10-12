package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/authprovider"
	"github.com/luckylhb90/bnbloc/internal/extreg"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
)

const (
	authProviderInitPath = "/v1/auth-provider/init"
	authProviderACSPath  = "/v1/auth-provider/acs"
	authProviderMetaPath = "/v1/auth-provider/metadata"
	authProviderSLOPath  = "/v1/auth-provider/logout"
	publicAuthStartPath  = "/api/auth/init"
	// defaultPublicACSPath is where the IdP posts assertions unless the
	// deployment registered a different ACS URL with its IdP (AuthACSPath).
	defaultPublicACSPath = "/api/auth/acs"
)

// handleSSOStart begins an SSO sign-in at the active auth provider,
// remembering where the browser returns.
func (s *Service) handleSSOStart(w http.ResponseWriter, r *http.Request) {
	s.rememberSSORelay(w, r)
	s.proxyAuthProvider(w, r, authProviderInitPath)
}

// handleSSOMetadata serves the auth provider's SP metadata to the IdP.
func (s *Service) handleSSOMetadata(w http.ResponseWriter, r *http.Request) {
	s.proxyAuthProvider(w, r, authProviderMetaPath)
}

// handleSSOLogout passes the IdP's single logout to the auth provider.
func (s *Service) handleSSOLogout(w http.ResponseWriter, r *http.Request) {
	s.proxyAuthProvider(w, r, authProviderSLOPath)
}

// publicACSPath is the configured public assertion consumer path.
func (s *Service) publicACSPath() string {
	if p := strings.TrimSpace(s.AuthACSPath); p != "" {
		return p
	}
	return defaultPublicACSPath
}

func (s *Service) activeAuthProvider(ctx context.Context) (authprovider.Endpoint, bool) {
	for _, ext := range extreg.List(extreg.KindAuthProvider) {
		provider, ok := ext.Instance.(authprovider.Endpoint)
		if ok && provider.Available(ctx) {
			return provider, true
		}
	}
	return authprovider.Endpoint{}, false
}

// handleAuthProviderConfig tells the login page which sign-in paths exist:
// SSO through an installed auth-provider, and direct token login (absent in
// production without OIDC, where the verifier is auth.DisabledVerifier).
func (s *Service) handleAuthProviderConfig(w http.ResponseWriter, r *http.Request) {
	_, disabled := s.Verifier.(auth.DisabledVerifier)
	out := map[string]any{"authProviderAvailable": false, "directLoginAvailable": !disabled}
	if _, ok := s.activeAuthProvider(r.Context()); ok {
		out["authProviderAvailable"] = true
		out["authProviderStartPath"] = publicAuthStartPath
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

func (s *Service) proxyAuthProvider(w http.ResponseWriter, r *http.Request, providerPath string) {
	provider, ok := s.activeAuthProvider(r.Context())
	if !ok {
		httpx.Unavailable(w, "auth provider is")
		return
	}
	base, err := url.Parse(provider.BaseURL)
	if err != nil {
		obs.ServerError(w, "auth provider endpoint invalid", err)
		httpx.Error(w, "auth provider endpoint invalid", http.StatusInternalServerError)
		return
	}
	proxy := httputil.NewSingleHostReverseProxy(base)
	proxy.Transport = provider.Client.Transport
	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.URL.Path = providerPath
	}
	proxy.ErrorHandler = obs.ProxyError("auth provider proxy failed")
	proxy.ServeHTTP(w, r)
}

func (s *Service) handleAuthProviderACS(w http.ResponseWriter, r *http.Request) {
	provider, ok := s.activeAuthProvider(r.Context())
	if !ok {
		httpx.Unavailable(w, "auth provider is")
		return
	}
	if err := r.ParseForm(); err != nil {
		httpx.Error(w, "invalid auth response", http.StatusBadRequest)
		return
	}
	endpoint, err := url.Parse(provider.BaseURL + authProviderACSPath)
	if err != nil {
		obs.ServerError(w, "auth provider endpoint invalid", err)
		httpx.Error(w, "auth provider endpoint invalid", http.StatusInternalServerError)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, endpoint.String(), strings.NewReader(r.PostForm.Encode()))
	if err != nil {
		obs.ServerError(w, "auth provider request invalid", err)
		httpx.Error(w, "auth provider request invalid", http.StatusInternalServerError)
		return
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	client := *provider.Client
	client.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	resp, err := client.Do(req)
	if err != nil {
		obs.ServerError(w, "auth provider unavailable", err)
		httpx.Error(w, "auth provider unavailable", http.StatusServiceUnavailable)
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= http.StatusMultipleChoices && resp.StatusCode < http.StatusBadRequest {
		location := resp.Header.Get("Location")
		if strings.TrimSpace(location) == "" {
			obs.ServerError(w, "auth provider redirect missing location", nil)
			httpx.Error(w, "auth provider redirect missing location", http.StatusBadGateway)
			return
		}
		http.Redirect(w, r, location, http.StatusFound)
		return
	}
	if resp.StatusCode != http.StatusOK {
		httpx.Error(w, "auth provider rejected response", http.StatusUnauthorized)
		return
	}
	limited := http.MaxBytesReader(w, resp.Body, 64*1024)
	var assertion authprovider.Assertion
	if err := json.NewDecoder(limited).Decode(&assertion); err != nil {
		_, _ = io.Copy(io.Discard, limited)
		obs.ServerError(w, "auth provider response invalid", err)
		httpx.Error(w, "auth provider response invalid", http.StatusBadGateway)
		return
	}
	subject := strings.TrimSpace(assertion.Subject)
	if subject == "" {
		subject = strings.TrimSpace(assertion.Email)
	}
	if subject == "" {
		httpx.Error(w, "auth provider response missing identity", http.StatusUnauthorized)
		return
	}
	identity := auth.Identity{Subject: subject, Email: assertion.Email}
	// Organization sync converges the subject's memberships on the asserted
	// directory attributes before the session scope is resolved, and lands
	// the sign-in in the first matching rule's tenant. Membership is an
	// access decision, so a failed sync refuses the sign-in.
	if tid, err := s.applyOrgSync(r.Context(), identity, assertion); err != nil {
		obs.ServerError(w, "org sync at sign-in", err)
		httpx.Error(w, "organization sync unavailable", http.StatusServiceUnavailable)
		return
	} else if tid != "" {
		identity.Tenant = tid
	}
	s.completeSSO(w, r, identity, assertion.Relay)
}

// desktopNonceParam is the redirect_uri query parameter a desktop-initiated SSO
// flow carries: the random nonce the desktop generated and kept. The auth
// provider round-trips redirect_uri through RelayState unchanged, so the nonce
// comes back here and binds the hand-off code to the flow that started it.
const desktopNonceParam = "desktop_nonce"

// cliNonceParam is the redirect_uri query parameter a CLI-initiated SSO flow
// (`bnbloc login`) carries: the random nonce the CLI generated and kept. The
// grant is parked under sha256(nonce) for the CLI to claim directly
// (POST /v1/auth/sso/claim); the browser receives neither a token nor a code.
const cliNonceParam = "cli_nonce"

// minCLINonceLen is the shortest CLI nonce accepted. The nonce alone claims
// the grant, so it must be unguessable (the CLI sends 256 random bits,
// base64url: 43 characters).
const minCLINonceLen = 32

// completeSSO mints the session for a verified SSO identity and redirects to
// the validated relay target. A browser flow receives the session in the URL
// fragment (never sent to a server); a desktop flow (relay carries
// desktop_nonce) receives only a short-lived one-time code bound to that
// nonce, which the browser forwards to the app via the bnbloc://sso deep link
// and the app redeems over HTTPS (POST /v1/auth/sso/exchange). A CLI flow
// (relay carries cli_nonce) parks the grant for the CLI to claim with its
// nonce (POST /v1/auth/sso/claim); the browser only learns that sign-in
// finished (#cliSignedIn=1).
func (s *Service) completeSSO(w http.ResponseWriter, r *http.Request, identity auth.Identity, relay string) {
	// The return address this browser asked for at sign-in start wins over
	// the provider's: an IdP-hosted login page drops RelayState, so without it
	// a desktop-initiated flow (desktop_nonce) would complete as a browser one.
	if remembered := s.takeSSORelay(w, r); remembered != "" {
		relay = remembered
	}
	target, err := s.ssoRelayTarget(r, relay)
	if err != nil {
		httpx.Error(w, "invalid sso redirect target", http.StatusBadRequest)
		return
	}
	// Resolve the scope up front so every flow shares the suspended-tenant
	// gate; the hand-off flows park only the verified identity and mint the
	// session (and its refresh family) when the client redeems.
	scope, err := s.resolveSession(identity)
	if err != nil {
		writeSessionErr(w, err)
		return
	}
	fragment := url.Values{}
	q := target.Query()
	desktopNonce := strings.TrimSpace(q.Get(desktopNonceParam))
	cliNonce := strings.TrimSpace(q.Get(cliNonceParam))
	switch {
	case desktopNonce != "" && cliNonce != "":
		httpx.Error(w, "sso redirect target names two hand-off flows", http.StatusBadRequest)
		return
	case cliNonce != "":
		if len(cliNonce) < minCLINonceLen {
			httpx.Error(w, "cli sign-in nonce too short", http.StatusBadRequest)
			return
		}
		q.Del(cliNonceParam)
		target.RawQuery = q.Encode()
		if err := s.parkCLIHandoff(r.Context(), identity, cliNonce); errors.Is(err, store.ErrSSOHandoffPending) {
			httpx.Error(w, "cli sign-in already completed", http.StatusConflict)
			return
		} else if err != nil {
			obs.ServerError(w, "sso hand-off unavailable", err)
			httpx.Error(w, "sso hand-off unavailable", http.StatusServiceUnavailable)
			return
		}
		fragment.Set("cliSignedIn", "1")
	case desktopNonce != "":
		q.Del(desktopNonceParam)
		target.RawQuery = q.Encode()
		code, err := s.parkDesktopHandoff(r.Context(), identity, desktopNonce)
		if err != nil {
			obs.ServerError(w, "sso hand-off unavailable", err)
			httpx.Error(w, "sso hand-off unavailable", http.StatusServiceUnavailable)
			return
		}
		fragment.Set("ssoCode", code)
	default:
		grant, err := s.openFamily(r.Context(), identity, scope, originFor(r, "", clientWeb))
		if err != nil {
			obs.ServerError(w, "session issue failed", err)
			httpx.Error(w, "session issue failed", http.StatusInternalServerError)
			return
		}
		fragment.Set("sessionToken", grant.Token)
		fragment.Set("refreshToken", grant.RefreshToken)
		fragment.Set("subject", grant.Subject)
	}
	target.Fragment = ""
	http.Redirect(w, r, target.String()+"#"+fragment.Encode(), http.StatusFound)
}

// ssoRelayCookie carries the sign-in return address from /api/auth/init to the
// ACS. It is gateway-signed (KindSSORelay, never a bearer), short-lived,
// HttpOnly, and scoped to /api/ (the ACS path is configurable under /api/).
const (
	ssoRelayCookie = "bnbloc_sso_relay"
	ssoRelayTTL    = 10 * time.Minute
)

// rememberSSORelay stores a valid redirect_uri from the sign-in start request
// in the relay cookie. Invalid or absent relays are not remembered.
func (s *Service) rememberSSORelay(w http.ResponseWriter, r *http.Request) {
	relay := strings.TrimSpace(r.URL.Query().Get("redirect_uri"))
	if relay == "" {
		return
	}
	if _, err := s.ssoRelayTarget(r, relay); err != nil {
		return
	}
	token := s.Signer.Issue(auth.Claims{Subject: relay, Kind: auth.KindSSORelay, ExpiresAtUnix: s.Now().Add(ssoRelayTTL).Unix()})
	http.SetCookie(w, s.ssoRelayCookieFor(r, token, int(ssoRelayTTL/time.Second)))
}

// takeSSORelay returns the remembered return address (if the cookie is valid)
// and clears the cookie: a relay is used for exactly one sign-in.
func (s *Service) takeSSORelay(w http.ResponseWriter, r *http.Request) string {
	c, err := r.Cookie(ssoRelayCookie)
	if err != nil || c.Value == "" {
		return ""
	}
	http.SetCookie(w, s.ssoRelayCookieFor(r, "", -1))
	claims, err := s.Signer.VerifyKind(c.Value, auth.KindSSORelay)
	if err != nil {
		return ""
	}
	return claims.Subject
}

// ssoRelayCookieFor builds the relay cookie. The IdP posts the assertion to
// the ACS cross-site, which only carries SameSite=None (hence Secure) cookies;
// plain-http development falls back to Lax.
func (s *Service) ssoRelayCookieFor(r *http.Request, value string, maxAge int) *http.Cookie {
	secure := r.TLS != nil || strings.EqualFold(strings.TrimSpace(r.Header.Get("X-Forwarded-Proto")), "https")
	sameSite := http.SameSiteLaxMode
	if secure {
		sameSite = http.SameSiteNoneMode
	}
	return &http.Cookie{Name: ssoRelayCookie, Value: value, Path: "/api/", MaxAge: maxAge, HttpOnly: true, Secure: secure, SameSite: sameSite}
}

// ssoRelayTarget validates where the SSO callback may land: a same-origin
// relative path ("/sso-callback"), or an absolute http(s) URL whose host is
// this gateway's own host or whose origin is a configured allowed origin.
// Anything else is an open redirect that would hand a session (or code) to a
// foreign site. An empty relay lands on "/".
func (s *Service) ssoRelayTarget(r *http.Request, relay string) (*url.URL, error) {
	relay = strings.TrimSpace(relay)
	if relay == "" {
		relay = "/"
	}
	u, err := url.Parse(relay)
	if err != nil {
		return nil, err
	}
	if u.Scheme == "" && u.Host == "" && u.User == nil {
		// Relative: must be an absolute path that cannot be read as
		// scheme-relative ("//evil", "/\evil").
		if !strings.HasPrefix(u.Path, "/") || strings.HasPrefix(relay, "//") || strings.Contains(relay, "\\") {
			return nil, errors.New("relay must be an absolute path")
		}
		return u, nil
	}
	if (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return nil, errors.New("relay must be http(s)")
	}
	if strings.EqualFold(u.Host, r.Host) || s.allowedOrigin(u.Scheme+"://"+u.Host) {
		return u, nil
	}
	return nil, errors.New("relay origin not allowed")
}

// maxAuthBodyBytes caps unauthenticated auth request bodies.
const maxAuthBodyBytes = 16 * 1024

// handleSSOExchange redeems a desktop SSO hand-off code for the session it was
// issued for. The caller must present the nonce the flow was started with;
// the code is single-use and expires after ssoCodeTTL. No rate limit: the
// code is 256 random bits bound to the caller's nonce, so it cannot be
// guessed, and a per-IP throttle would make every user behind one proxy
// share a bucket at sign-in.
func (s *Service) handleSSOExchange(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Code  string `json:"code"`
		Nonce string `json:"nonce"`
	}
	if !httpx.DecodeJSON(w, r, maxAuthBodyBytes, &body) {
		return
	}
	if body.Code == "" || body.Nonce == "" {
		httpx.Error(w, "code and nonce required", http.StatusBadRequest)
		return
	}
	identity, ok, err := s.redeemDesktopHandoff(r.Context(), body.Code, body.Nonce)
	if err != nil {
		obs.ServerError(w, "sso hand-off unavailable", err)
		httpx.Error(w, "sso hand-off unavailable", http.StatusServiceUnavailable)
		return
	}
	if !ok {
		httpx.Error(w, "invalid or expired sso code", http.StatusUnauthorized)
		return
	}
	s.writeHandoffGrant(w, r, identity, clientDesktop)
}

// handleSSOClaim hands a CLI sign-in its grant. The CLI started the flow with
// redirect_uri=...?cli_nonce=<nonce> and polls here with the same nonce: 404
// until the sign-in completes (and after the grant expired or was claimed),
// then 200 with the grant exactly once. No rate limit, for the same reason as
// the exchange: the nonce is 256 random bits, and the lookup is by its hash.
func (s *Service) handleSSOClaim(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Nonce string `json:"nonce"`
	}
	if !httpx.DecodeJSON(w, r, maxAuthBodyBytes, &body) {
		return
	}
	if strings.TrimSpace(body.Nonce) == "" {
		httpx.Error(w, "nonce required", http.StatusBadRequest)
		return
	}
	identity, ok, err := s.claimCLIHandoff(r.Context(), strings.TrimSpace(body.Nonce))
	if err != nil {
		obs.ServerError(w, "sso hand-off unavailable", err)
		httpx.Error(w, "sso hand-off unavailable", http.StatusServiceUnavailable)
		return
	}
	if !ok {
		httpx.Error(w, "no completed sign-in for this nonce", http.StatusNotFound)
		return
	}
	s.writeHandoffGrant(w, r, identity, clientCLI)
}

// writeHandoffGrant mints the session for a redeemed SSO hand-off. The scope
// is resolved again (the gate holds even if the tenant was suspended between
// sign-in and redemption).
func (s *Service) writeHandoffGrant(w http.ResponseWriter, r *http.Request, identity auth.Identity, client string) {
	grant, err := s.issueSession(r.Context(), identity, originFor(r, "", client))
	if err != nil {
		writeSessionErr(w, err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, grant)
}
