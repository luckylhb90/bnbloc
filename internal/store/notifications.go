package store

import (
	"context"
	"errors"
	"sort"
	"sync"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// NotificationPrefs is one member's notification settings in a tenant. A
// member with no stored row gets DefaultNotificationPrefs (everything on).
// QuietRooms lists rooms the member muted: no notification of any type is
// produced for them in those rooms.
type NotificationPrefs struct {
	Mentions       bool
	AgentFinished  bool
	AgentFailed    bool
	BorrowRequests bool
	// ContentShared: an author shared content with the member.
	ContentShared bool
	// ContentReview: content waits for the member's approval (org
	// admins), or an admin decided the member's publish request.
	ContentReview bool
	// AppNotices: an interactive app sent the member a notice.
	AppNotices  bool
	QuietRooms  []string
	UpdatedAtMs int64
}

// DefaultNotificationPrefs is the setting of a member who never saved any.
func DefaultNotificationPrefs() NotificationPrefs {
	return NotificationPrefs{Mentions: true, AgentFinished: true, AgentFailed: true, BorrowRequests: true, ContentShared: true, ContentReview: true, AppNotices: true, QuietRooms: []string{}}
}

// NotificationPrefsStore persists per-member notification settings keyed
// (tenant, user). The tenant comes from the context (tenant.Require).
type NotificationPrefsStore interface {
	// GetNotificationPrefs returns the stored prefs; found=false (no error)
	// when the member never saved any.
	GetNotificationPrefs(ctx context.Context, userID string) (NotificationPrefs, bool, error)
	// PutNotificationPrefs replaces the member's prefs.
	PutNotificationPrefs(ctx context.Context, userID string, p NotificationPrefs) error
}

// NotificationOutboxItem is one queued delivery of a notification to one
// notifier component. EventJSON is the exact request body; the worker signs
// and posts it verbatim. ID is the logical event's idempotency key (stable
// for one event, so observing the same event twice enqueues it once per
// notifier); (TenantID, ID, Notifier) keys the row, so retries and outcomes
// are per target.
type NotificationOutboxItem struct {
	TenantID        string
	ID              string
	Notifier        string
	EventJSON       []byte
	Attempts        int
	NextAttemptAtMs int64
	DeliveredAtMs   int64 // 0 = not delivered
	FailedAtMs      int64 // 0 = not given up
	LastError       string
	CreatedAtMs     int64
}

// NotificationClaim asks for up to Limit due items (cross-tenant: one worker
// drains every tenant's queue; Limit <= 0 claims one, at most
// MaxNotificationClaim). Claimed items are leased to Token until
// NowMs+LeaseMs: another worker (replica) cannot claim them before the lease
// lapses, and a crashed worker's items become due again afterwards.
type NotificationClaim struct {
	Token   string
	NowMs   int64
	LeaseMs int64
	Limit   int
}

// NotificationOutcome is the result of one delivery attempt.
type NotificationOutcome string

// Attempt outcomes.
const (
	NotificationDelivered NotificationOutcome = "delivered"
	NotificationRetry     NotificationOutcome = "retry"
	NotificationFailed    NotificationOutcome = "failed"
	// NotificationDispatched records a hand-off to an asynchronous channel
	// whose outcome arrives later (a member's desktop plugin acknowledging
	// it). It is not a delivery attempt: Attempts is unchanged, the claim
	// stays with Token (so the outcome is recorded under the same lease),
	// NextAttemptAtMs becomes the outcome deadline — past it the item is due
	// again, e.g. after a restart lost the outcome — and EventJSON, when
	// non-empty, replaces the body.
	NotificationDispatched NotificationOutcome = "dispatched"
)

// NotificationAttempt records one attempt on a claimed item. It only applies
// while Token still holds the item's lease (ErrNotificationLeaseLost
// otherwise), so a worker whose lease lapsed cannot clobber a newer attempt.
type NotificationAttempt struct {
	TenantID        string
	ID              string
	Notifier        string
	Token           string
	Outcome         NotificationOutcome
	AtMs            int64
	NextAttemptAtMs int64 // retry only
	// EventJSON, retry only and when non-empty, replaces the item's body:
	// a partial delivery narrows the recipients to those still owed.
	EventJSON []byte
	Error     string
}

// ErrNotificationLeaseLost reports an attempt against an item the caller no
// longer holds (lease lapsed and re-claimed, or already finished).
var ErrNotificationLeaseLost = errors.New("store: notification lease lost")

// NotificationOutboxStore is the durable outbound-notification queue.
type NotificationOutboxStore interface {
	// EnqueueNotification inserts an item under the context tenant. A second
	// insert of the same (tenant, id, notifier) is a no-op reporting
	// inserted=false.
	EnqueueNotification(ctx context.Context, item NotificationOutboxItem) (inserted bool, err error)
	// ClaimNotifications leases up to Limit due items across tenants.
	ClaimNotifications(ctx context.Context, c NotificationClaim) ([]NotificationOutboxItem, error)
	// RecordNotificationAttempt finishes one attempt on a leased item.
	RecordNotificationAttempt(ctx context.Context, a NotificationAttempt) error
	// PruneNotifications deletes up to limit finished (delivered or failed)
	// items created before beforeMs, returning how many it removed (limit
	// <= 0 or above NotificationPruneBatch means NotificationPruneBatch).
	PruneNotifications(ctx context.Context, beforeMs int64, limit int) (int64, error)
	// NotificationOutboxStats aggregates every tenant's outbox (metrics).
	NotificationOutboxStats(ctx context.Context) (NotificationOutboxStats, error)
}

// Notification batch bounds, the same on both backends.
const (
	// MaxNotificationClaim bounds one claim.
	MaxNotificationClaim = 500
	// NotificationPruneBatch bounds one prune of finished items, reply
	// tokens or reply references.
	NotificationPruneBatch = 1000
)

func notificationClaimLimit(n int) int { return clampLimit(n, 1, MaxNotificationClaim) }

func notificationPruneLimit(n int) int {
	return clampLimit(n, NotificationPruneBatch, NotificationPruneBatch)
}

// truncateErr bounds a stored delivery error to its column width.
func truncateErr(s string) string {
	const max = 512
	if len(s) <= max {
		return s
	}
	return s[:max]
}

// MemoryNotifications implements the notification stores in memory (dev/tests).
type MemoryNotifications struct {
	mu     sync.Mutex
	prefs  map[[2]string]NotificationPrefs
	outbox map[[3]string]*memOutboxRow
}

type memOutboxRow struct {
	item        NotificationOutboxItem
	claimToken  string
	claimExpiry int64
}

// NewMemoryNotifications constructs empty in-memory notification stores.
func NewMemoryNotifications() *MemoryNotifications {
	return &MemoryNotifications{prefs: map[[2]string]NotificationPrefs{}, outbox: map[[3]string]*memOutboxRow{}}
}

var (
	_ NotificationPrefsStore  = (*MemoryNotifications)(nil)
	_ NotificationOutboxStore = (*MemoryNotifications)(nil)
)

func (m *MemoryNotifications) GetNotificationPrefs(ctx context.Context, userID string) (NotificationPrefs, bool, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return NotificationPrefs{}, false, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p, ok := m.prefs[[2]string{tid, userID}]
	if ok {
		p.QuietRooms = append([]string{}, p.QuietRooms...)
	}
	return p, ok, nil
}

func (m *MemoryNotifications) PutNotificationPrefs(ctx context.Context, userID string, p NotificationPrefs) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	p.QuietRooms = append([]string{}, p.QuietRooms...)
	m.prefs[[2]string{tid, userID}] = p
	return nil
}

