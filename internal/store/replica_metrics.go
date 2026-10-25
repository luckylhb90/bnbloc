package store

import (
	"context"
	"time"

	"github.com/luckylhb90/bnbloc/internal/obs"
)

// Replica series (docs/monitoring.md). Per process: each replica of a service
// reads its own pool, so aggregate with sum for the counters and max for the
// gauges' fleet view.
const (
	metricReplicaUsable = "store_replica_usable"
	metricReplicaLag    = "store_replica_lag_seconds"
	metricStoreReads    = "store_reads_total"
)

// RegisterReplicaMetrics describes the replica series and registers them at zero,
// so an alert that reads them sees a sample before the first read.
func RegisterReplicaMetrics(m *obs.Metrics) {
	if m == nil {
		return
	}
	m.Describe(metricReplicaUsable, "1 when this process may send opted-in reads to the read replica (its threads run and its lag is within the bound), 0 when the primary serves them.")
	m.Describe(metricReplicaLag, "Replica apply lag in seconds as the last health check read it (0 when the replica is not reporting).")
	m.Describe(metricStoreReads, "Opted-in read-only queries by where they were served: target=replica, or target=fallback when the replica was configured but not usable.")
	m.GaugeSet(metricReplicaUsable, 0)
	m.GaugeSet(metricReplicaLag, 0)
	m.CounterAdd(metricStoreReads, 0, obs.L("target", "replica"))
	m.CounterAdd(metricStoreReads, 0, obs.L("target", "fallback"))
}

// RunReplicaMetrics samples the router every interval until ctx ends. It does
// nothing when no replica is configured (nil or unconfigured DB), so services
// without a replica carry no replica series beyond what they already expose.
func RunReplicaMetrics(ctx context.Context, m *obs.Metrics, d *DB, interval time.Duration) {
	if m == nil || d == nil || !d.Stats().Configured {
		return
	}
	RegisterReplicaMetrics(m)
	var lastReplica, lastFallback int64
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		st := d.Stats()
		usable := 0.0
		if st.Usable {
			usable = 1
		}
		m.GaugeSet(metricReplicaUsable, usable)
		m.GaugeSet(metricReplicaLag, st.Lag.Seconds())
		if n := st.ReplicaReads - lastReplica; n > 0 {
			m.CounterAdd(metricStoreReads, float64(n), obs.L("target", "replica"))
		}
		if n := st.FallbackReads - lastFallback; n > 0 {
			m.CounterAdd(metricStoreReads, float64(n), obs.L("target", "fallback"))
		}
		lastReplica, lastFallback = st.ReplicaReads, st.FallbackReads
	}
}
