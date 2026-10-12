// Package retention runs data retention (docs/prd/2026-10-10-enterprise-scale.md
// §3): a periodic pass that deletes the rows each tenant's effective policy
// has expired, then a blob collection that removes blobs no row references.
//
// realtime runs it on every replica (it is the service holding the blob
// store). A pass takes the cluster-wide retention lock first, so one
// replica works at a time; the work itself is safe concurrently (deleting
// the same expired rows twice is harmless), the lock only avoids doing it
// twice. It only deletes store rows and blobs: it never touches a room hub,
// so it needs no room ownership.
package retention

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand/v2"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/blobstore"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Defaults of the worker's cadence. They are properties of the worker, not
// of a deployment: retention days are whole days, so an hourly pass deletes
// a row within an hour of its expiry, and listing the blob store is the
// expensive step, so it runs daily.
const (
	DefaultInterval       = time.Hour
	DefaultBlobGCInterval = 24 * time.Hour
	// DefaultBlobGrace protects a blob from collection for a week after it
	// was last stored. Every reference is written right after its upload
	// except a staged component or desktop artifact, which is referenced
	// when the admin publishes the package; a week covers that, and the
	// package install probes the blob (a collected one fails loudly and is
	// uploaded again).
	DefaultBlobGrace = 7 * 24 * time.Hour
	// DefaultBatch bounds one delete statement.
	DefaultBatch = 500
	// blobCheckBatch is how many blobs one reference query checks.
	blobCheckBatch = 500
)

// Metric names (docs/monitoring.md).
const (
	MetricDeleted   = "retention_deleted_total"
	MetricRuns      = "retention_runs_total"
	MetricBlobBytes = "retention_blob_bytes_deleted_total"
	MetricLastRun   = "retention_last_run_timestamp_seconds"
)

// RegisterMetrics describes the retention series.
func RegisterMetrics(m *obs.Metrics) {
	if m == nil {
		return
	}
	m.Describe(MetricDeleted, "Rows, blobs and search documents data retention deleted, by item (room_messages, attachments, artifact_versions, shared_agent_audit, extension_audit, agent_jobs, blobs, search_documents, ...).")
	m.Describe(MetricRuns, "Retention passes by outcome (ok, error, skipped: another replica held the lock).")
	m.Describe(MetricBlobBytes, "Bytes of blobs the blob collection deleted.")
	m.Describe(MetricLastRun, "Unix time of the last finished retention pass on this replica.")
}

// SearchIndex is the message search index retention keeps in step with
// the rows (search.Index satisfies it).
type SearchIndex interface {
	// PurgeBefore removes the documents of tenantID's messages created
	// before beforeMs, returning how many it removed.
	PurgeBefore(ctx context.Context, tenantID string, beforeMs int64) (int64, error)
}

