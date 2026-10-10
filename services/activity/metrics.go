package activity

import (
	"context"
	"log/slog"
	"time"

	"github.com/luckylhb90/bnbloc/internal/obs"
)

// Bridge metrics (docs/monitoring.md).
const (
	metricBridgeHeartbeats = "bridge_heartbeats_total"
	metricBridgesOnline    = "bridges_online"
)

// RegisterMetrics describes the bridge series and registers them at zero.
func RegisterMetrics(m *obs.Metrics) {
	if m == nil {
		return
	}
	m.Describe(metricBridgeHeartbeats, "Bridge heartbeats accepted by this replica.")
	m.Describe(metricBridgesOnline, "Bridges (every tenant) whose last heartbeat is within the offline window; read from the shared store, so every replica reports the same value.")
	m.CounterAdd(metricBridgeHeartbeats, 0)
	m.GaugeSet(metricBridgesOnline, 0)
}

// SampleBridgeMetrics refreshes the online-bridges gauge from the shared
// store. A failed read keeps the previous value (logged).
func (s *Service) SampleBridgeMetrics(ctx context.Context) {
	if s.Metrics == nil || s.BridgeJobs == nil {
		return
	}
	now := time.Now()
	if s.Now != nil {
		now = s.Now()
	}
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	n, err := s.BridgeJobs.CountOnlineBridges(ctx, now.UnixMilli())
	if err != nil {
		slog.Warn("bnbloc activity: online bridge count failed", "event", "bnbloc.activity.bridge_stats_failed", "error", err)
		return
	}
	s.Metrics.GaugeSet(metricBridgesOnline, float64(n))
}
