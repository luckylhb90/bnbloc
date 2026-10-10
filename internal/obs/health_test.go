package obs

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The probe routes built by buildServeMux implement the Kubernetes contract:
// /healthz is pure liveness (always 200 while the process is up) and /readyz is
// readiness (200 only when the dependency check passes). A failing dependency
// must fail readiness — never liveness — so a transient outage sheds traffic
// instead of restarting the container.
func TestProbeRoutes(t *testing.T) {
	mux := func(check func() error) http.Handler {
		return buildServeMux(NewLogger("test"), NewMetrics("test"), http.NotFoundHandler(), check)
	}
	get := func(h http.Handler, path string) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		return rec.Code
	}

	// Liveness is unconditional: 200 with no check, with a passing check, and —
	// critically — even when the dependency check is failing.
	for name, check := range map[string]func() error{
		"nil":     nil,
		"passing": func() error { return nil },
		"failing": func() error { return errors.New("db down") },
	} {
		if code := get(mux(check), "/healthz"); code != http.StatusOK {
			t.Fatalf("/healthz with %s check: status %d, want 200 (liveness must not depend on the check)", name, code)
		}
	}

	// Readiness tracks the check.
	if code := get(mux(nil), "/readyz"); code != http.StatusOK {
		t.Fatalf("/readyz nil check: status %d, want 200", code)
	}
	if code := get(mux(func() error { return nil }), "/readyz"); code != http.StatusOK {
		t.Fatalf("/readyz passing check: status %d, want 200", code)
	}
	if code := get(mux(func() error { return errors.New("db down") }), "/readyz"); code != http.StatusServiceUnavailable {
		t.Fatalf("/readyz failing check: status %d, want 503", code)
	}
}