// Worker deletes expired rows and unreferenced blobs.
type Worker struct {
	Store    store.RetentionStore
	Tenants  store.TenantStore
	Settings store.PlatformSettingsStore
	// Audit receives one record per tenant and kind a pass deleted from
	// (kind retention, action purge), so the deletion itself is on record.
	Audit store.ExtensionAuditStore
	// Blobs is the blob store to collect; nil skips the collection.
	Blobs blobstore.BlobStore
	// Search removes expired messages from the message search index after
	// each tenant's message purge (nil = nothing to remove; the store's
	// FULLTEXT index follows the rows on its own).
	Search  SearchIndex
	Metrics *obs.Metrics
	// Zero values take the defaults above; Now defaults to time.Now.
	Now            func() time.Time
	Interval       time.Duration
	BlobGCInterval time.Duration
	BlobGrace      time.Duration
	Batch          int
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

func durationOr(d, def time.Duration) time.Duration {
	if d > 0 {
		return d
	}
	return def
}

// Run passes every Interval until ctx ends. The first pass waits a random
// part of the interval so replicas started together do not race for the
// lock on every tick.
func (w *Worker) Run(ctx context.Context) {
	interval := durationOr(w.Interval, DefaultInterval)
	wait := time.Duration(rand.Int64N(int64(interval)))
	for {
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		_, _ = w.RunOnce(ctx)
		wait = interval
	}
}

// Report is what one pass did.
type Report struct {
	// Ran is false when another replica held the lock.
	Ran bool
	// Deleted counts by item (store.Purged* names).
	Deleted store.PurgeResult
	// BlobGC is true when the pass collected blobs.
	BlobGC bool
}

// RunOnce runs one pass: purge every tenant's expired rows, then collect
// blobs when the last collection is older than BlobGCInterval. A failure
// on one tenant or kind is logged and the pass goes on; the first error is
// returned.
func (w *Worker) RunOnce(ctx context.Context) (Report, error) {
	rep := Report{Deleted: store.PurgeResult{}}
	release, ok, err := w.Store.TryRetentionLock(ctx)
	if err != nil {
		w.count(MetricRuns, 1, obs.L("outcome", "error"))
		slog.Error("bnbloc retention: lock failed", "event", "bnbloc.retention.error", "what", "lock", "error", err)
		return rep, err
	}
	if !ok {
		w.count(MetricRuns, 1, obs.L("outcome", "skipped"))
		return rep, nil
	}
	defer release()
	rep.Ran = true
	start := w.now()
	firstErr := w.purgeAll(ctx, start, rep.Deleted)
	// Staged content drafts expire instead of following a policy: delete
	// them before the blob collection so their bundles can go.
	drafts, err := w.Store.PurgeExpiredDrafts(ctx, store.PurgeRequest{NowMs: start.UnixMilli(), Batch: w.Batch})
	rep.Deleted.Add(drafts)
	if err != nil {
		slog.Error("bnbloc retention: purge failed", "event", "bnbloc.retention.error", "what", "content_drafts", "error", err)
		firstErr = errors.Join(firstErr, err)
	}
	status, err := store.LoadRetentionStatus(ctx, w.Settings)
	if err != nil {
		firstErr = errors.Join(firstErr, err)
	}
	if w.Blobs != nil && start.Sub(time.UnixMilli(status.LastBlobGCAtMs)) >= durationOr(w.BlobGCInterval, DefaultBlobGCInterval) {
		n, bytes, err := w.CollectBlobs(ctx)
		rep.Deleted[store.PurgedBlobs] += n
		w.count(MetricBlobBytes, float64(bytes))
		if err != nil {
			slog.Error("bnbloc retention: blob collection failed", "event", "bnbloc.retention.error", "what", "blob_gc", "error", err)
			firstErr = errors.Join(firstErr, err)
		} else {
			rep.BlobGC = true
			status.LastBlobGCAtMs = w.now().UnixMilli()
		}
	}
	for item, n := range rep.Deleted {
		if n > 0 {
			w.count(MetricDeleted, float64(n), obs.L("item", item))
		}
	}
	end := w.now()
	status.LastRunAtMs = end.UnixMilli()
	status.LastDeleted = map[string]int64(rep.Deleted)
	if err := store.SaveRetentionStatus(ctx, w.Settings, status, end.UnixMilli()); err != nil {
		firstErr = errors.Join(firstErr, err)
	}
	outcome := "ok"
	if firstErr != nil {
		outcome = "error"
	}
	w.count(MetricRuns, 1, obs.L("outcome", outcome))
	if w.Metrics != nil {
		w.Metrics.GaugeSet(MetricLastRun, float64(end.Unix()))
	}
	slog.Info("bnbloc retention: pass finished", "event", "bnbloc.retention.pass", "outcome", outcome,
		"deleted", map[string]int64(rep.Deleted), "blob_gc", rep.BlobGC, "duration_ms", end.Sub(start).Milliseconds())
	return rep, firstErr
}

func (w *Worker) count(name string, v float64, labels ...obs.Label) {
	if w.Metrics != nil && v > 0 {
		w.Metrics.CounterAdd(name, v, labels...)
	}
}

// purgeAll resolves each tenant's effective policy and purges every kind it
// limits; platform rows (tenant "") follow the platform default's audit
// days.
func (w *Worker) purgeAll(ctx context.Context, now time.Time, out store.PurgeResult) error {
	platform, err := store.LoadPlatformRetention(ctx, w.Settings)
	if err != nil {
		// An unreadable default must not be read as "keep forever" for some
		// tenants and not others: skip the pass.
		slog.Error("bnbloc retention: platform default unreadable; pass skipped", "event", "bnbloc.retention.error", "what", "policy", "error", err)
		return err
	}
	var firstErr error
	scopes := []struct {
		tenant string
		policy wire.RetentionPolicy
		kinds  []wire.RetentionKind
	}{{"", platform, []wire.RetentionKind{wire.RetentionAudit}}}
	for _, t := range w.Tenants.ListTenants(ctx) {
		scopes = append(scopes, struct {
			tenant string
			policy wire.RetentionPolicy
			kinds  []wire.RetentionKind
		}{t.TenantID, wire.EffectiveRetention(platform, t.Retention), wire.RetentionKinds})
	}
	for _, sc := range scopes {
		for _, kind := range sc.kinds {
			days := sc.policy.Days(kind)
			if days == nil || *days <= 0 {
				continue
			}
			if err := ctx.Err(); err != nil {
				return errors.Join(firstErr, err)
			}
			before := now.Add(-time.Duration(*days) * 24 * time.Hour).UnixMilli()
			res, err := w.Store.Purge(ctx, store.PurgeRequest{Kind: kind, TenantID: sc.tenant, BeforeMs: before, Batch: w.Batch, NowMs: now.UnixMilli()})
			if kind == wire.RetentionMessages {
				err = errors.Join(err, w.purgeSearch(ctx, sc.tenant, before, res))
			}
			out.Add(res)
			if err != nil {
				slog.Error("bnbloc retention: purge failed", "event", "bnbloc.retention.error", "what", "purge",
					"tenant", sc.tenant, "kind", string(kind), "error", err)
				firstErr = errors.Join(firstErr, fmt.Errorf("purge %s/%s: %w", sc.tenant, kind, err))
			}
			if total := sum(res); total > 0 {
				slog.Info("bnbloc retention: purged", "event", "bnbloc.retention.purge",
					"tenant", sc.tenant, "kind", string(kind), "days", *days, "before_ms", before, "deleted", map[string]int64(res))
				w.auditPurge(ctx, sc.tenant, kind, *days, before, res)
			}
		}
	}
	return firstErr
}

// purgeSearch removes the tenant's expired messages from the search index,
// counting them in res. It follows the policy cutoff, not the rows the
// purge reached: every message created before it is deleted or fenced (a
// fence has no text and no document), and if the purge stopped early the
// rest goes on the next pass anyway — the index never keeps an expired
// message after a pass, even when the row purge failed half-way. A failure
// is retried by the next pass (the delete is idempotent).
func (w *Worker) purgeSearch(ctx context.Context, tenantID string, before int64, res store.PurgeResult) error {
	if w.Search == nil {
		return nil
	}
	n, err := w.Search.PurgeBefore(ctx, tenantID, before)
	res[store.PurgedSearchDocuments] += n
	if err != nil {
		return fmt.Errorf("search index: %w", err)
	}
	return nil
}

// auditPurge records what a purge deleted in the tenant's audit log.
func (w *Worker) auditPurge(ctx context.Context, tenantID string, kind wire.RetentionKind, days int, before int64, res store.PurgeResult) {
	if w.Audit == nil {
		return
	}
	items := make([]string, 0, len(res))
	for item, n := range res {
		if n > 0 {
			items = append(items, item+"="+strconv.FormatInt(n, 10))
		}
	}
	sort.Strings(items)
	detail := "older than " + strconv.Itoa(days) + " days (before " + time.UnixMilli(before).UTC().Format(time.RFC3339) + "): " + strings.Join(items, " ")
	if err := w.Audit.AppendExtAudit(ctx, store.ExtAuditRecord{
		Kind: wire.AuditKindRetention, Name: string(kind), Action: "purge", Actor: "retention",
		TenantID: tenantID, Outcome: "ok", Detail: detail, AtMs: w.now().UnixMilli(),
	}); err != nil {
		slog.Warn("bnbloc retention: audit append failed", "event", "bnbloc.retention.error", "what", "audit", "error", err)
	}
}

func sum(r store.PurgeResult) int64 {
	var n int64
	for _, v := range r {
		n += v
	}
	return n
}

// CollectBlobs deletes every blob that was last stored before the grace
// period and that no row references. Blobs are content-addressed and may
// back several rows, so a blob goes only when the last reference is gone.
//
// Safety against an upload racing the collection: a reference is always
// written after its Put, and a Put of existing content rewrites the blob
// (fresh ModTime). Each candidate is therefore re-checked right before the
// delete: unreferenced at the reference query AND still older than the
// grace period by a fresh Stat. What remains is the window between that
// Stat and the delete, in which the very same bytes would have to be
// uploaded again after lying unreferenced for longer than the grace period.
func (w *Worker) CollectBlobs(ctx context.Context) (deleted, bytes int64, err error) {
	cutoff := w.now().Add(-durationOr(w.BlobGrace, DefaultBlobGrace))
	var batch []blobstore.Info
	flush := func() error {
		if len(batch) == 0 {
			return nil
		}
		keys := make([]string, len(batch))
		for i, b := range batch {
			keys[i] = b.Key
		}
		refs, err := w.Store.ReferencedBlobKeys(ctx, keys)
		if err != nil {
			return fmt.Errorf("blob references: %w", err)
		}
		for _, b := range batch {
			if refs[b.Key] {
				continue
			}
			info, err := w.Blobs.Stat(ctx, b.Key)
			if errors.Is(err, blobstore.ErrBlobNotFound) {
				continue
			}
			if err != nil {
				return err
			}
			if !info.ModTime.Before(cutoff) {
				continue // stored again meanwhile
			}
			if err := w.Blobs.Delete(ctx, b.Key); err != nil {
				return err
			}
			deleted++
			bytes += info.Size
		}
		batch = batch[:0]
		return nil
	}
	err = w.Blobs.Walk(ctx, func(info blobstore.Info) error {
		if !info.ModTime.Before(cutoff) {
			return nil
		}
		batch = append(batch, info)
		if len(batch) >= blobCheckBatch {
			return flush()
		}
		return nil
	})
	if err == nil {
		err = flush()
	}
	if deleted > 0 {
		slog.Info("bnbloc retention: blobs collected", "event", "bnbloc.retention.blob_gc", "deleted", deleted, "bytes", bytes)
	}
	return deleted, bytes, err
}
