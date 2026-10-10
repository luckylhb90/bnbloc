package auth

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

// fakeIdP serves OIDC discovery + JWKS for one RSA key and counts JWKS fetches.
type fakeIdP struct {
	srv       *httptest.Server
	key       *rsa.PrivateKey
	kid       string
	jwksFetch atomic.Int32
}

func newFakeIdP(t *testing.T) *fakeIdP {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	idp := &fakeIdP{key: key, kid: "k1"}
	mux := http.NewServeMux()
	mux.HandleFunc("/.well-known/openid-configuration", func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"issuer": idp.srv.URL, "jwks_uri": idp.srv.URL + "/jwks"})
	})
	mux.HandleFunc("/jwks", func(w http.ResponseWriter, _ *http.Request) {
		idp.jwksFetch.Add(1)
		_ = json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kid": idp.kid, "kty": "RSA",
			"n": base64.RawURLEncoding.EncodeToString(key.N.Bytes()),
			"e": base64.RawURLEncoding.EncodeToString(big.NewInt(int64(key.E)).Bytes()),
		}}})
	})
	idp.srv = httptest.NewServer(mux)
	t.Cleanup(idp.srv.Close)
	return idp
}

func (idp *fakeIdP) token(t *testing.T, alg, kid string, claims map[string]any) string {
	t.Helper()
	enc := func(v any) string {
		raw, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(raw)
	}
	signed := enc(map[string]string{"alg": alg, "kid": kid}) + "." + enc(claims)
	h := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, idp.key, crypto.SHA256, h[:])
	if err != nil {
		t.Fatal(err)
	}
	return signed + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func (idp *fakeIdP) claims(over map[string]any) map[string]any {
	c := map[string]any{"iss": idp.srv.URL, "sub": "alice", "aud": "bnbloc", "email": "a@x", "exp": time.Now().Add(time.Hour).Unix()}
	for k, v := range over {
		if v == nil {
			delete(c, k)
			continue
		}
		c[k] = v
	}
	return c
}

func TestOIDCVerifierAcceptsValidToken(t *testing.T) {
	idp := newFakeIdP(t)
	v := NewOIDCVerifier(idp.srv.URL+"/", "bnbloc")
	id, err := v.Verify(idp.token(t, "RS256", "k1", idp.claims(map[string]any{"aud": []any{"other", "bnbloc"}})))
	if err != nil {
		t.Fatalf("verify: %v", err)
	}
	if id.Subject != "alice" || id.Email != "a@x" {
		t.Fatalf("identity = %+v", id)
	}
}

func TestOIDCVerifierRejectsBadTokens(t *testing.T) {
	idp := newFakeIdP(t)
	v := NewOIDCVerifier(idp.srv.URL, "bnbloc")
	good := idp.token(t, "RS256", "k1", idp.claims(nil))
	cases := map[string]string{
		"wrong issuer":  idp.token(t, "RS256", "k1", idp.claims(map[string]any{"iss": "https://evil"})),
		"wrong aud":     idp.token(t, "RS256", "k1", idp.claims(map[string]any{"aud": "someone-else"})),
		"expired":       idp.token(t, "RS256", "k1", idp.claims(map[string]any{"exp": time.Now().Add(-time.Minute).Unix()})),
		"missing exp":   idp.token(t, "RS256", "k1", idp.claims(map[string]any{"exp": nil})),
		"missing sub":   idp.token(t, "RS256", "k1", idp.claims(map[string]any{"sub": ""})),
		"alg none/HS":   idp.token(t, "HS256", "k1", idp.claims(nil)),
		"unknown kid":   idp.token(t, "RS256", "k2", idp.claims(nil)),
		"bad signature": good[:len(good)-4] + "AAAA",
		"not a jwt":     "abc",
	}
	for name, tok := range cases {
		if _, err := v.Verify(tok); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// Unknown key ids cannot force a JWKS refetch per request.
func TestOIDCVerifierThrottlesUnknownKidRefetch(t *testing.T) {
	idp := newFakeIdP(t)
	v := NewOIDCVerifier(idp.srv.URL, "bnbloc")
	if _, err := v.Verify(idp.token(t, "RS256", "k1", idp.claims(nil))); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 20; i++ {
		_, _ = v.Verify(idp.token(t, "RS256", "rotated", idp.claims(nil)))
	}
	if n := idp.jwksFetch.Load(); n != 1 {
		t.Fatalf("jwks fetched %d times, want 1 (unknown kid refetch throttled)", n)
	}
	// After the throttle window a rotation is picked up.
	now := time.Now().Add(2 * minKeyRefresh)
	v.Now = func() time.Time { return now }
	_, _ = v.Verify(idp.token(t, "RS256", "rotated", idp.claims(map[string]any{"exp": now.Add(time.Hour).Unix()})))
	if n := idp.jwksFetch.Load(); n != 2 {
		t.Fatalf("jwks fetched %d times after the window, want 2", n)
	}
}
