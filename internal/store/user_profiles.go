package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"unicode"
	"unicode/utf8"
)

// Display names (docs/prd/2026-10-10-display-names-and-guided-onboarding.md
// §1). A user id is the identity (usually an e-mail address) everywhere —
// mentions, recipients, permissions, URLs; the display name is only what a
// person is shown as. It is global per user (not per tenant), optional, and
// answered for any id, never with more than the name itself.

// MaxDisplayNameRunes is the longest display name accepted, in characters
// (Unicode code points, which is also how MySQL sizes VARCHAR(40)).
const MaxDisplayNameRunes = 40

// ErrInvalidDisplayName is a display name NormalizeDisplayName refuses; the
// wrapped message says why and is safe to show the user.
var ErrInvalidDisplayName = errors.New("invalid display name")

// NormalizeDisplayName validates a user-entered display name and returns it
// trimmed. An empty (or all-space) value is valid and means "no display
// name" (the default applies). Otherwise it must be at most
// MaxDisplayNameRunes characters of valid UTF-8 with no control characters.
func NormalizeDisplayName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if !utf8.ValidString(name) {
		return "", fmt.Errorf("%w: not valid UTF-8", ErrInvalidDisplayName)
	}
	if utf8.RuneCountInString(name) > MaxDisplayNameRunes {
		return "", fmt.Errorf("%w: at most %d characters", ErrInvalidDisplayName, MaxDisplayNameRunes)
	}
	if strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", fmt.Errorf("%w: control characters are not allowed", ErrInvalidDisplayName)
	}
	return name, nil
}

// DisplayName is THE display-name rule: the stored name when one is set,
// otherwise the local part of the user id (before '@') — or the id itself
// when it has no '@' (or nothing before it). Every surface that names a
// person (the profiles route, the public share page's "shared by") goes
// through this one function.
func DisplayName(userID, stored string) string {
	if stored != "" {
		return stored
	}
	userID = strings.TrimSpace(userID)
	if at := strings.IndexByte(userID, '@'); at > 0 {
		return userID[:at]
	}
	return userID
}

// UserProfileStore persists users' chosen display names. It is global (not
// per tenant). User ids compare exactly (D2: a case variant is another
// subject).
type UserProfileStore interface {
	// DisplayNames returns the stored display names of ids, keyed by the id
	// as given; an id with no stored name is absent (apply DisplayName).
	DisplayNames(ctx context.Context, ids []string) (map[string]string, error)
	// SetDisplayName stores userID's display name (already normalized);
	// an empty name deletes it, restoring the default.
	SetDisplayName(ctx context.Context, userID, name string, nowMs int64) error
}

// MemoryUserProfiles is the in-memory UserProfileStore.
type MemoryUserProfiles struct {
	mu    sync.Mutex
	names map[string]string // user id -> display name
}

// NewMemoryUserProfiles returns an empty in-memory profile store.
func NewMemoryUserProfiles() *MemoryUserProfiles {
	return &MemoryUserProfiles{names: map[string]string{}}
}

var _ UserProfileStore = (*MemoryUserProfiles)(nil)

// DisplayNames implements UserProfileStore.
func (s *MemoryUserProfiles) DisplayNames(_ context.Context, ids []string) (map[string]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make(map[string]string, len(ids))
	for _, id := range ids {
		if name, ok := s.names[id]; ok {
			out[id] = name
		}
	}
	return out, nil
}

// SetDisplayName implements UserProfileStore.
func (s *MemoryUserProfiles) SetDisplayName(_ context.Context, userID, name string, _ int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if name == "" {
		delete(s.names, userID)
		return nil
	}
	s.names[userID] = name
	return nil
}

// MySQLUserProfiles is the MySQL UserProfileStore (table user_profiles).
type MySQLUserProfiles struct{ DB *sql.DB }

// NewMySQLUserProfiles returns a MySQL-backed profile store.
func NewMySQLUserProfiles(db *sql.DB) *MySQLUserProfiles { return &MySQLUserProfiles{DB: db} }

var _ UserProfileStore = (*MySQLUserProfiles)(nil)

// DisplayNames implements UserProfileStore in one query (user ids compare
// exactly: the column is utf8mb4_bin).
func (s *MySQLUserProfiles) DisplayNames(ctx context.Context, ids []string) (map[string]string, error) {
	out := make(map[string]string, len(ids))
	if len(ids) == 0 {
		return out, nil
	}
	args := make([]any, len(ids))
	for i, id := range ids {
		args[i] = id
	}
	rows, err := s.DB.QueryContext(ctx,
		`SELECT user_id, display_name FROM user_profiles WHERE user_id IN (`+inPlaceholders(len(ids))+`)`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	for rows.Next() {
		var id, name string
		if err := rows.Scan(&id, &name); err != nil {
			return nil, err
		}
		out[id] = name
	}
	return out, rows.Err()
}

// SetDisplayName implements UserProfileStore.
func (s *MySQLUserProfiles) SetDisplayName(ctx context.Context, userID, name string, nowMs int64) error {
	if name == "" {
		_, err := s.DB.ExecContext(ctx, `DELETE FROM user_profiles WHERE user_id = ?`, userID)
		return err
	}
	_, err := s.DB.ExecContext(ctx, `INSERT INTO user_profiles (user_id, display_name, updated_at_ms) VALUES (?, ?, ?)
    ON DUPLICATE KEY UPDATE display_name = VALUES(display_name), updated_at_ms = VALUES(updated_at_ms)`, userID, name, nowMs)
	return err
}
