package store

import (
	"context"
	"database/sql"
	"errors"
	"sync"
)

// NotificationReplyToken is one reply token of a delivered notification (N1):
// the notifier component Notifier may post UserID's reply into RoomID
// (ChannelID) until ExpiresAtMs, addressed to the agent ReplyOwnerID /
// ReplyProvider / ReplyCore (or to the room — human chat — when
// ReplyProvider is empty). Only the SHA-256 of the token is stored
// (TokenHash, hex), so a database read never yields a usable token. A token
// may be used many times until it expires (each reply is its own message).
type NotificationReplyToken struct {
	TokenHash     string
	TenantID      string
	Notifier      string
	EventID       string
	RoomID        string
	ChannelID     string
	UserID        string
	ReplyOwnerID  string
	ReplyProvider string
	ReplyCore     string
	CreatedAtMs   int64
	ExpiresAtMs   int64
}

// NotificationReplyTokenStore persists reply tokens. Lookups are by token
// hash and cross-tenant (the tenant is a column of the row, not a context
// scope): the caller is a notifier component, which has no tenant session.
// A token is live strictly before ExpiresAtMs (liveAt); pruning at beforeMs
// deletes the tokens (and references) no longer live then.
type NotificationReplyTokenStore interface {
	// PutNotificationReplyToken stores t; a taken hash is
	// ErrNotificationReplyTokenExists.
	PutNotificationReplyToken(ctx context.Context, t NotificationReplyToken) error
	// GetNotificationReplyToken returns the row of tokenHash (found=false when
	// unknown; expiry is the caller's check).
	GetNotificationReplyToken(ctx context.Context, tokenHash string) (NotificationReplyToken, bool, error)
	// DeleteNotificationReplyTokens removes every token of a notifier
	// (uninstall).
	DeleteNotificationReplyTokens(ctx context.Context, notifier string) error
	// PruneNotificationReplyTokens deletes up to limit tokens expired at
	// beforeMs (limit <= 0 or above NotificationPruneBatch means
	// NotificationPruneBatch), and as many expired references.
	PruneNotificationReplyTokens(ctx context.Context, beforeMs int64, limit int) (int64, error)
	// Reply references (R9, notification_reply_refs.go): uninstall and
	// pruning remove them with their tokens.
	NotificationReplyRefStore
}

var (
	_ NotificationReplyTokenStore = (*MemoryNotificationReplies)(nil)
	_ NotificationReplyTokenStore = (*MySQLNotificationReplies)(nil)
)

// MemoryNotificationReplies is the in-memory NotificationReplyTokenStore.
type MemoryNotificationReplies struct {
	mu   sync.Mutex
	rows map[string]NotificationReplyToken
	refs map[replyRefKey]NotificationReplyRef
}

// NewMemoryNotificationReplies builds an empty in-memory reply-token store.
func NewMemoryNotificationReplies() *MemoryNotificationReplies {
	return &MemoryNotificationReplies{rows: map[string]NotificationReplyToken{}, refs: map[replyRefKey]NotificationReplyRef{}}
}

var errEmptyReplyTokenHash = errors.New("store: reply token hash required")

// ErrNotificationReplyTokenExists rejects a second token under one hash.
var ErrNotificationReplyTokenExists = errors.New("store: reply token exists")

