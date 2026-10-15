package notify

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"math/big"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/audit"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/extreg"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Worker defaults.
const (
	DefaultMaxAttempts = 8
	defaultBaseBackoff = 10 * time.Second
	defaultMaxBackoff  = 30 * time.Minute
	// defaultLease must outlast a whole sequential batch of deliveries
	// (batch x request timeout) so a slow batch is never re-claimed mid-way.
	defaultLease     = 5 * time.Minute
	defaultBatch     = 16
	defaultPoll      = 2 * time.Second
	defaultRetention = 7 * 24 * time.Hour
	deliveryTimeout  = 10 * time.Second
)

// Worker drains the notification outbox: it leases due items (safe with
// several replicas draining one database), mints each recipient's reply
// token, signs and POSTs the item to its notifier component, and
// records the outcome with exponential backoff. A notifier's per-recipient
// report narrows a retry to the recipients that failed transiently.
type Worker struct {
	Outbox    store.NotificationOutboxStore
	Notifiers *Directory
	Secrets   *SecretStore
	// Replies mints each recipient's reply token for a notification that
	// accepts replies (nil = notifications cannot be answered).
	Replies *ReplyTokens

	// Push delivers the built-in Web Push channel's items (nil = channel
	// off: such items are given up).
	Push *WebPush

	// Desktop hands DesktopChannel recipients to their desktop notification
	// plugin (realtime's plugin hook dispatcher; nil = every recipient goes
	// to the server notifiers). Emitter queues the server rows it routes or
	// falls back to (nil = DesktopChannel items are given up).
	Desktop DesktopRoute
	Emitter *Emitter

	Audit store.ExtensionAuditStore
	Now   func() time.Time
	// Metrics records delivery outcomes and the outbox snapshot (nil = off).
	Metrics *obs.Metrics

	MaxAttempts int
	BaseBackoff time.Duration
	MaxBackoff  time.Duration
	Lease       time.Duration
	Batch       int
	Poll        time.Duration
	Retention   time.Duration

	wake chan struct{}
}

func (w *Worker) defaults() {
	if w.MaxAttempts <= 0 {
		w.MaxAttempts = DefaultMaxAttempts
	}
	if w.BaseBackoff <= 0 {
		w.BaseBackoff = defaultBaseBackoff
	}
	if w.MaxBackoff <= 0 {
		w.MaxBackoff = defaultMaxBackoff
	}
	if w.Lease <= 0 {
		w.Lease = defaultLease
	}
	if w.Batch <= 0 {
		w.Batch = defaultBatch
	}
	if w.Poll <= 0 {
		w.Poll = defaultPoll
	}
	if w.Retention <= 0 {
		w.Retention = defaultRetention
	}
	if w.wake == nil {
		w.wake = make(chan struct{}, 1)
	}
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

// Wake asks the worker to drain now instead of at the next poll tick.
func (w *Worker) Wake() {
	if w == nil || w.wake == nil {
		return
	}
	select {
	case w.wake <- struct{}{}:
	default:
	}
}

// Run drains until ctx ends: on every wake-up and poll tick, plus an hourly
// prune of finished items older than Retention.
func (w *Worker) Run(ctx context.Context) {
	w.defaults()
	tick := time.NewTicker(w.Poll)
	defer tick.Stop()
	prune := time.NewTicker(time.Hour)
	defer prune.Stop()
	w.prune(ctx)
	stats := time.NewTicker(outboxStatsInterval)
	defer stats.Stop()
	w.sampleOutbox(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-prune.C:
			w.prune(ctx)
		case <-stats.C:
			w.sampleOutbox(ctx)
		case <-tick.C:
		case <-w.wake:
		}
		for {
			n, err := w.DrainOnce(ctx)
			if err != nil {
				slog.Warn("bnbloc notifications: claim failed", "event", "bnbloc.notify.claim_failed", "error", err)
			}
			if err != nil || n < w.Batch || ctx.Err() != nil {
				break
			}
		}
	}
}

