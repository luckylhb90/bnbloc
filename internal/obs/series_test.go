package obs

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// Labeled counters and gauges render in the Prometheus text format with the
// service label first and the caller's labels sorted by name.
func TestLabeledSeriesExposition(t *testing.T) {
	m := NewMetrics("svc")
	m.Describe("events_total", "Events by kind.")
	m.CounterInc("events_total", L("kind", "chat_message"))
	m.CounterInc("events_total", L("kind", "chat_message"))
	m.CounterAdd("events_total", 0, L("kind", "other"))
	m.GaugeAdd("conns", 1, L("kind", "member"))
	m.GaugeAdd("conns", 1, L("kind", "member"))
	m.GaugeAdd("conns", -1, L("kind", "member"))
	m.GaugeSet("age_seconds", 12.5)
	m.GaugeSet("multi", 3, L("z", "1"), L("a", "2"))
	// A name keeps its first type; a mismatched update is dropped.
	m.GaugeSet("events_total", 99, L("kind", "chat_message"))

	rec := httptest.NewRecorder()
	m.Handler()(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()
	for _, want := range []string{
		"# HELP bnbloc_events_total Events by kind.\n",
		"# TYPE bnbloc_events_total counter\n",
		`bnbloc_events_total{service="svc",kind="chat_message"} 2`,
		`bnbloc_events_total{service="svc",kind="other"} 0`,
		"# TYPE bnbloc_conns gauge\n",
		`bnbloc_conns{service="svc",kind="member"} 1`,
		`bnbloc_age_seconds{service="svc"} 12.5`,
		`bnbloc_multi{service="svc",a="2",z="1"} 3`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
}

// Label values are escaped, and a nil registry is a no-op.
func TestLabeledSeriesEscapingAndNil(t *testing.T) {
	m := NewMetrics("svc")
	m.CounterInc("x_total", L("v", "a\"b\\c\nd"))
	rec := httptest.NewRecorder()
	m.Handler()(rec, httptest.NewRequest("GET", "/metrics", nil))
	if want := `bnbloc_x_total{service="svc",v="a\"b\\c\nd"} 1`; !strings.Contains(rec.Body.String(), want) {
		t.Fatalf("missing %q in:\n%s", want, rec.Body.String())
	}
	var nilM *Metrics
	nilM.CounterInc("x_total")
	nilM.GaugeSet("y", 1)
	nilM.GaugeAdd("y", 1)
	nilM.Describe("y", "h")
}

// WithMetrics makes serve expose the caller's registry.
func TestWithMetricsOption(t *testing.T) {
	m := NewMetrics("svc")
	p := planListeners(":0", "", []ServeOption{WithMetrics(m)})
	if p.metrics != m {
		t.Fatal("WithMetrics did not set the plan's registry")
	}
}

// The new platform routes collapse ids like every other collection.
func TestRouteLabelPlatformTokensAndAlerts(t *testing.T) {
	cases := map[string]string{
		"/v1/platform/api-tokens":           "/v1/platform/api-tokens",
		"/v1/platform/api-tokens/abc123":    "/v1/platform/api-tokens/:id",
		"/v1/platform/api-tokens/api-token": "/v1/platform/api-tokens/:id",
		"/v1/platform/alerts":               "/v1/platform/alerts",
		"/v1/auth/api-token":                "/v1/auth/api-token",
	}
	for in, want := range cases {
		if got := routeLabel(in); got != want {
			t.Errorf("routeLabel(%q) = %q, want %q", in, got, want)
		}
	}
}

// A histogram renders cumulative buckets, the sum and the count; a counter
// update of its name is dropped.
func TestHistogramExposition(t *testing.T) {
	m := NewMetrics("svc")
	m.HistogramObserve("step_seconds", 0.003, L("step", "commit"))
	m.HistogramObserve("step_seconds", 0.2, L("step", "commit"))
	m.HistogramObserve("step_seconds", 30, L("step", "commit"))
	m.CounterInc("step_seconds", L("step", "commit"))
	rec := httptest.NewRecorder()
	m.Handler()(rec, httptest.NewRequest("GET", "/metrics", nil))
	body := rec.Body.String()
	for _, want := range []string{
		"# TYPE bnbloc_step_seconds histogram\n",
		`bnbloc_step_seconds_bucket{service="svc",step="commit",le="0.001"} 0`,
		`bnbloc_step_seconds_bucket{service="svc",step="commit",le="0.005"} 1`,
		`bnbloc_step_seconds_bucket{service="svc",step="commit",le="0.25"} 2`,
		`bnbloc_step_seconds_bucket{service="svc",step="commit",le="10"} 2`,
		`bnbloc_step_seconds_bucket{service="svc",step="commit",le="+Inf"} 3`,
		`bnbloc_step_seconds_sum{service="svc",step="commit"} 30.203`,
		`bnbloc_step_seconds_count{service="svc",step="commit"} 3`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("missing %q in:\n%s", want, body)
		}
	}
}