func (m *MemoryNotificationReplies) PutNotificationReplyToken(_ context.Context, t NotificationReplyToken) error {
	if t.TokenHash == "" {
		return errEmptyReplyTokenHash
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, taken := m.rows[t.TokenHash]; taken {
		return ErrNotificationReplyTokenExists
	}
	m.rows[t.TokenHash] = t
	return nil
}

func (m *MemoryNotificationReplies) GetNotificationReplyToken(_ context.Context, tokenHash string) (NotificationReplyToken, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	t, ok := m.rows[tokenHash]
	return t, ok, nil
}

func (m *MemoryNotificationReplies) DeleteNotificationReplyTokens(_ context.Context, notifier string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	for k, t := range m.rows {
		if t.Notifier == notifier {
			delete(m.rows, k)
		}
	}
	m.deleteRefsLocked(notifier)
	return nil
}

func (m *MemoryNotificationReplies) PruneNotificationReplyTokens(_ context.Context, beforeMs int64, limit int) (int64, error) {
	limit = notificationPruneLimit(limit)
	m.mu.Lock()
	defer m.mu.Unlock()
	var n int64
	for k, t := range m.rows {
		if n >= int64(limit) {
			break
		}
		if !liveAt(t.ExpiresAtMs, beforeMs) {
			delete(m.rows, k)
			n++
		}
	}
	m.pruneRefsLocked(beforeMs, limit)
	return n, nil
}

// MySQLNotificationReplies is the MySQL-backed reply-token store
// (notification_reply_tokens).
type MySQLNotificationReplies struct{ DB *sql.DB }

// NewMySQLNotificationReplies builds the store over a shared pool.
func NewMySQLNotificationReplies(db *sql.DB) *MySQLNotificationReplies {
	return &MySQLNotificationReplies{DB: db}
}

func (m *MySQLNotificationReplies) PutNotificationReplyToken(ctx context.Context, t NotificationReplyToken) error {
	if t.TokenHash == "" {
		return errEmptyReplyTokenHash
	}
	_, err := m.DB.ExecContext(ctx,
		`INSERT INTO notification_reply_tokens
		   (token_hash, tenant_id, notifier, event_id, room_id, channel_id, user_id,
		    reply_owner_id, reply_provider, reply_core, created_at_ms, expires_at_ms)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		t.TokenHash, t.TenantID, t.Notifier, t.EventID, t.RoomID, t.ChannelID, t.UserID,
		t.ReplyOwnerID, t.ReplyProvider, t.ReplyCore, t.CreatedAtMs, t.ExpiresAtMs)
	return dupAs(err, ErrNotificationReplyTokenExists)
}

func (m *MySQLNotificationReplies) GetNotificationReplyToken(ctx context.Context, tokenHash string) (NotificationReplyToken, bool, error) {
	// cross-tenant: looked up by token hash for a notifier (no tenant session).
	var t NotificationReplyToken
	err := m.DB.QueryRowContext(ctx,
		`SELECT token_hash, tenant_id, notifier, event_id, room_id, channel_id, user_id,
		        reply_owner_id, reply_provider, reply_core, created_at_ms, expires_at_ms
		 FROM notification_reply_tokens WHERE token_hash=?`, tokenHash).
		Scan(&t.TokenHash, &t.TenantID, &t.Notifier, &t.EventID, &t.RoomID, &t.ChannelID, &t.UserID,
			&t.ReplyOwnerID, &t.ReplyProvider, &t.ReplyCore, &t.CreatedAtMs, &t.ExpiresAtMs)
	if errors.Is(err, sql.ErrNoRows) {
		return NotificationReplyToken{}, false, nil
	}
	if err != nil {
		return NotificationReplyToken{}, false, err
	}
	return t, true, nil
}

func (m *MySQLNotificationReplies) DeleteNotificationReplyTokens(ctx context.Context, notifier string) error {
	// cross-tenant: a notifier's uninstall removes its tokens in every tenant.
	if _, err := m.DB.ExecContext(ctx, `DELETE FROM notification_reply_tokens WHERE notifier=?`, notifier); err != nil {
		return err
	}
	return m.deleteRefs(ctx, notifier)
}

func (m *MySQLNotificationReplies) PruneNotificationReplyTokens(ctx context.Context, beforeMs int64, limit int) (int64, error) {
	// cross-tenant: expired tokens of every tenant are pruned together.
	limit = notificationPruneLimit(limit)
	res, err := m.DB.ExecContext(ctx,
		`DELETE FROM notification_reply_tokens WHERE expires_at_ms <= ? LIMIT ?`, beforeMs, limit)
	if err != nil {
		return 0, err
	}
	if err := m.pruneRefs(ctx, beforeMs, limit); err != nil {
		return 0, err
	}
	return res.RowsAffected()
}