func (w *Worker) prune(ctx context.Context) {
	before := w.now().Add(-w.Retention).UnixMilli()
	if _, err := w.Outbox.PruneNotifications(ctx, before, 1000); err != nil {
		slog.Warn("bnbloc notifications: prune failed", "event", "bnbloc.notify.prune_failed", "error", err)
	}
	if _, err := w.Replies.Prune(ctx); err != nil {
		slog.Warn("bnbloc notifications: reply token prune failed", "event", "bnbloc.notify.prune_failed", "error", err)
	}
}

// DrainOnce claims one batch of due items and attempts each, returning how
// many it claimed.
func (w *Worker) DrainOnce(ctx context.Context) (int, error) {
	w.defaults()
	token := randomToken()
	items, err := w.Outbox.ClaimNotifications(ctx, store.NotificationClaim{
		Token: token, NowMs: w.now().UnixMilli(), LeaseMs: w.Lease.Milliseconds(), Limit: w.Batch,
	})
	if err != nil {
		return 0, err
	}
	for _, it := range items {
		if ctx.Err() != nil {
			break
		}
		w.attempt(ctx, token, it)
	}
	return len(items), nil
}

func (w *Worker) attempt(ctx context.Context, token string, it store.NotificationOutboxItem) {
	if it.Notifier == DesktopChannel {
		w.attemptDesktop(ctx, token, it)
		return
	}
	tctx := tenant.NewContext(ctx, it.TenantID)
	a := store.NotificationAttempt{TenantID: it.TenantID, ID: it.ID, Notifier: it.Notifier, Token: token}
	a.Outcome, a.Error, a.EventJSON = w.send(tctx, it)
	if a.Outcome == store.NotificationRetry {
		if it.Attempts+1 >= w.MaxAttempts {
			a.Outcome = store.NotificationFailed
			a.EventJSON = nil
		} else {
			a.NextAttemptAtMs = w.now().Add(w.backoff(it.Attempts)).UnixMilli()
		}
	}
	a.AtMs = w.now().UnixMilli()
	if err := w.Outbox.RecordNotificationAttempt(ctx, a); err != nil {
		slog.Warn("bnbloc notifications: record attempt failed",
			"event", "bnbloc.notify.record_failed", "id", it.ID, "notifier", it.Notifier, "error", err)
	}
	w.audit(tctx, it, a)
	w.observeAttempt(a.Outcome)
}

// send performs one delivery of it and classifies the outcome. narrowed is
// the item body to keep for a partial retry (recipients still owed).
func (w *Worker) send(ctx context.Context, it store.NotificationOutboxItem) (store.NotificationOutcome, string, []byte) {
	if it.Notifier == WebPushChannel {
		return w.sendPush(ctx, it)
	}
	rec, found, err := w.Notifiers.Record(ctx, it.Notifier)
	switch {
	case err != nil:
		return store.NotificationRetry, "read notifier records: " + err.Error(), nil
	case !found || !extpipe.TenantDistribution(rec, it.TenantID).Distributed:
		// Uninstalled, switched off, or re-scoped away from the tenant after
		// the item was queued: nothing will ever accept it.
		return store.NotificationFailed, "notifier uninstalled, off, or not enabled for the tenant", nil
	}
	ep, live := w.Notifiers.Live(rec)
	if !live {
		return store.NotificationRetry, "notifier not active in this process (activation pending or failed)", nil
	}
	secret, ok, err := w.Secrets.Get(ctx, it.Notifier)
	if err != nil || !ok {
		return store.NotificationRetry, "notifier signing secret unavailable", nil
	}
	var p wire.NotificationPayload
	if err := json.Unmarshal(it.EventJSON, &p); err != nil {
		return store.NotificationFailed, "undecodable outbox item: " + err.Error(), nil
	}
	sent := p
	sent.Recipients = make([]wire.NotificationRecipient, len(p.Recipients))
	for i, r := range p.Recipients {
		sent.Recipients[i] = wire.NotificationRecipient{UserID: r.UserID}
		// A reply token is minted per attempt (never written to the outbox);
		// failing to mint only makes this notification unanswerable.
		tok, err := w.Replies.Mint(ctx, it.Notifier, p, r.UserID)
		if err != nil {
			slog.Warn("bnbloc notifications: mint reply token failed",
				"event", "bnbloc.notify.reply_token_failed", "id", it.ID, "notifier", it.Notifier, "error", err)
		}
		sent.Recipients[i].ReplyToken = tok
	}
	body, err := json.Marshal(sent)
	if err != nil {
		return store.NotificationFailed, err.Error(), nil
	}
	// The event id travels in the payload; a desktop-fallback row has its
	// own outbox id (FallbackRowID) but the same event id.
	res, err := Deliver(ctx, ep, secret, p.ID, body, w.now())
	if err != nil {
		return store.NotificationRetry, err.Error(), nil
	}
	return conclude(p, res)
}

