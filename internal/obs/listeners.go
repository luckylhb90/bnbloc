package obs

import (
	"net/http"
	"strings"
)

// MetricsAddrEnv names the internal listen address for /metrics. When set,
// a service serves /metrics ONLY there (never on its main listener). When
// unset, internal services keep /metrics on their main listener (reachable
// only on the private service network), while a public listener (the
// gateway) serves no metrics at all.
const MetricsAddrEnv = "BNBLOC_METRICS_ADDR"

// ServeOption adjusts how a service's listeners are built.
type ServeOption func(*listenerPlan)

// PublicListener marks the main listener as internet-facing: /metrics is
// never served on it (404), whatever the configuration.
func PublicListener() ServeOption {
	return func(p *listenerPlan) { p.public = true }
}

// WithMetrics serves m (instead of a fresh registry) at /metrics, so the
// domain series a service records (Metrics.CounterInc, GaugeSet, …) are
// exposed beside its request metrics.
func WithMetrics(m *Metrics) ServeOption {
	return func(p *listenerPlan) { p.metrics = m }
}

// listenerPlan decides where /metrics is served.
type listenerPlan struct {
	public        bool
	metricsAddr   string
	metricsOnMain bool
	// metrics, when set (WithMetrics), replaces the registry serve was given.
	metrics *Metrics
}

func planListeners(mainAddr, metricsAddr string, opts []ServeOption) listenerPlan {
	p := listenerPlan{metricsAddr: strings.TrimSpace(metricsAddr)}
	for _, o := range opts {
		o(&p)
	}
	p.metricsOnMain = !p.public && p.metricsAddr == ""
	return p
}

// withoutMetrics answers /metrics with 404 in front of h, so a public
// listener's catch-all (the gateway's SPA) can never serve it either.
func withoutMetrics(h http.Handler) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/metrics", http.NotFoundHandler())
	mux.Handle("/", h)
	return mux
}

// metricsMux is the internal metrics listener's handler: /metrics only.
func metricsMux(m *Metrics) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/metrics", m.Handler())
	return mux
}
