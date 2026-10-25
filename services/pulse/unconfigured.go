package pulsesvc

import (
	"net/http"

	"github.com/luckylhb90/bnbloc/internal/httpx"
)

// NotConfigured answers the board's routes when no Redis is configured
// (BNBLOC_PULSE_REDIS_URL unset): the process stays up so an all-in-one
// container keeps its supervised services, but there is no stream and no live
// state to read. A 503 with the reason is a clear answer; a crash would take
// the container down with it.
func NotConfigured() http.Handler {
	mux := http.NewServeMux()
	unavailable := func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Retry-After", "600")
		httpx.Error(w, "the organization board is not configured on this deployment", http.StatusServiceUnavailable)
	}
	mux.HandleFunc("GET /v1/tenant/pulse/snapshot", unavailable)
	mux.HandleFunc("GET /v1/tenant/pulse/stream", unavailable)
	return mux
}