// conclude classifies a delivery's per-recipient report: delivered unless a
// recipient failed transiently, in which case the item is retried with its
// recipients narrowed to those still owed.
func conclude(p wire.NotificationPayload, res DeliveryResult) (store.NotificationOutcome, string, []byte) {
	retryable, final := res.Failed()
	summary := summarize(retryable, final)
	if len(retryable) == 0 {
		return store.NotificationDelivered, summary, nil
	}
	if p.Group != "" {
		// A group notification has no recipients to narrow: a retryable
		// failure retries it whole.
		return store.NotificationRetry, summary, nil
	}
	owed := map[string]bool{}
	for _, r := range retryable {
		owed[r.UserID] = true
	}
	var keep []wire.NotificationRecipient
	for _, r := range p.Recipients {
		if owed[r.UserID] {
			keep = append(keep, wire.NotificationRecipient{UserID: r.UserID})
		}
	}
	if len(keep) == 0 {
		return store.NotificationDelivered, summary, nil
	}
	p.Recipients = keep
	narrowed, err := json.Marshal(p)
	if err != nil {
		return store.NotificationRetry, summary, nil
	}
	return store.NotificationRetry, summary, narrowed
}

// summarize renders the failed recipients of a delivery for last_error and
// the audit trail (ids and the notifier's error text only).
func summarize(retryable, final []wire.NotificationRecipientResult) string {
	var parts []string
	for _, r := range retryable {
		parts = append(parts, fmt.Sprintf("%s (retry): %s", r.UserID, r.Error))
	}
	for _, r := range final {
		parts = append(parts, fmt.Sprintf("%s: %s", r.UserID, r.Error))
	}
	return strings.Join(parts, "; ")
}

// backoff is BaseBackoff·2^attempts, capped at MaxBackoff, with ±20% jitter
// so a receiver outage does not end in a synchronized retry storm.
func (w *Worker) backoff(attempts int) time.Duration {
	d := w.BaseBackoff
	for i := 0; i < attempts && d < w.MaxBackoff; i++ {
		d *= 2
	}
	if d > w.MaxBackoff {
		d = w.MaxBackoff
	}
	span := int64(d) / 5
	if span > 0 {
		if n, err := rand.Int(rand.Reader, big.NewInt(2*span)); err == nil {
			d += time.Duration(n.Int64() - span)
		}
	}
	return d
}

func (w *Worker) audit(ctx context.Context, it store.NotificationOutboxItem, a store.NotificationAttempt) {
	if w.Audit == nil {
		return
	}
	detail := it.ID
	if a.Error != "" {
		detail += ": " + a.Error
	}
	audit.Record(ctx, w.Audit, store.ExtAuditRecord{
		Kind: string(extreg.KindNotifier), Name: it.Notifier, Action: "deliver", Outcome: string(a.Outcome),
		Detail: detail, TenantID: it.TenantID, AtMs: a.AtMs,
	})
}

func randomToken() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}