func (m *MemoryNotifications) EnqueueNotification(ctx context.Context, item NotificationOutboxItem) (bool, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return false, err
	}
	item.TenantID = tid
	key := [3]string{tid, item.ID, item.Notifier}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.outbox[key]; ok {
		return false, nil
	}
	item.EventJSON = append([]byte{}, item.EventJSON...)
	m.outbox[key] = &memOutboxRow{item: item}
	return true, nil
}

func (m *MemoryNotifications) ClaimNotifications(_ context.Context, c NotificationClaim) ([]NotificationOutboxItem, error) {
	c.Limit = notificationClaimLimit(c.Limit)
	m.mu.Lock()
	defer m.mu.Unlock()
	due := []*memOutboxRow{}
	for _, row := range m.outbox {
		if row.item.DeliveredAtMs != 0 || row.item.FailedAtMs != 0 || row.item.NextAttemptAtMs > c.NowMs {
			continue
		}
		due = append(due, row)
	}
	sort.Slice(due, func(i, j int) bool {
		if due[i].item.NextAttemptAtMs != due[j].item.NextAttemptAtMs {
			return due[i].item.NextAttemptAtMs < due[j].item.NextAttemptAtMs
		}
		if due[i].item.TenantID != due[j].item.TenantID {
			return due[i].item.TenantID < due[j].item.TenantID
		}
		if due[i].item.ID != due[j].item.ID {
			return due[i].item.ID < due[j].item.ID
		}
		return due[i].item.Notifier < due[j].item.Notifier
	})
	if len(due) > c.Limit {
		due = due[:c.Limit]
	}
	out := make([]NotificationOutboxItem, 0, len(due))
	for _, row := range due {
		row.claimToken = c.Token
		row.claimExpiry = c.NowMs + c.LeaseMs
		// The lease is expressed through next_attempt_at_ms, exactly like the
		// MySQL store: the item is not due again until the lease lapses.
		row.item.NextAttemptAtMs = row.claimExpiry
		item := row.item
		item.EventJSON = append([]byte{}, item.EventJSON...)
		out = append(out, item)
	}
	return out, nil
}

