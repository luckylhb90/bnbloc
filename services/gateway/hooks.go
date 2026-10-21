package gateway

import (
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/schedule"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A task's webhook (docs/prd/2026-10-21-room-events.md §2.3): an outside
// system posts to POST /v1/hooks/{hookId}, an open route whose credential
// is the request's signature. The gateway only bounds it (the client IP's
// public budget, the body at schedule.MaxHookBodyBytes and one byte more,
// so realtime sees and counts an oversized one) and proxies it as sent to
// realtime's POST /internal/v1/hooks/{hookId}, which owns the hook: it
// verifies the signature, counts refusals, applies the task's rate limit
// and dispatches the event (services/realtime/hooks.go).

// hookForwardHeaders are the request headers a webhook call carries to
// realtime: its body's type, its signature, its event type and delivery id.
var hookForwardHeaders = []string{"Content-Type", wire.HookSignatureHeader, wire.GitHubSignatureHeader,
	wire.HookEventHeader, wire.GitHubEventHeader, wire.HookDeliveryHeader, wire.GitHubDeliveryHeader}

// handleHook proxies one webhook request to realtime.
func (s *Service) handleHook(w http.ResponseWriter, r *http.Request) {
	base := strings.TrimRight(s.Backends.Realtime, "/")
	if base == "" {
		httpx.Unavailable(w, "webhooks")
		return
	}
	target, err := url.Parse(base + "/internal/v1/hooks/" + url.PathEscape(r.PathValue("hookId")))
	if err != nil {
		obs.ServerError(w, "webhook target failed", err)
		httpx.Error(w, "webhooks unavailable", http.StatusInternalServerError)
		return
	}
	r.Body = io.NopCloser(io.LimitReader(r.Body, schedule.MaxHookBodyBytes+1))
	r.ContentLength = -1
	proxy := &httputil.ReverseProxy{
		Rewrite: func(pr *httputil.ProxyRequest) {
			pr.Out.URL, pr.Out.Host = target, ""
			pr.Out.Header = http.Header{}
			for _, h := range hookForwardHeaders {
				if v := pr.In.Header.Get(h); v != "" {
					pr.Out.Header.Set(h, v)
				}
			}
			svcauth.Key(svcauth.KeyFromEnv()).Assert(pr.Out, gatewayServiceSubject, "", "")
		},
		ErrorHandler: obs.ProxyError("webhook proxy failed"),
	}
	proxy.ServeHTTP(w, r)
}
