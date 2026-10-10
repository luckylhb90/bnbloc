package auth

import (
	"context"
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// OIDCVerifier is a real, dependency-free OpenID Connect ID-token verifier. It
// fetches the issuer's discovery document and JWKS, then validates RS256 ID
// tokens (signature, issuer, audience, expiry) using only the standard library.
// It is the production IDPVerifier; the dev verifier remains for local use.
type OIDCVerifier struct {
	IssuerURL string
	ClientID  string
	HTTP      *http.Client
	Now       func() time.Time

	mu       sync.Mutex
	jwksURI  string
	keys     map[string]*rsa.PublicKey
	fetched  time.Time
	cacheTTL time.Duration
}

// minKeyRefresh bounds how often an unknown key id may trigger a JWKS refetch.
// Without it every token carrying a random kid forces an outbound discovery +
// JWKS round-trip (an amplification lever for unauthenticated callers); a
// genuine key rotation is still picked up within this interval.
const minKeyRefresh = time.Minute

// NewOIDCVerifier constructs a verifier for the given issuer and audience.
func NewOIDCVerifier(issuerURL, clientID string) *OIDCVerifier {
	return &OIDCVerifier{
		IssuerURL: strings.TrimRight(issuerURL, "/"),
		ClientID:  clientID,
		HTTP:      &http.Client{Timeout: 10 * time.Second},
		Now:       time.Now,
		keys:      map[string]*rsa.PublicKey{},
		cacheTTL:  time.Hour,
	}
}

// Verify validates an OIDC ID token and returns the subject identity.
func (v *OIDCVerifier) Verify(idToken string) (Identity, error) {
	parts := strings.Split(idToken, ".")
	if len(parts) != 3 {
		return Identity{}, errors.New("oidc: not a JWT")
	}
	header, err := decodeSegment(parts[0])
	if err != nil {
		return Identity{}, err
	}
	var h struct {
		Alg string `json:"alg"`
		Kid string `json:"kid"`
	}
	if err := json.Unmarshal(header, &h); err != nil {
		return Identity{}, err
	}
	if h.Alg != "RS256" {
		return Identity{}, fmt.Errorf("oidc: unsupported alg %q", h.Alg)
	}
	key, err := v.keyForKid(h.Kid)
	if err != nil {
		return Identity{}, err
	}
	signed := parts[0] + "." + parts[1]
	sig, err := decodeSegment(parts[2])
	if err != nil {
		return Identity{}, err
	}
	if err := verifyRS256(key, signed, sig); err != nil {
		return Identity{}, fmt.Errorf("oidc: signature: %w", err)
	}

	payload, err := decodeSegment(parts[1])
	if err != nil {
		return Identity{}, err
	}
	var claims struct {
		Iss   string `json:"iss"`
		Sub   string `json:"sub"`
		Aud   any    `json:"aud"`
		Email string `json:"email"`
		Exp   int64  `json:"exp"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return Identity{}, err
	}
	if strings.TrimRight(claims.Iss, "/") != v.IssuerURL {
		return Identity{}, fmt.Errorf("oidc: issuer mismatch")
	}
	if !audienceContains(claims.Aud, v.ClientID) {
		return Identity{}, fmt.Errorf("oidc: audience mismatch")
	}
	// exp is REQUIRED for an ID token (OIDC Core 2): a token without it would
	// be valid forever.
	if claims.Exp == 0 || v.Now().Unix() > claims.Exp {
		return Identity{}, fmt.Errorf("oidc: token expired or missing exp")
	}
	if claims.Sub == "" {
		return Identity{}, fmt.Errorf("oidc: missing subject")
	}
	return Identity{Subject: claims.Sub, Email: claims.Email}, nil
}

func (v *OIDCVerifier) keyForKid(kid string) (*rsa.PublicKey, error) {
	v.mu.Lock()
	age := v.Now().Sub(v.fetched)
	key := v.keys[kid]
	v.mu.Unlock()
	if key != nil && age < v.cacheTTL {
		return key, nil
	}
	if key == nil && !v.fetched.IsZero() && age < minKeyRefresh {
		return nil, fmt.Errorf("oidc: unknown key id %q", kid)
	}
	if err := v.refreshKeys(); err != nil {
		return nil, err
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if k := v.keys[kid]; k != nil {
		return k, nil
	}
	return nil, fmt.Errorf("oidc: unknown key id %q", kid)
}

func (v *OIDCVerifier) refreshKeys() error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	v.mu.Lock()
	jwksURI := v.jwksURI
	v.mu.Unlock()
	if jwksURI == "" {
		var disc struct {
			JWKSURI string `json:"jwks_uri"`
		}
		if err := v.getJSON(ctx, v.IssuerURL+"/.well-known/openid-configuration", &disc); err != nil {
			return fmt.Errorf("oidc: discovery: %w", err)
		}
		if disc.JWKSURI == "" {
			return errors.New("oidc: discovery missing jwks_uri")
		}
		jwksURI = disc.JWKSURI
		v.mu.Lock()
		v.jwksURI = jwksURI
		v.mu.Unlock()
	}
	var jwks struct {
		Keys []struct {
			Kid string `json:"kid"`
			Kty string `json:"kty"`
			N   string `json:"n"`
			E   string `json:"e"`
		} `json:"keys"`
	}
	if err := v.getJSON(ctx, jwksURI, &jwks); err != nil {
		return fmt.Errorf("oidc: jwks: %w", err)
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range jwks.Keys {
		if k.Kty != "RSA" {
			continue
		}
		nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			continue
		}
		eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			continue
		}
		keys[k.Kid] = &rsa.PublicKey{
			N: new(big.Int).SetBytes(nBytes),
			E: int(new(big.Int).SetBytes(eBytes).Int64()),
		}
	}
	v.mu.Lock()
	v.keys = keys
	v.fetched = v.Now()
	v.mu.Unlock()
	return nil
}

func (v *OIDCVerifier) getJSON(ctx context.Context, url string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := v.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("status %d", resp.StatusCode)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func decodeSegment(seg string) ([]byte, error) {
	return base64.RawURLEncoding.DecodeString(seg)
}

func audienceContains(aud any, clientID string) bool {
	switch v := aud.(type) {
	case string:
		return v == clientID
	case []any:
		for _, a := range v {
			if s, ok := a.(string); ok && s == clientID {
				return true
			}
		}
	}
	return false
}

func verifyRS256(key *rsa.PublicKey, signed string, sig []byte) error {
	h := sha256.Sum256([]byte(signed))
	return rsa.VerifyPKCS1v15(key, crypto.SHA256, h[:], sig)
}
