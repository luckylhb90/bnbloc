package shard

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httputil"
	"time"

	"github.com/luckylhb90/bnbloc/internal/obs"
)

// ForwardedHeader marks a request a replica routed by ownership (a proxied
// request, or a direct peer call): the receiver serves it locally and never
// forwards it again, even when its own view names another owner (the views
// briefly disagree) — one hop, never a loop. Its value is the sender's
// address. The gateway strips it from client requests.
const ForwardedHeader = "X-Bnbloc-Forwarded-By"

// KeyFunc returns a request's ownership key; ok=false serves the request
// locally (it is not key-scoped: store-backed reads, probes, peer-to-peer
// internal calls).
type KeyFunc func(r *http.Request) (key string, ok bool)

type ownerCtxKey struct{}

// Forward routes each keyed request to the replica that owns its key: served
// by next when this replica owns it (or the request was already forwarded),
// otherwise proxied to the owner in one hop. The proxy flushes every write,
// so SSE streams pass through live, and carries WebSocket upgrades (relay).
// Every request header survives the hop unchanged — in particular the
// svcauth-signed X-Bnbloc-User assertion, which the owner verifies itself.
// A nil set returns next (single node).
func Forward(s *Set, key KeyFunc, next http.Handler) http.Handler {
	if s == nil {
		return next
	}
	var serve http.HandlerFunc
	proxyFailed := obs.ProxyError("peer forward failed")
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			owner, _ := pr.In.Context().Value(ownerCtxKey{}).(string)
			pr.Out.URL.Scheme = "http"
			pr.Out.URL.Host = owner
			pr.Out.Host = owner
			pr.SetXForwarded()
			// Rewrite starts from the inbound headers minus hop-by-hop ones;
			// the identity headers ride along untouched.
			pr.Out.Header.Set(ForwardedHeader, s.self)
		},
		Transport:     s.transport,
		FlushInterval: -1,
		// The owner could not be reached at all (it died, or is leaving): the
		// request never left this replica, so once the view has dropped that
		// owner it is served again, by the key's new owner, instead of failing
		// (docs/production-deployment.md#high-availability).
		ErrorHandler: func(w http.ResponseWriter, r *http.Request, err error) {
			rt, _ := r.Context().Value(retryCtxKey{}).(*retryState)
			owner, _ := r.Context().Value(ownerCtxKey{}).(string)
			if rt == nil || rt.tried || !unreached(err) {
				proxyFailed(w, r, err)
				return
			}
			rt.tried = true
			s.Suspect(owner)
			if !s.waitOwnerChange(r.Context(), rt.key, owner, ownerChangeWait) {
				proxyFailed(w, r, err)
				return
			}
			r2 := r.Clone(r.Context())
			r2.Body = rt.body()
			serve(w, r2)
		},
	}
	serve = func(w http.ResponseWriter, r *http.Request) {
		k, ok := key(r)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		owner := s.Owner(k)
		if r.Header.Get(ForwardedHeader) != "" {
			if owner != "" {
				s.mu.RLock()
				m, label := s.metrics, s.label
				s.mu.RUnlock()
				m.CounterInc(metricServedUnowned)
				slog.Debug("bnbloc shard: serving a forwarded request this replica does not own",
					"event", "bnbloc.shard.served_unowned", "service", label, "from", r.Header.Get(ForwardedHeader), "owner", owner)
			}
			next.ServeHTTP(w, r)
			return
		}
		if owner == "" {
			next.ServeHTTP(w, r)
			return
		}
		s.mu.RLock()
		m := s.metrics
		s.mu.RUnlock()
		m.CounterInc(metricForwarded)
		ctx := context.WithValue(r.Context(), ownerCtxKey{}, owner)
		if r.Context().Value(retryCtxKey{}) == nil {
			if rt := newRetryState(r, k); rt != nil {
				ctx = context.WithValue(ctx, retryCtxKey{}, rt)
				r.Body = rt.body()
			}
		}
		proxy.ServeHTTP(w, r.WithContext(ctx))
	}
	return serve
}

// ownerChangeWait bounds how long a request whose owner died waits for the
// view to drop it (downAfter probes, the first one at once: Suspect).
const ownerChangeWait = 3 * time.Second

// maxRetryBody is the largest request body kept to be sent again: a chat
// post, never an upload.
const maxRetryBody = 1 << 20

type retryCtxKey struct{}

// retryState is what a forwarded request needs to be served once more.
type retryState struct {
	key   string
	buf   []byte
	tried bool
}

// newRetryState keeps r's body (when small enough) so r can be sent again;
// nil when it cannot.
func newRetryState(r *http.Request, key string) *retryState {
	rt := &retryState{key: key}
	if r.Body == nil || r.Body == http.NoBody {
		return rt
	}
	if r.ContentLength < 0 || r.ContentLength > maxRetryBody {
		return nil
	}
	buf, err := io.ReadAll(io.LimitReader(r.Body, maxRetryBody+1))
	_ = r.Body.Close()
	if err != nil || len(buf) > maxRetryBody {
		return nil
	}
	rt.buf = buf
	return rt
}

func (rt *retryState) body() io.ReadCloser {
	if rt.buf == nil {
		return http.NoBody
	}
	return io.NopCloser(bytes.NewReader(rt.buf))
}

// unreached reports a failure to connect to the peer: nothing of the
// request reached it.
func unreached(err error) bool {
	var op *net.OpError
	return errors.As(err, &op) && op.Op == "dial"
}

// waitOwnerChange waits until key's owner is no longer was (the view dropped
// it), up to d; false when it did not change.
func (s *Set) waitOwnerChange(ctx context.Context, key, was string, d time.Duration) bool {
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if s.Owner(key) != was {
			return true
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(50 * time.Millisecond):
		}
	}
	return false
}

// PeerRequest builds a request addressed to peer that is already routed by
// ownership (ForwardedHeader set): the internal peer-to-peer calls (a plugin
// delivery handed to a member's owner, a scheduled post handed to a room's
// owner). The caller signs it like any internal call.
func (s *Set) PeerRequest(ctx context.Context, method, peer, path string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, "http://"+peer+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set(ForwardedHeader, s.self)
	return req, nil
}

// Client is an HTTP client for peer calls over the set's transport (no egress
// proxy). Callers bound each call with their context.
func (s *Set) Client() *http.Client {
	return &http.Client{Transport: s.transport}
}
