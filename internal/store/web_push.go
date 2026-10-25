package store

import (
	"context"
	"sort"
	"sync"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// WebPushSubscription is one browser's Web Push subscription (the Push API's
// PushSubscription) registered by a member in a tenant (N4). ID is the
// endpoint's digest (one row per browser endpoint: a browser that
// re-subscribes under another member moves to that member). P256dh and Auth
// are the subscription's public encryption key and auth secret, base64url
// as the browser reports them; they are not bnbloc secrets (the push service
// already knows the endpoint) but only the delivering service reads them.
type WebPushSubscription struct {
	ID          string
	UserID      string
	Endpoint    string
	P256dh      string
	Auth        string
	UserAgent   string
	CreatedAtMs int64
	UpdatedAtMs int64
}

// WebPushSubscriptionStore persists members' Web Push subscriptions keyed
// (tenant, id); the tenant comes from the context.
type WebPushSubscriptionStore interface {
	// PutWebPushSubscription inserts or replaces the row for s.ID (an
	// existing row keeps its CreatedAtMs and moves to s.UserID).
	PutWebPushSubscription(ctx context.Context, s WebPushSubscription) error
	// ListWebPushSubscriptions returns the member's subscriptions, oldest
	// update first.
	ListWebPushSubscriptions(ctx context.Context, userID string) ([]WebPushSubscription, error)
	// DeleteWebPushSubscription removes the member's row id (another
	// member's row is untouched); missing = no-op.
	DeleteWebPushSubscription(ctx context.Context, userID, id string) error
	// DeleteWebPushSubscriptionsForUser removes every row of the member in
	// the context tenant (member removed from the tenant).
	DeleteWebPushSubscriptionsForUser(ctx context.Context, userID string) error
}

var (
	_ WebPushSubscriptionStore = (*MemoryWebPush)(nil)
	_ WebPushSubscriptionStore = (*MySQLWebPush)(nil)
)

// MemoryWebPush is the in-memory WebPushSubscriptionStore (dev/tests).
type MemoryWebPush struct {
	mu   sync.Mutex
	rows map[[2]string]WebPushSubscription // (tenant, id)
}

// NewMemoryWebPush builds an empty in-memory subscription store.
func NewMemoryWebPush() *MemoryWebPush {
	return &MemoryWebPush{rows: map[[2]string]WebPushSubscription{}}
}

func (m *MemoryWebPush) PutWebPushSubscription(ctx context.Context, s WebPushSubscription) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := [2]string{tid, s.ID}
	if old, ok := m.rows[key]; ok {
		// A re-put keeps the creation time, like the SQL upsert.
		s.CreatedAtMs = old.CreatedAtMs
	}
	m.rows[key] = s
	return nil
}

func (m *MemoryWebPush) ListWebPushSubscriptions(ctx context.Context, userID string) ([]WebPushSubscription, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	out := []WebPushSubscription{}
	for k, s := range m.rows {
		if k[0] == tid && s.UserID == userID {
			out = append(out, s)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].UpdatedAtMs != out[j].UpdatedAtMs {
			return out[i].UpdatedAtMs < out[j].UpdatedAtMs
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func (m *MemoryWebPush) DeleteWebPushSubscription(ctx context.Context, userID, id string) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	key := [2]string{tid, id}
	if s, ok := m.rows[key]; ok && s.UserID == userID {
		delete(m.rows, key)
	}
	return nil
}

func (m *MemoryWebPush) DeleteWebPushSubscriptionsForUser(ctx context.Context, userID string) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, s := range m.rows {
		if k[0] == tid && s.UserID == userID {
			delete(m.rows, k)
		}
	}
	return nil
}

// MySQLWebPush is the MySQL-backed WebPushSubscriptionStore.
type MySQLWebPush struct{ DB *DB }

// NewMySQLWebPush builds a MySQLWebPush over a shared pool.
func NewMySQLWebPush(db *DB) *MySQLWebPush { return &MySQLWebPush{DB: db} }

func (m *MySQLWebPush) PutWebPushSubscription(ctx context.Context, s WebPushSubscription) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	_, err = m.DB.ExecContext(ctx,
		`INSERT INTO web_push_subscriptions (tenant_id, id, user_id, endpoint, p256dh, auth_secret, user_agent, created_at_ms, updated_at_ms)
		 VALUES (?,?,?,?,?,?,?,?,?)
		 ON DUPLICATE KEY UPDATE user_id=VALUES(user_id), endpoint=VALUES(endpoint), p256dh=VALUES(p256dh),
		   auth_secret=VALUES(auth_secret), user_agent=VALUES(user_agent), updated_at_ms=VALUES(updated_at_ms)`,
		tid, s.ID, s.UserID, s.Endpoint, s.P256dh, s.Auth, s.UserAgent, s.CreatedAtMs, s.UpdatedAtMs)
	return err
}

func (m *MySQLWebPush) ListWebPushSubscriptions(ctx context.Context, userID string) ([]WebPushSubscription, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	return queryAll(ctx, m.DB,
		`SELECT id, user_id, endpoint, p256dh, auth_secret, user_agent, created_at_ms, updated_at_ms
		 FROM web_push_subscriptions WHERE tenant_id=? AND user_id=? ORDER BY updated_at_ms, id`, func(r rowScanner) (WebPushSubscription, error) {
			var s WebPushSubscription
			err := r.Scan(&s.ID, &s.UserID, &s.Endpoint, &s.P256dh, &s.Auth, &s.UserAgent, &s.CreatedAtMs, &s.UpdatedAtMs)
			return s, err
		}, tid, userID)
}

func (m *MySQLWebPush) DeleteWebPushSubscription(ctx context.Context, userID, id string) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	_, err = m.DB.ExecContext(ctx, `DELETE FROM web_push_subscriptions WHERE tenant_id=? AND id=? AND user_id=?`, tid, id, userID)
	return err
}

func (m *MySQLWebPush) DeleteWebPushSubscriptionsForUser(ctx context.Context, userID string) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	_, err = m.DB.ExecContext(ctx, `DELETE FROM web_push_subscriptions WHERE tenant_id=? AND user_id=?`, tid, userID)
	return err
}
