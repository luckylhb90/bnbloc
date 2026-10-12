// Package svcauth authenticates the internal identity assertion that passes
// between bnbloc services as the X-Bnbloc-User header.
//
// The gateway is the only trust boundary that mints X-Bnbloc-User from a
// verified session. Internal services historically trusted that header
// unconditionally, so any workload able to reach a service port on the internal
// network (e.g. room:8080) could spoof any user, including a room owner (SEC8).
//
// svcauth closes that gap with a shared-secret HMAC: a signer (the gateway and
// any internal caller such as the membership probe or the bridge) signs
// HMAC-SHA256(key, ts + "\n" + user) into X-bnbloc-Sig with the timestamp in
// X-bnbloc-Ts. The Middleware verifies the signature and a freshness window
// before the request reaches a handler, so an unsigned or forged X-Bnbloc-User
// is rejected.
//
// Key distribution is via BNBLOC_INTERNAL_KEY. When the key is unset the
// middleware runs in dev-permissive mode (a one-time loud warning, requests pass
// through) so local single-host development keeps working; production
// (BNBLOC_ENV=production) refuses to start without it.
package svcauth

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/luckylhb90/bnbloc/internal/config"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

const (
	// HeaderUser carries the asserted identity (already used repo-wide).
	HeaderUser = "X-Bnbloc-User"
	// HeaderTenant carries the asserted tenant (organization) scope. Like the
	// user, it is covered by the signature so an internal workload cannot forge
	// a cross-tenant assertion.
	HeaderTenant = "X-Bnbloc-Tenant"
	// HeaderSig carries the hex HMAC over the timestamp, user, and tenant.
	HeaderSig = "X-bnbloc-Sig"
	// HeaderTs carries the unix-milliseconds timestamp the signature covers.
	HeaderTs = "X-bnbloc-Ts"

	// freshnessWindowMs bounds clock skew and replay: a signature older or newer
	// than this is rejected.
	freshnessWindowMs = 5 * 60 * 1000
)

// KeyFromEnv returns the configured internal signing key, empty when unset.
func KeyFromEnv() string { return strings.TrimSpace(os.Getenv("BNBLOC_INTERNAL_KEY")) }

// ErrKeyRequired reports a missing internal key in production mode.
var ErrKeyRequired = errors.New("svcauth: BNBLOC_INTERNAL_KEY is required when BNBLOC_ENV=production")

// CheckKey returns ErrKeyRequired when key is empty in production mode. The
// dev-permissive (unverified) mode is only reachable outside production.
func CheckKey(key string) error {
	if key == "" && config.Production() {
		return ErrKeyRequired
	}
	return nil
}

// servicePrefix namespaces the identity a control-plane service asserts when it
// calls another service on its own behalf (not for a user).
const servicePrefix = "service:"

// ServiceSubject is the X-Bnbloc-User a control-plane service asserts for its
// own internal calls (e.g. activity publishing into realtime), so audit logs
// and handlers can tell a service call from a user-forwarded one.
func ServiceSubject(name string) string { return servicePrefix + name }

// compute returns the hex HMAC-SHA256 over "<ts>\n<user>\n<tenant>". Tenant is
// included so the assertion binds identity AND organization; a legacy empty
// tenant hashes as "" and stays compatible with single-tenant callers.
func compute(key string, tsMs int64, user, tenant string) string {
	mac := hmac.New(sha256.New, []byte(key))
	mac.Write([]byte(strconv.FormatInt(tsMs, 10)))
	mac.Write([]byte("\n"))
	mac.Write([]byte(user))
	mac.Write([]byte("\n"))
	mac.Write([]byte(tenant))
	return hex.EncodeToString(mac.Sum(nil))
}

// Sign stamps req with a fresh internal identity assertion for user. It is a
// no-op when key is empty (dev mode), so callers can pass KeyFromEnv()
// unconditionally. Any caller that also sets X-Bnbloc-User MUST call Sign with
// the same user so the pair is consistent.
func Sign(req *http.Request, key, user string) {
	if key == "" {
		return
	}
	ts := time.Now().UnixMilli()
	tenant := strings.TrimSpace(req.Header.Get(HeaderTenant))
	req.Header.Set(HeaderTs, strconv.FormatInt(ts, 10))
	req.Header.Set(HeaderSig, compute(key, ts, user, tenant))
}

var warnOnce sync.Once

