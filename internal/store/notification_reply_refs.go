package store

import (
	"context"
	"database/sql"
	"errors"
)

// NotificationReplyRef (R9) binds a message the notifier sent in the
// receiving system (Ref, opaque: e.g. the messenger's message id of a card)
// and the receiving system's account it was sent to (Recipient, opaque) to
// the reply token bnbloc delivered for that recipient (TokenHash). A reply
// quoting Ref from Recipient resolves to the token server-side, so the
// notifier keeps no process state and any of its replicas can answer. One
// message sent to several recipients is one Ref with one row per recipient.
// The row lives as long as its token (ExpiresAtMs copies the token's).
type NotificationReplyRef struct {
	Notifier    string
	Ref         string
	Recipient   string
	TokenHash   string
	CreatedAtMs int64
	ExpiresAtMs int64
}

// NotificationReplyRefStore persists reply references (cross-tenant, like
// the tokens: the caller is a notifier component).
type NotificationReplyRefStore interface {
	// PutNotificationReplyRef records r. Re-registering the same
	// (notifier, ref, recipient) with the same token is a no-op; with another
	// token it is ErrNotificationReplyRefConflict (a reference never moves).
	PutNotificationReplyRef(ctx context.Context, r NotificationReplyRef) error
	// GetNotificationReplyRef returns the row of (notifier, ref, recipient)
	// (found=false when unknown; expiry is the caller's check).
	GetNotificationReplyRef(ctx context.Context, notifier, ref, recipient string) (NotificationReplyRef, bool, error)
}

// ErrNotificationReplyRefConflict reports a reference already bound to
// another token.
var ErrNotificationReplyRefConflict = errors.New("store: reply reference bound to another token")

var errIncompleteReplyRef = errors.New("store: reply reference needs notifier, ref, recipient and token hash")

func (r NotificationReplyRef) complete() bool {
	return r.Notifier != "" && r.Ref != "" && r.Recipient != "" && r.TokenHash != ""
}

type replyRefKey struct{ notifier, ref, recipient string }

func (m *MemoryNotificationReplies) PutNotificationReplyRef(_ context.Context, r NotificationReplyRef) error {
	if !r.complete() {
		return errIncompleteReplyRef
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	k := replyRefKey{r.Notifier, r.Ref, r.Recipient}
	if have, ok := m.refs[k]; ok {
		if have.TokenHash != r.TokenHash {
			return ErrNotificationReplyRefConflict
		}
		return nil
	}
	m.refs[k] = r
	return nil
}

func (m *MemoryNotificationReplies) GetNotificationReplyRef(_ context.Context, notifier, ref, recipient string) (NotificationReplyRef, bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.refs[replyRefKey{notifier, ref, recipient}]
	return r, ok, nil
}

func (m *MemoryNotificationReplies) deleteRefsLocked(notifier string) {
	for k := range m.refs {
		if k.notifier == notifier {
			delete(m.refs, k)
		}
	}
}

func (m *MemoryNotificationReplies) pruneRefsLocked(beforeMs int64, limit int) {
	n := 0
	for k, r := range m.refs {
		if n >= limit {
			return
		}
		if !liveAt(r.ExpiresAtMs, beforeMs) {
			delete(m.refs, k)
			n++
		}
	}
}

func (m *MySQLNotificationReplies) PutNotificationReplyRef(ctx context.Context, r NotificationReplyRef) error {
	if !r.complete() {
		return errIncompleteReplyRef
	}
	res, err := m.DB.ExecContext(ctx,
		`INSERT IGNORE INTO notification_reply_refs
		   (notifier, ref_id, recipient, token_hash, created_at_ms, expires_at_ms)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		r.Notifier, r.Ref, r.Recipient, r.TokenHash, r.CreatedAtMs, r.ExpiresAtMs)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err != nil || n == 1 {
		return err
	}
	have, ok, err := m.GetNotificationReplyRef(ctx, r.Notifier, r.Ref, r.Recipient)
	switch {
	case err != nil:
		return err
	case !ok:
		// Pruned between the insert and the read: nothing holds the key now.
		return m.PutNotificationReplyRef(ctx, r)
	case have.TokenHash != r.TokenHash:
		return ErrNotificationReplyRefConflict
	}
	return nil
}

func (m *MySQLNotificationReplies) GetNotificationReplyRef(ctx context.Context, notifier, ref, recipient string) (NotificationReplyRef, bool, error) {
	var r NotificationReplyRef
	err := m.DB.QueryRowContext(ctx,
		`SELECT notifier, ref_id, recipient, token_hash, created_at_ms, expires_at_ms
		 FROM notification_reply_refs WHERE notifier=? AND ref_id=? AND recipient=?`, notifier, ref, recipient).
		Scan(&r.Notifier, &r.Ref, &r.Recipient, &r.TokenHash, &r.CreatedAtMs, &r.ExpiresAtMs)
	if errors.Is(err, sql.ErrNoRows) {
		return NotificationReplyRef{}, false, nil
	}
	if err != nil {
		return NotificationReplyRef{}, false, err
	}
	return r, true, nil
}

func (m *MySQLNotificationReplies) deleteRefs(ctx context.Context, notifier string) error {
	_, err := m.DB.ExecContext(ctx, `DELETE FROM notification_reply_refs WHERE notifier=?`, notifier)
	return err
}

func (m *MySQLNotificationReplies) pruneRefs(ctx context.Context, beforeMs int64, limit int) error {
	_, err := m.DB.ExecContext(ctx,
		`DELETE FROM notification_reply_refs WHERE expires_at_ms <= ? LIMIT ?`, beforeMs, limit)
	return err
}
