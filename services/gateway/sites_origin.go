package gateway

import (
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The sites origin (docs/prd/2026-10-12-interactive-apps.md §4.7, delivery
// step 6). A site runs sandboxed (an opaque origin, siteCSP) wherever it is
// served; a deployment that sets BNBLOC_SITES_ORIGIN also serves every
// site, its ~app/ API and the SDK from that separate origin and nowhere
// else:
//
//   - on the sites origin only site paths (sitePath) and their CORS
//     preflight are served: no API route, no web client, nothing that takes
//     a session;
//   - on the main origin a site path is a 404;
//   - every site URL the gateway hands out (a member's site ticket, a
//     public link's siteUrl) is on the sites origin (Service.siteURL).
//
// Unset, sites are served from the gateway's own origin as relative paths,
// sandbox-only. The origin needs its own DNS name and TLS certificate, and
// an ingress rule that sends it to the same gateway with its Host header.

// SitesOriginEnv names the deployment's separate origin for sites.
const SitesOriginEnv = "BNBLOC_SITES_ORIGIN"

// ParseSitesOrigin validates BNBLOC_SITES_ORIGIN: empty (no sites origin),
// or a bare https origin (scheme and host, an optional port; no path, query
// or credentials; one trailing slash is the same origin). It answers the
// canonical form (lower case, no default port, no slash). A malformed value
// is a startup error. A value
// on the host of publicURL (BNBLOC_PUBLIC_URL) is no separate origin at all
// (the main origin's cookies would reach it): it means the same as unset —
// sites served in place, sandbox-only — and is logged, never fatal, so a
// deployment without its own sites DNS name yet still starts.
func ParseSitesOrigin(raw, publicURL string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", nil
	}
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		return "", fmt.Errorf("%s: %v", SitesOriginEnv, err)
	case u.Scheme != "https":
		return "", fmt.Errorf("%s must be an https origin (got %q)", SitesOriginEnv, raw)
	case u.Host == "" || u.Hostname() == "" || u.Opaque != "" || u.User != nil:
		return "", fmt.Errorf("%s must name a host (got %q)", SitesOriginEnv, raw)
	case (u.Path != "" && u.Path != "/") || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "":
		return "", fmt.Errorf("%s must be a bare origin: no path, query or fragment (got %q)", SitesOriginEnv, raw)
	}
	host := canonicalHost(u.Host)
	if main := strings.TrimSpace(publicURL); main != "" {
		pu, err := url.Parse(main)
		if err == nil && strings.EqualFold(pu.Hostname(), u.Hostname()) {
			log.Printf("bnbloc-gateway: %s names the main host %s: no separate sites origin; sites are served in place (sandbox-only)", SitesOriginEnv, u.Hostname())
			return "", nil
		}
	}
	return "https://" + host, nil
}

// canonicalHost is a Host value compared by the sites gate: lower case,
// without the https default port.
func canonicalHost(host string) string {
	return strings.TrimSuffix(strings.ToLower(strings.TrimSuffix(host, ".")), ":443")
}

// sitePath reports a path of a site's frame: a member site
// (/v1/sites/{ticket}[/…]) or a public link's site
// (/v1/public/shares/{token}/site[/…]), its files and its ~app/ API alike.
// It is the one answer to "is this a site path" (the sites gate, the frame
// entries' CORS).
func sitePath(p string) bool {
	segs := strings.Split(strings.TrimPrefix(p, "/"), "/")
	switch {
	case len(segs) >= 3 && segs[0] == "v1" && segs[1] == "sites" && segs[2] != "":
		return true
	case len(segs) >= 5 && segs[0] == "v1" && segs[1] == "public" && segs[2] == "shares" && segs[3] != "" && segs[4] == "site":
		return true
	}
	return false
}

// withSitesOrigin is the sites gate: with a sites origin, a request is
// served only when it is a site path on the sites origin or any other path
// on the main origin; everything else is a 404 that says nothing more.
func (s *Service) withSitesOrigin(next http.Handler) http.Handler {
	if s.SitesOrigin == "" {
		return next
	}
	sites := canonicalHost(strings.TrimPrefix(s.SitesOrigin, "https://"))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if (canonicalHost(r.Host) == sites) != sitePath(r.URL.Path) {
			h := w.Header()
			h.Set("Referrer-Policy", "no-referrer")
			h.Set("Cache-Control", "no-store")
			httpx.Error(w, "not found", http.StatusNotFound)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// handleClientConfig is GET /v1/public/client-config: the deployment facts a
// client needs before it signs in (wire.ClientConfig). The desktop shell
// reads it to reach the sites origin.
func (s *Service) handleClientConfig(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Cache-Control", "no-cache")
	httpx.WriteJSON(w, http.StatusOK, wire.ClientConfig{SitesOrigin: s.SitesOrigin})
}
