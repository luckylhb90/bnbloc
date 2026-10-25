package store

import (
	"context"
	"sync"
)

// MemoryPulseMinutes is the in-memory rollup store (the in-memory backends,
// and the services' tests). It keeps the same contract as the MySQL store.
type MemoryPulseMinutes struct {
	mu      sync.Mutex
	sums    map[pulseKey]float64
	updated map[string]int64 // tenant -> last write (unix ms)
}

type pulseKey struct {
	tenant string
	minute int64
	metric string
	dim    string
}

// NewMemoryPulseMinutes returns an empty in-memory rollup store.
func NewMemoryPulseMinutes() *MemoryPulseMinutes {
	return &MemoryPulseMinutes{sums: map[pulseKey]float64{}, updated: map[string]int64{}}
}

func (m *MemoryPulseMinutes) AddPulseMinutes(_ context.Context, rows []PulseMinuteDelta, nowMs int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, r := range rows {
		m.sums[pulseKey{r.Tenant, r.MinuteMs, r.Metric, r.Dim}] += r.Value
		m.updated[r.Tenant] = nowMs
	}
	return nil
}

func (m *MemoryPulseMinutes) PulseTotals(_ context.Context, tenant string, fromMs, toMs int64, metrics []string) (map[string]float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]float64{}
	for k, v := range m.sums {
		if k.tenant == tenant && k.minute >= fromMs && k.minute < toMs && pulseMetricIn(metrics, k.metric) {
			out[k.metric] += v
		}
	}
	return out, nil
}

func (m *MemoryPulseMinutes) PulseBreakdown(_ context.Context, tenant string, fromMs, toMs int64, metrics []string) (map[string]map[string]float64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := map[string]map[string]float64{}
	for k, v := range m.sums {
		if k.tenant == tenant && k.minute >= fromMs && k.minute < toMs && pulseMetricIn(metrics, k.metric) {
			if out[k.metric] == nil {
				out[k.metric] = map[string]float64{}
			}
			out[k.metric][k.dim] += v
		}
	}
	return out, nil
}

func (m *MemoryPulseMinutes) PulseFreshness(_ context.Context, tenant string) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.updated[tenant], nil
}

func (m *MemoryPulseMinutes) PrunePulseMinutes(_ context.Context, beforeMs int64) (int64, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for k := range m.sums {
		if k.minute < beforeMs {
			delete(m.sums, k)
			n++
		}
	}
	return n, nil
}

func pulseMetricIn(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}
