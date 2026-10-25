package pulsesvc

import (
	"context"
	"time"

	"github.com/luckylhb90/bnbloc/internal/obs"
)

// Series of the consumer (docs/monitoring.md).
const (
	metricEvents   = "pulse_events_total"
	metricFlushes  = "pulse_flush_total"
	metricPending  = "pulse_pending"
	metricLiveErrs = "pulse_live_errors_total"
)

// RunMetrics samples the consumer's counters into obs every interval until ctx
// ends. Counters are sent as deltas, so a restart never double-counts.
func RunMetrics(ctx context.Context, m *obs.Metrics, c *Consumer, every time.Duration) {
	m.Describe(metricEvents, "Pulse stream entries by result: applied (counted), duplicate (a redelivery, skipped), invalid (refused by the schema, dropped).")
	m.Describe(metricFlushes, "Rollup flushes by result: ok (committed and acked), error (kept for retry).")
	m.Describe(metricPending, "Stream messages read but not yet acked (waiting for the next flush).")
	m.Describe(metricLiveErrs, "Live-state writes that failed (best effort, self-healing).")
	for _, r := range []string{"applied", "duplicate", "invalid"} {
		m.CounterAdd(metricEvents, 0, obs.L("result", r))
	}
	m.CounterAdd(metricFlushes, 0, obs.L("result", "ok"))
	m.CounterAdd(metricFlushes, 0, obs.L("result", "error"))
	m.GaugeSet(metricPending, 0)
	m.CounterAdd(metricLiveErrs, 0)

	st := c.Stats()
	var applied, dup, invalid, ok, failed, liveErrs int64
	t := time.NewTicker(every)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		if n := st.Applied.Load() - applied; n > 0 {
			m.CounterAdd(metricEvents, float64(n), obs.L("result", "applied"))
		}
		if n := st.Duplicates.Load() - dup; n > 0 {
			m.CounterAdd(metricEvents, float64(n), obs.L("result", "duplicate"))
		}
		if n := st.Invalid.Load() - invalid; n > 0 {
			m.CounterAdd(metricEvents, float64(n), obs.L("result", "invalid"))
		}
		if n := st.Flushed.Load() - ok; n > 0 {
			m.CounterAdd(metricFlushes, float64(n), obs.L("result", "ok"))
		}
		if n := st.FlushErrors.Load() - failed; n > 0 {
			m.CounterAdd(metricFlushes, float64(n), obs.L("result", "error"))
		}
		if n := st.LiveErrors.Load() - liveErrs; n > 0 {
			m.CounterAdd(metricLiveErrs, float64(n))
		}
		m.GaugeSet(metricPending, float64(st.Pending.Load()))
		applied, dup, invalid = st.Applied.Load(), st.Duplicates.Load(), st.Invalid.Load()
		ok, failed, liveErrs = st.Flushed.Load(), st.FlushErrors.Load(), st.LiveErrors.Load()
	}
}
