package pulse

import (
	"context"
	"time"

	"github.com/luckylhb90/bnbloc/internal/obs"
)

// Series of the emitter (docs/monitoring.md). They are how an operator sees the
// events a product service could not send: a Redis outage shows as dropped_*.
const (
	metricEmit   = "pulse_emit_total"
	metricQueued = "pulse_emit_queued"
)

// emitResults are the label values of metricEmit, in the order RunMetrics
// samples them.
var emitResults = []string{"accepted", "flushed", "invalid", "attr_dropped", "dropped_queue", "dropped_sink", "dropped_breaker", "sink_errors"}

// RunMetrics samples the emitter's counters into obs every interval until ctx
// ends. A nil emitter (Pulse off) exports nothing. Counters go out as deltas, so
// a restart never double-counts.
func RunMetrics(ctx context.Context, m *obs.Metrics, e *Emitter, every time.Duration) {
	if m == nil || e == nil {
		return
	}
	m.Describe(metricEmit, "Pulse events by result: accepted (queued for the stream), flushed (written to the stream), invalid (refused by the schema), attr_dropped (attributes removed; the event was accepted), dropped_queue (queue full), dropped_sink (a write failed), dropped_breaker (the breaker was open), sink_errors (failed writes).")
	m.Describe(metricQueued, "Events waiting for the batcher to write them to the stream.")
	for _, r := range emitResults {
		m.CounterAdd(metricEmit, 0, obs.L("result", r))
	}
	m.GaugeSet(metricQueued, 0)

	var last Stats
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		st := e.Stats()
		delta := func(result string, now, before int64) {
			if n := now - before; n > 0 {
				m.CounterAdd(metricEmit, float64(n), obs.L("result", result))
			}
		}
		delta("accepted", st.Accepted, last.Accepted)
		delta("flushed", st.Flushed, last.Flushed)
		delta("invalid", st.Invalid, last.Invalid)
		delta("attr_dropped", st.AttrDropped, last.AttrDropped)
		delta("dropped_queue", st.DroppedQueue, last.DroppedQueue)
		delta("dropped_sink", st.DroppedSink, last.DroppedSink)
		delta("dropped_breaker", st.DroppedBreaker, last.DroppedBreaker)
		delta("sink_errors", st.SinkErrors, last.SinkErrors)
		m.GaugeSet(metricQueued, float64(e.Queued()))
		last = st
	}
}
