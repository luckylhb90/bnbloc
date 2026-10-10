package obs

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// H5: where /metrics is served. Internal services without
// BNBLOC_METRICS_ADDR keep it on the main listener; a configured metrics
// address moves it there; a public listener never serves it.
func TestPlanListeners(t *testing.T) {
	cases := []struct {
		name         string
		metricsAddr  string
		opts         []ServeOption
		onMain       bool
		separateAddr string
	}{
		{"internal default", "", nil, true, ""},
		{"internal with metrics addr", ":9090", nil, false, ":9090"},
		{"public default", "", []ServeOption{PublicListener()}, false, ""},
		{"public with metrics addr", " :9090 ", []ServeOption{PublicListener()}, false, ":9090"},
	}
	for _, c := range cases {
		p := planListeners(":8080", c.metricsAddr, c.opts)
		if p.metricsOnMain != c.onMain || p.metricsAddr != c.separateAddr {
			t.Errorf("%s: plan = %+v; want onMain=%v addr=%q", c.name, p, c.onMain, c.separateAddr)
		}
	}
}

// A public main listener answers /metrics with 404 even when its catch-all
// handler (the gateway SPA) would answer every path, while probes and the
// app keep working; the internal metrics mux serves only /metrics.
func TestPublicListenerHidesMetrics(t *testing.T) {
	spa := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte("<html>")) })
	m := NewMetrics("gw")
	main := withoutMetrics(buildServeMux(NewLogger("test"), m, spa, nil))
	get := func(h http.Handler, path string) int {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		return rec.Code
	}
	if code := get(main, "/metrics"); code != http.StatusNotFound {
		t.Fatalf("public /metrics = %d, want 404", code)
	}
	for _, p := range []string{"/healthz", "/readyz", "/rooms/abc"} {
		if code := get(main, p); code != http.StatusOK {
			t.Fatalf("public %s = %d, want 200", p, code)
		}
	}
	internal := metricsMux(m)
	if code := get(internal, "/metrics"); code != http.StatusOK {
		t.Fatalf("internal /metrics = %d, want 200", code)
	}
	if code := get(internal, "/v1/rooms"); code != http.StatusNotFound {
		t.Fatalf("internal listener served an app path: %d", code)
	}
}
