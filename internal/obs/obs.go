// Package obs provides structured request logging and a separate audit sink.
// Log lines are tagged with (sub, roomId) and must never contain
// secrets, host paths, or ICE candidates (SEC-8).
package obs

import (
	"bufio"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"regexp"
	"time"

	"github.com/luckylhb90/bnbloc/internal/sharelink"
)

var roomPathRe = regexp.MustCompile(`/v1/rooms/([^/]+)`)

// secretPathRes match path segments that carry secrets: the invite
// redemption code, the relay stream session key, and the capability tokens
// of the frame and link routes (a member's site ticket, which an
// interactive app's data calls carry too; a public share token; a
// previewer's or a settings page's frame ticket). They are redacted before a path is logged so request
// logs never leak them (SEC-9).
var secretPathRes = []*regexp.Regexp{
	regexp.MustCompile(`(/v1/invites/)[^/]+(/redeem)`),
	regexp.MustCompile(`(/relay/stream/)[^/]+()`),
	regexp.MustCompile(`(/v1/sites/)[^/]+()`),
	regexp.MustCompile(`(/v1/public/shares/)[^/]+()`),
	regexp.MustCompile(`^(` + regexp.QuoteMeta(sharelink.Prefix) + `)[^/]+()`),
	regexp.MustCompile(`(/v1/previewers/[^/]+/[^/]+/)[^/]+()`),
	regexp.MustCompile(`(/v1/settings-pages/[^/]+/[^/]+/[^/]+/)[^/]+()`),
}

// redactPath replaces secret path segments with [REDACTED] for logging. It does
// not touch room ids (logged separately) or the metrics route label, which
// already collapses ids into stable buckets.
func redactPath(path string) string {
	for _, re := range secretPathRes {
		path = re.ReplaceAllString(path, "${1}[REDACTED]${2}")
	}
	return path
}

// NewLogger constructs a JSON structured logger for a service.
func NewLogger(service string) *slog.Logger {
	return slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})).
		With("service", service)
}

// Middleware logs each request with request-scoped tags. It records only
// method, path, room scope, subject, status, and duration — never
// bodies, headers with secrets, or candidates.
func Middleware(logger *slog.Logger, metrics *Metrics, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		rec := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(rec, r)

		elapsed := time.Since(start)
		roomID := ""
		if m := roomPathRe.FindStringSubmatch(r.URL.Path); m != nil {
			roomID = m[1]
		}
		attrs := []any{
			"method", r.Method,
			"path", redactPath(r.URL.Path),
			"roomId", roomID,
			"sub", r.Header.Get("X-Bnbloc-User"),
			"status", rec.status,
			"durationMs", elapsed.Milliseconds(),
		}
		if rec.status >= http.StatusInternalServerError {
			// Every 5xx is an Error line with its cause (ServerError); a 5xx
			// with no recorded cause is itself the signal of a silent path.
			what := rec.what
			if what == "" {
				what = "unrecorded"
			}
			attrs = append(attrs, "event", ServerErrorEvent, "what", what)
			if rec.cause != nil {
				attrs = append(attrs, "error", rec.cause.Error())
			}
			logger.Error("request", attrs...)
		} else {
			logger.Info("request", attrs...)
		}
		metrics.observeRequest(methodLabel(r.Method), routeLabel(r.URL.Path), rec.status,
			float64(elapsed.Microseconds())/1000.0)
	})
}

type statusRecorder struct {
	http.ResponseWriter
	status int
	// what and cause are the 5xx cause recorded by ServerError.
	what  string
	cause error
}

