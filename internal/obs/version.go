package obs

import (
	"net/http"

	"github.com/luckylhb90/bnbloc/internal/buildinfo"
)

// withVersion serves GET /version (buildinfo: service, commit, builtAt,
// instance) in front of h, next to /healthz and outside any auth wrapper:
// it is how the gateway's deployment view samples each replica. Only
// internal listeners serve it; the public gateway reports itself in-process
// at the super-admin GET /v1/platform/versions instead of exposing its
// commit and host name to the internet.
func withVersion(service string, h http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/version", buildinfo.Handler(service))
	mux.Handle("/", h)
	return mux
}