// Middleware verifies the internal identity assertion on every request before
// it reaches next. With no key configured it logs one warning and passes
// through (dev mode). With a key configured it requires a fresh, valid signature
// over the X-Bnbloc-User value; otherwise it responds 401 and does not call
// next. An empty X-Bnbloc-User is always allowed through (unauthenticated
// callers carry no identity to forge; handlers needing one reject it).
//
// Middleware is a composition-root constructor: in production mode
// (BNBLOC_ENV=production) a missing key is fatal at startup instead of
// silently selecting the unverified dev mode.
func Middleware(key string, next http.Handler) http.Handler {
	if err := CheckKey(key); err != nil {
		log.Fatal(err)
	}
	if key == "" {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			warnOnce.Do(func() {
				log.Printf("svcauth: BNBLOC_INTERNAL_KEY unset; internal X-Bnbloc-User is UNVERIFIED (dev mode). Set it in shared deployments.")
			})
			next.ServeHTTP(w, injectTenant(r))
		})
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := strings.TrimSpace(r.Header.Get(HeaderUser))
		if user == "" {
			// No identity asserted: nothing to forge. Downstream handlers that
			// require identity reject empty callers themselves. Still inject any
			// tenant so unauthenticated-but-tenanted paths keep a scope.
			next.ServeHTTP(w, injectTenant(r))
			return
		}
		if !verify(key, r, user) {
			httpx.Error(w, "invalid internal identity signature", http.StatusUnauthorized)
			return
		}
		// Signature covers the tenant header, so it is now trusted: bind it to
		// the request context where the store layer reads it for scoping.
		next.ServeHTTP(w, injectTenant(r))
	})
}

// RequireSigned guards internal-only routes (/internal/*): the caller must
// assert an identity in X-Bnbloc-User, and when key is configured that
// assertion must carry a fresh valid signature. It is self-contained (it does
// not rely on Middleware being mounted in front), so an internal endpoint can
// never be reached anonymously: in a keyed deployment only a holder of
// BNBLOC_INTERNAL_KEY can call it. With no key (dev) the identity is asserted
// but unverified, matching Middleware's dev mode.
func RequireSigned(key string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user := strings.TrimSpace(r.Header.Get(HeaderUser))
		if user == "" {
			httpx.Error(w, "internal endpoint requires a service identity", http.StatusUnauthorized)
			return
		}
		if key != "" && !verify(key, r, user) {
			httpx.Error(w, "invalid internal identity signature", http.StatusUnauthorized)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// injectTenant binds the request's tenant assertion into the context so the
// store layer (tenant.Require) can enforce it without every handler re-reading
// the header. Defaults to the default tenant when none is asserted, so a
// single-tenant / dev deployment always has a scope and never fails closed.
func injectTenant(r *http.Request) *http.Request {
	t := strings.TrimSpace(r.Header.Get(HeaderTenant))
	if t == "" {
		t = tenant.Default
	}
	return r.WithContext(tenant.NewContext(r.Context(), t))
}

// EnsureTenant wraps a service handler so a request always carries a tenant in
// its context, even when the service is mounted without the gateway's
// Middleware in front (single-process embedding, tests, health probes). It only
// fills the DEFAULT tenant when none is present; when Middleware ran first and
// bound the verified tenant, that value is kept. This makes each service
// self-contained (never accidentally unscoped) without masking a real
// cross-tenant mistake, since the verified value always wins.
func EnsureTenant(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if tenant.FromContext(r.Context()) != "" {
			next.ServeHTTP(w, r)
			return
		}
		next.ServeHTTP(w, injectTenant(r))
	})
}

// verify checks the signature and freshness for the asserted user + tenant.
func verify(key string, r *http.Request, user string) bool {
	tsRaw := strings.TrimSpace(r.Header.Get(HeaderTs))
	sig := strings.TrimSpace(r.Header.Get(HeaderSig))
	if tsRaw == "" || sig == "" {
		return false
	}
	tsMs, err := strconv.ParseInt(tsRaw, 10, 64)
	if err != nil {
		return false
	}
	now := time.Now().UnixMilli()
	if delta := now - tsMs; delta > freshnessWindowMs || delta < -freshnessWindowMs {
		return false
	}
	tenant := strings.TrimSpace(r.Header.Get(HeaderTenant))
	want := compute(key, tsMs, user, tenant)
	return hmac.Equal([]byte(want), []byte(sig))
}

// Tenant returns the verified tenant asserted on the request (empty if none).
// Handlers call this after Middleware has validated the signature.
func Tenant(r *http.Request) string {
	return strings.TrimSpace(r.Header.Get(HeaderTenant))
}
