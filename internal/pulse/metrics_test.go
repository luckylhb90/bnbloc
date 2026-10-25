package pulse

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/luckylhb90/bnbloc/internal/obs"
)

// The emitter's drops reach the metrics, by result, as counters that match what
// the emitter counted.
func TestRunMetricsExportsTheEmitterCounters(t *testing.T) {
	m := obs.NewMetrics("bnbloc-test")
	e := New(SinkFunc(func(context.Context, []Event) error { return context.Canceled }), Config{QueueSize: 4, BatchSize: 1, FlushInterval: time.Millisecond, BreakerThreshold: 100})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go e.Run(ctx)
	go RunMetrics(ctx, m, e, 5*time.Millisecond)

	ev := Event{ID: "x", Tenant: "t", Kind: KindJobClaimed}.With(Enum("core", "codex"))
	for i := 0; i < 50; i++ {
		e.Emit(ev)
	}
	deadline := time.Now().Add(5 * time.Second)
	for {
		rec := httptest.NewRecorder()
		m.Handler().ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/metrics", nil))
		body := rec.Body.String()
		if strings.Contains(body, `bnbloc_pulse_emit_total{service="bnbloc-test",result="dropped_sink"}`) && strings.Contains(body, `result="dropped_queue"`) {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("emitter series never appeared:\n%s", body)
		}
		time.Sleep(5 * time.Millisecond)
	}
}
