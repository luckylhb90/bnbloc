package notify

import (
	"context"
	"log/slog"
	"time"

	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
)

// Notification delivery metrics (docs/monitoring.md). Outcome and state
// label values are the fixed sets below.
const (
	metricDeliveries   = "notification_deliveries_total"
	metricOutboxItems  = "notification_outbox_items"
	metricOutboxOldest = "notification_outbox_oldest_pending_age_seconds"
)

// outboxStatsInterval is how often the worker refreshes the outbox snapshot
// gauges (one aggregate query over the shared table).
const outboxStatsInterval = 30 * time.Second

// RegisterMetrics describes the notification series and registers every
// label value at zero, so rate() and absence-based alerts see a series
// before the first delivery.
func RegisterMetrics(m *obs.Metrics) {
	if m == nil {
		return
	}
	m.Describe(metricDeliveries, "Notification delivery attempts by outcome (delivered, retry, failed).")
	m.Describe(metricOutboxItems, "Notification outbox items by state (queued, retrying, delivered, failed), every tenant.")
	m.Describe(metricOutboxOldest, "Age of the oldest pending notification outbox item (0 when none).")
	for _, o := range []store.NotificationOutcome{store.NotificationDelivered, store.NotificationRetry, store.NotificationFailed} {
		m.CounterAdd(metricDeliveries, 0, obs.L("outcome", string(o)))
	}
	for _, s := range []string{"queued", "retrying", "delivered", "failed"} {
		m.GaugeSet(metricOutboxItems, 0, obs.L("state", s))
	}
	m.GaugeSet(metricOutboxOldest, 0)
}

// observeAttempt counts one finished delivery attempt.
func (w *Worker) observeAttempt(outcome store.NotificationOutcome) {
	switch outcome {
	case store.NotificationDelivered, store.NotificationRetry, store.NotificationFailed:
		w.Metrics.CounterInc(metricDeliveries, obs.L("outcome", string(outcome)))
	}
}

// sampleOutbox refreshes the outbox snapshot gauges. A failed read keeps the
// previous values (logged): a stale gauge is better than a false zero.
func (w *Worker) sampleOutbox(ctx context.Context) {
	if w.Metrics == nil {
		return
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	st, err := w.Outbox.NotificationOutboxStats(ctx)
	if err != nil {
		slog.Warn("bnbloc notifications: outbox stats failed", "event", "bnbloc.notify.stats_failed", "error", err)
		return
	}
	w.Metrics.GaugeSet(metricOutboxItems, float64(st.Queued), obs.L("state", "queued"))
	w.Metrics.GaugeSet(metricOutboxItems, float64(st.Retrying), obs.L("state", "retrying"))
	w.Metrics.GaugeSet(metricOutboxItems, float64(st.Delivered), obs.L("state", "delivered"))
	w.Metrics.GaugeSet(metricOutboxItems, float64(st.Failed), obs.L("state", "failed"))
	age := 0.0
	if st.OldestPendingCreatedAtMs > 0 {
		if d := w.now().Sub(time.UnixMilli(st.OldestPendingCreatedAtMs)); d > 0 {
			age = d.Seconds()
		}
	}
	w.Metrics.GaugeSet(metricOutboxOldest, age)
}
