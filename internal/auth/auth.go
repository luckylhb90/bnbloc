// Package auth provides the bnbloc identity primitives: an injectable OIDC token
// verifier interface (so the gateway builds without a live IdP) and a
// stdlib-only signed session token carrying the subject and room memberships.
package auth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// Identity is the verified subject returned by an OIDC/SAML verifier.
type Identity struct {
	Subject string
	Email   string
	// Tenant is the organization resolved from the IdP identity (SAML issuer or
	// OIDC claim). Empty when the verifier does not resolve one; the gateway
	// then falls back to the default tenant.
	Tenant string
}

// IDPVerifier verifies an external IdP token and returns the subject identity.
// Production wires an OIDC discovery-backed implementation; tests inject a fake.
type IDPVerifier interface {
	Verify(idpToken string) (Identity, error)
}

// ErrInvalidToken is returned when a bnbloc session token fails verification.
var ErrInvalidToken = errors.New("auth: invalid session token")

// Claims are the bnbloc session token claims.
type Claims struct {
	Subject string   `json:"sub"`
	Rooms   []string `json:"rooms"`
	// Tenant is the organization the subject belongs to. It scopes every
	// downstream data and authorization boundary; the gateway resolves it at
	// login from the verified IdP identity and forwards it to backends. Empty
	// only for legacy tokens minted before multi-tenancy — treated as the
	// "default" tenant by the gateway.
	Tenant string `json:"tenant,omitempty"`
	// Role is the subject's tenant-level role (org_admin|member|viewer) for
	// coarse RBAC at the edge. Room-level roles remain separate.
	Role          string `json:"role,omitempty"`
	ExpiresAtUnix int64  `json:"exp"`
	// Kind separates token families signed with the same key: empty for a
	// session bearer, KindRefresh for a long-lived refresh token that can only
	// mint new sessions (POST /v1/auth/refresh) and is never a bearer.
	Kind string `json:"kind,omitempty"`
	// Family and ID identify a refresh token server-side: the sign-in's token
	// family and this token's id (jti). The gateway's refresh-token store
	// accepts only the family's current id, so a rotated-away token presented
	// again is reuse and revokes the family. Empty on every other kind.
	Family string `json:"fam,omitempty"`
	ID     string `json:"jti,omitempty"`
}

// Token kinds (see Claims.Kind). KindRefresh mints sessions; KindSSORelay
// carries an SSO return address across an IdP that drops RelayState.
const (
	KindRefresh  = "refresh"
	KindSSORelay = "sso-relay"
	// KindPreviewerFrame is a short-lived ticket in a previewer bundle's
	// frame URL (an iframe src cannot carry the bearer): it authorizes only
	// reading the files of the one previewer bundle named in ID
	// ("<name>@<version>") for its subject and tenant.
	KindPreviewerFrame = "previewer-frame"
	// KindSiteFrame is a short-lived ticket in a site snapshot's frame URL
	// (/v1/sites/<ticket>/…): it authorizes only reading the files of the
	// one artifact version named in ID ("<roomId>/<artifactId>/<version>")
	// for its subject and tenant, and the room-level read is re-checked.
	KindSiteFrame = "site-frame"
	// KindAttachment is a short-lived ticket in a chat attachment's URL (an
	// <img src> or <a href> cannot carry the bearer): it authorizes only
	// GET of the one attachment named in ID ("<roomId>/<attachmentId>") for
	// its subject and tenant, and room membership is re-checked per read.
	KindAttachment = "attachment"
	// KindAppVisitor is a public link's visitor token (an interactive app's
	// participant without a session): Subject is a random visitor id and ID
	// the share link it was minted for. It authorizes nothing but app data
	// calls through that link, which re-resolve the link on every call.
	KindAppVisitor = "app-visitor"
	// KindShareTicket is a signed-in member's credential for one share link
	// that requires sign-in to its organization: Subject is the member,
	// Tenant the link's organization and ID the share id. It rides in the
	// link's URL path in place of the link's token and authorizes nothing
	// but that link's routes, which re-resolve the link and re-check the
	// member's organization membership.
	KindShareTicket = "share-ticket"
)

// HasRoom reports whether the claims authorize the given room scope.
func (c Claims) HasRoom(roomID string) bool {
	for _, r := range c.Rooms {
		if r == roomID {
			return true
		}
	}
	return false
}