// Unwrap exposes the underlying writer (http.ResponseController).
func (r *statusRecorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (r *statusRecorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

// Flusher forwarding so SSE/streaming still works behind the middleware.
func (r *statusRecorder) Flush() {
	if f, ok := r.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Hijack forwarding so a WebSocket upgrade (the relay byte-stream) can take
// over the connection behind the middleware. Without it a hijacking handler
// (e.g. coder/websocket Accept) fails its http.Hijacker assertion and the
// upgrade returns 501.
func (r *statusRecorder) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	if h, ok := r.ResponseWriter.(http.Hijacker); ok {
		return h.Hijack()
	}
	return nil, nil, http.ErrNotSupported
}

// Serve wraps a handler with logging + metrics middleware, exposes /metrics,
// and starts an HTTP server. Every bnbloc service main uses this so request
// logging and metrics are uniform.
func Serve(service, addr string, handler http.Handler) error {
	return ServeWithMetrics(service, addr, handler, NewMetrics(service))
}

// ServeWithMetrics is Serve with a caller-provided Metrics, so a service can
// record domain counters (borrow allow/deny, rate-limit hits) into the same
// registry that /metrics exposes.
func ServeWithMetrics(service, addr string, handler http.Handler, metrics *Metrics) error {
	return serve(service, addr, handler, metrics, nil)
}

// ServeWithHealth is Serve plus a readiness checker exposed at /readyz: it
// returns 503 when check returns an error (e.g. the database is unreachable),
// 200 otherwise. /healthz stays a pure liveness probe (always 200 while the
// process is up). Keeping them separate is the Kubernetes contract: a failing
// readiness probe removes the pod from Service endpoints (shed traffic) while a
// failing liveness probe restarts the container — a transient DB blip must do
// the former, never the latter.
func ServeWithHealth(service, addr string, handler http.Handler, check func() error, opts ...ServeOption) error {
	return serve(service, addr, handler, NewMetrics(service), check, opts...)
}

func serve(service, addr string, handler http.Handler, metrics *Metrics, check func() error, opts ...ServeOption) error {
	logger := NewLogger(service)
	plan := planListeners(addr, os.Getenv(MetricsAddrEnv), opts)
	if plan.metrics != nil {
		metrics = plan.metrics
	}
	if plan.sameAsMain {
		if plan.public {
			return fmt.Errorf("%s=%s is the public listener's address, which never serves /metrics; set an internal address or leave it unset", MetricsAddrEnv, plan.metricsAddr)
		}
		logger.Warn(MetricsAddrEnv + " is the main listener's address: /metrics is served there, as when it is unset")
	}
	logger.Info("listening", "addr", addr, "metricsAddr", plan.metricsAddr, "metricsOnMain", plan.metricsOnMain)
	if plan.public && plan.metricsAddr == "" {
		logger.Warn("metrics disabled: the public listener never serves /metrics; set " + MetricsAddrEnv + " to an internal address to expose them")
	}
	main := buildServeMux(logger, metrics, handler, check)
	if !plan.metricsOnMain {
		main = withoutMetrics(main)
	}
	profiler := os.Getenv(ProfilerEnv) == "1"
	if profiler && plan.metricsOnMain {
		main = withProfiler(main)
	}
	if profiler && plan.public && plan.metricsAddr == "" {
		logger.Warn("profiler off: the public listener never serves it; set " + MetricsAddrEnv + " to an internal address")
	}
	if !plan.public {
		main = withVersion(service, main)
	}
	errc := make(chan error, 2)
	if plan.metricsAddr != "" {
		go func() {
			err := newServer(plan.metricsAddr, metricsMux(metrics, profiler)).ListenAndServe()
			errc <- fmt.Errorf("metrics listener %s: %w", plan.metricsAddr, err)
		}()
	}
	go func() { errc <- newServer(addr, main).ListenAndServe() }()
	return <-errc
}

// Server timeouts. ReadHeaderTimeout bounds a slow-loris client that trickles
// headers; IdleTimeout reclaims idle keep-alive connections. Whole-request
// read/write timeouts are deliberately unset: SSE streams and 256 MiB artifact
// uploads are legitimately long-lived, and handlers bound their own bodies.
const (
	readHeaderTimeout = 10 * time.Second
	idleTimeout       = 2 * time.Minute
)

// newServer builds the HTTP server every bnbloc service listens with.
func newServer(addr string, h http.Handler) *http.Server {
	return &http.Server{Addr: addr, Handler: h, ReadHeaderTimeout: readHeaderTimeout, IdleTimeout: idleTimeout}
}

// buildServeMux assembles the observability + probe routes shared by every
// service: /metrics, a pure-liveness /healthz, a dependency-gated /readyz, and
// the wrapped application handler. Extracted from serve so the probe semantics
// are testable without binding a listener.
func buildServeMux(logger *slog.Logger, metrics *Metrics, handler http.Handler, check func() error) http.Handler {
	mux := http.NewServeMux()
	mux.Handle("/metrics", metrics.Handler())
	// Liveness: the process is up and serving. Never gated on dependencies, so a
	// dependency outage cannot trigger a restart loop.
	mux.HandleFunc("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	// Readiness: safe to receive traffic. Gated on the dependency check when one
	// is supplied; with no check it mirrors liveness (always ready).
	mux.HandleFunc("/readyz", func(w http.ResponseWriter, _ *http.Request) {
		if check != nil {
			if err := check(); err != nil {
				http.Error(w, "not ready", http.StatusServiceUnavailable)
				return
			}
		}
		w.WriteHeader(http.StatusOK)
	})
	mux.Handle("/", Middleware(logger, metrics, handler))
	return mux
}