func (m *MemoryNotifications) RecordNotificationAttempt(_ context.Context, a NotificationAttempt) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.outbox[[3]string{a.TenantID, a.ID, a.Notifier}]
	if !ok || row.claimToken != a.Token || row.item.DeliveredAtMs != 0 || row.item.FailedAtMs != 0 {
		return ErrNotificationLeaseLost
	}
	if a.Outcome == NotificationDispatched {
		row.item.LastError = truncateErr(a.Error)
		row.item.NextAttemptAtMs = a.NextAttemptAtMs
		if len(a.EventJSON) > 0 {
			row.item.EventJSON = append([]byte{}, a.EventJSON...)
		}
		return nil
	}
	row.item.Attempts++
	row.item.LastError = truncateErr(a.Error)
	row.claimToken = ""
	switch a.Outcome {
	case NotificationDelivered:
		row.item.DeliveredAtMs = a.AtMs
	case NotificationFailed:
		row.item.FailedAtMs = a.AtMs
	default:
		row.item.NextAttemptAtMs = a.NextAttemptAtMs
		if len(a.EventJSON) > 0 {
			row.item.EventJSON = append([]byte{}, a.EventJSON...)
		}
	}
	return nil
}

func (m *MemoryNotifications) PruneNotifications(_ context.Context, beforeMs int64, limit int) (int64, error) {
	limit = notificationPruneLimit(limit)
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for key, row := range m.outbox {
		if n >= int64(limit) {
			break
		}
		finished := row.item.DeliveredAtMs != 0 || row.item.FailedAtMs != 0
		if finished && row.item.CreatedAtMs < beforeMs {
			delete(m.outbox, key)
			n++
		}
	}
	return n, nil
}

// Item returns a copy of one outbox item (tests and diagnostics).
func (m *MemoryNotifications) Item(tenantID, id, notifier string) (NotificationOutboxItem, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	row, ok := m.outbox[[3]string{tenantID, id, notifier}]
	if !ok {
		return NotificationOutboxItem{}, false
	}
	return row.item, true
}

// Items returns every stored outbox item (tests and diagnostics).
func (m *MemoryNotifications) Items() []NotificationOutboxItem {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]NotificationOutboxItem, 0, len(m.outbox))
	for _, row := range m.outbox {
		out = append(out, row.item)
	}
	return out
}

// Len returns how many outbox items are stored (tests and diagnostics).
func (m *MemoryNotifications) Len() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.outbox)
}
