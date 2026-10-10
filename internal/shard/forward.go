package shard

import (
	"context"
	"io"
	"log/slog"
	"net/http"
	"net/http/httputil"

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
		ErrorHandler:  obs.ProxyError("peer forward failed"),
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
		proxy.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ownerCtxKey{}, owner)))
	})
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
