package search

import (
	"context"
	"log/slog"
	"sync/atomic"
	"time"

	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Metric names (docs/monitoring.md).
const (
	// MetricUpdates counts messages handed to the index updater, by result:
	// ok (applied), error (failed after every attempt), dropped (the queue
	// was full). A nonzero error or dropped rate means the index drifts
	// until `bnbloc search reindex` runs; search stays correct (the store
	// re-checks every candidate) but may miss new or edited messages.
	MetricUpdates = "search_index_updates_total"
	// MetricQueue is the updater's queue length.
	MetricQueue = "search_index_queue_length"
	// MetricStaleCandidates counts search candidates the store rejected
	// (deleted, edited away or gone before the index caught up).
	MetricStaleCandidates = "search_stale_candidates_total"
)

// RegisterMetrics describes the search series and registers them at zero.
func RegisterMetrics(m *obs.Metrics) {
	if m == nil {
		return
	}
	m.Describe(MetricUpdates, "Messages handed to the external search index updater, by result (ok, error, dropped: queue full).")
	m.Describe(MetricQueue, "Messages waiting in the external search index update queue.")
	m.Describe(MetricStaleCandidates, "External search index candidates the store rejected (an index update not yet applied, or lost).")
	for _, r := range []string{"ok", "error", "dropped"} {
		m.CounterAdd(MetricUpdates, 0, obs.L("result", r))
	}
	m.GaugeSet(MetricQueue, 0)
	m.CounterAdd(MetricStaleCandidates, 0)
}

// Updater bounds. They are properties of the updater (memory it may hold,
// work per index call), not of a deployment.
const (
	// updateQueueSize bounds the messages waiting for the index: a full
	// queue drops (counted) instead of blocking the publisher.
	updateQueueSize = 10000
	// updateBatch is the most messages one Apply covers.
	updateBatch = 500
	// updateAttempts and updateBackoff retry a failed batch (an index
	// restart, a network blip) before it counts as failed.
	updateAttempts = 3
	updateBackoff  = time.Second
	// dropLogEvery rate-limits the dropped-update log line.
	dropLogEvery = 10 * time.Second
)

// Updater keeps an external index current asynchronously. Publishers
// enqueue message keys without ever blocking (a full queue drops and
// counts); one worker applies them in batches, re-reading each message from
// the store, so the last write of a message wins regardless of which event
// queued it. A nil *Updater ignores everything (the store index needs no
// updates).
type Updater struct {
	index   Index
	queue   chan store.MessageKey
	metrics *obs.Metrics

	applied atomic.Int64
	failed  atomic.Int64
	dropped atomic.Int64
	lastLog atomic.Int64 // unix nanos of the last dropped-update log line
	// sleep waits between attempts (tests replace it).
	sleep func(context.Context, time.Duration) bool
}

// NewUpdater returns the updater of idx; Run starts its worker.
func NewUpdater(idx Index, metrics *obs.Metrics) *Updater {
	return &Updater{index: idx, queue: make(chan store.MessageKey, updateQueueSize), metrics: metrics, sleep: sleepCtx}
}

// Enqueue queues keys for the index without blocking.
func (u *Updater) Enqueue(keys ...store.MessageKey) {
	if u == nil {
		return
	}
	var dropped int
	for _, k := range keys {
		select {
		case u.queue <- k:
		default:
			dropped++
		}
	}
	u.metrics.GaugeSet(MetricQueue, float64(len(u.queue)))
	if dropped == 0 {
		return
	}
	total := u.dropped.Add(int64(dropped))
	u.metrics.CounterAdd(MetricUpdates, float64(dropped), obs.L("result", "dropped"))
	now := time.Now().UnixNano()
	if last := u.lastLog.Load(); now-last >= int64(dropLogEvery) && u.lastLog.CompareAndSwap(last, now) {
		slog.Warn("bnbloc search: index update queue full; updates dropped (run `bnbloc search reindex` to repair)",
			"event", "bnbloc.search.update_dropped", "dropped", dropped, "dropped_total", total)
	}
}

// Committed is the room group commit's hook (realtime.Hub.Committed): it
// queues every row a commit stored. It never blocks.
func (u *Updater) Committed(rows []store.RoomMessage) {
	if u == nil || len(rows) == 0 {
		return
	}
	keys := make([]store.MessageKey, len(rows))
	for i, r := range rows {
		keys[i] = store.MessageKey{TenantID: r.TenantID, RoomID: r.RoomID, Seq: r.Seq}
	}
	u.Enqueue(keys...)
}

// Stats reports this updater's counters.
func (u *Updater) Stats() *wire.SearchUpdateStats {
	if u == nil {
		return nil
	}
	return &wire.SearchUpdateStats{Queued: len(u.queue), Applied: u.applied.Load(), Failed: u.failed.Load(), Dropped: u.dropped.Load()}
}

// Run applies queued keys until ctx ends.
func (u *Updater) Run(ctx context.Context) {
	if u == nil {
		return
	}
	for {
		var first store.MessageKey
		select {
		case <-ctx.Done():
			return
		case first = <-u.queue:
		}
		batch := []store.MessageKey{first}
		seen := map[store.MessageKey]bool{first: true}
	drain:
		for len(batch) < updateBatch {
			select {
			case k := <-u.queue:
				if !seen[k] {
					seen[k] = true
					batch = append(batch, k)
				}
			default:
				break drain
			}
		}
		u.metrics.GaugeSet(MetricQueue, float64(len(u.queue)))
		u.apply(ctx, batch)
	}
}

// apply writes one batch, retrying a failure a bounded number of times.
func (u *Updater) apply(ctx context.Context, batch []store.MessageKey) {
	var err error
	for attempt := 0; attempt < updateAttempts; attempt++ {
		if attempt > 0 && !u.sleep(ctx, updateBackoff<<(attempt-1)) {
			break
		}
		if err = u.index.Apply(ctx, batch); err == nil {
			u.applied.Add(int64(len(batch)))
			u.metrics.CounterAdd(MetricUpdates, float64(len(batch)), obs.L("result", "ok"))
			return
		}
	}
	u.failed.Add(int64(len(batch)))
	u.metrics.CounterAdd(MetricUpdates, float64(len(batch)), obs.L("result", "error"))
	slog.Error("bnbloc search: index update failed (run `bnbloc search reindex` to repair)",
		"event", "bnbloc.search.update_failed", "messages", len(batch), "error", err)
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