// Signer issues and verifies bnbloc session tokens using HMAC-SHA256.
type Signer struct {
	key []byte
	now func() time.Time
}

// NewSigner constructs a Signer with the given secret key.
func NewSigner(key []byte) *Signer {
	return &Signer{key: key, now: time.Now}
}

// Issue serializes and signs claims into a "payload.signature" token.
func (s *Signer) Issue(c Claims) string {
	payload, _ := json.Marshal(c)
	enc := base64.RawURLEncoding.EncodeToString(payload)
	return enc + "." + s.sign(enc)
}

// Verify validates a session bearer, returning its claims. It rejects a bad
// signature, a missing expiry, an expired token, or a refresh token.
func (s *Signer) Verify(token string) (Claims, error) {
	c, err := s.verify(token)
	if err != nil || c.Kind != "" {
		return Claims{}, ErrInvalidToken
	}
	return c, nil
}

// VerifyRefresh validates a refresh token: same signature and expiry checks as
// Verify, but only Kind == KindRefresh is accepted.
func (s *Signer) VerifyRefresh(token string) (Claims, error) {
	return s.VerifyKind(token, KindRefresh)
}

// VerifyKind validates a signed token of one non-session kind.
func (s *Signer) VerifyKind(token, kind string) (Claims, error) {
	c, err := s.verify(token)
	if err != nil || kind == "" || c.Kind != kind {
		return Claims{}, ErrInvalidToken
	}
	return c, nil
}

func (s *Signer) verify(token string) (Claims, error) {
	parts := strings.SplitN(token, ".", 2)
	if len(parts) != 2 {
		return Claims{}, ErrInvalidToken
	}
	if !hmac.Equal([]byte(parts[1]), []byte(s.sign(parts[0]))) {
		return Claims{}, ErrInvalidToken
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[0])
	if err != nil {
		return Claims{}, ErrInvalidToken
	}
	var c Claims
	if err := json.Unmarshal(raw, &c); err != nil {
		return Claims{}, ErrInvalidToken
	}
	// Every session carries an expiry; a token without one would never expire.
	if c.ExpiresAtUnix == 0 || s.now().Unix() > c.ExpiresAtUnix {
		return Claims{}, ErrInvalidToken
	}
	return c, nil
}

// PeekSession decodes a session bearer's claims WITHOUT verifying its
// signature or expiry. It is for a client that holds its own bearer and needs
// to know whose it is (the `bnbloc smoke` check, the load tool); the gateway
// verifies the token on every call, so nothing here authorizes anything. It
// refuses a token that is not "<payload>.<signature>", a payload that is not
// base64url JSON, a non-session kind (refresh, frame tickets) and a session
// without a subject. Expiry is the caller's to judge (ExpiresAtUnix).
func PeekSession(token string) (Claims, error) {
	payload, _, ok := strings.Cut(token, ".")
	if !ok {
		return Claims{}, errors.New("not a bnbloc session token (want <payload>.<signature>)")
	}
	raw, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil {
		return Claims{}, fmt.Errorf("session token payload is not base64url: %w", err)
	}
	var c Claims
	if err := json.Unmarshal(raw, &c); err != nil {
		return Claims{}, fmt.Errorf("session token payload is not JSON: %w", err)
	}
	if c.Kind != "" {
		return Claims{}, fmt.Errorf("token is a %q token, not a session", c.Kind)
	}
	if c.Subject == "" {
		return Claims{}, errors.New("session token has no subject")
	}
	return c, nil
}

func (s *Signer) sign(payload string) string {
	mac := hmac.New(sha256.New, s.key)
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// ErrDirectLoginDisabled is returned by DisabledVerifier.
var ErrDirectLoginDisabled = errors.New("auth: direct token login is disabled; sign in through SSO")

// DisabledVerifier is the production verifier when no OIDC issuer is
// configured: it refuses every POST /v1/auth/login token, so the dev verifier
// (which accepts any identity string) can never serve a production gateway.
// Sign-in then happens only through an installed auth-provider (SSO), which
// mints sessions on its own verified path.
type DisabledVerifier struct{}

// Verify always fails.
func (DisabledVerifier) Verify(string) (Identity, error) { return Identity{}, ErrDirectLoginDisabled }
