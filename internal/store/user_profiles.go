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

	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Display names (docs/prd/2026-10-10-display-names-and-guided-onboarding.md
// §1). A user id is the identity (usually an e-mail address) everywhere —
// mentions, recipients, permissions, URLs; the display name is only what a
// person is shown as. It is global per user (not per tenant), optional, and
// answered for any id, never with more than the name itself.

// ErrInvalidDisplayName is a display name NormalizeDisplayName refuses; the
// wrapped message says why and is safe to show the user.
var ErrInvalidDisplayName = errors.New("invalid display name")

// NormalizeDisplayName validates a user-entered display name and returns it
// trimmed. An empty (or all-space) value is valid and means "no display
// name" (the default applies). Otherwise it must be at most
// wire.MaxDisplayNameRunes characters of valid UTF-8 with no control characters.
func NormalizeDisplayName(raw string) (string, error) {
	name := strings.TrimSpace(raw)
	if !utf8.ValidString(name) {
		return "", fmt.Errorf("%w: not valid UTF-8", ErrInvalidDisplayName)
	}
	if utf8.RuneCountInString(name) > wire.MaxDisplayNameRunes {
		return "", fmt.Errorf("%w: at most %d characters", ErrInvalidDisplayName, wire.MaxDisplayNameRunes)
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
	// an empty name restores the default.
	SetDisplayName(ctx context.Context, userID, name string, nowMs int64) error
	// SetEmail records the email the identity provider asserted for userID
	// at sign-in ("" = none asserted). Sign-in is its only writer.
	SetEmail(ctx context.Context, userID, email string, nowMs int64) error
	// Email returns userID's recorded email ("" when none).
	Email(ctx context.Context, userID string) (string, error)
	// MemberPreferences returns userID's reading preferences, the defaults
	// applied for a person who never chose (FeedLayout: wire.FeedLayoutTime).
	MemberPreferences(ctx context.Context, userID string) (wire.MemberPreferences, error)
	// SetFeedLayout stores userID's feed layout (a wire.FeedLayout value,
	// already checked with wire.ValidFeedLayout). The name and email stay.
	SetFeedLayout(ctx context.Context, userID, layout string, nowMs int64) error
}

// MaxEmailLen bounds a recorded email (the RFC 5321 path limit, rounded up).
const MaxEmailLen = 320

// MemoryUserProfiles is the in-memory UserProfileStore.
type MemoryUserProfiles struct {
	mu      sync.Mutex
	names   map[string]string // user id -> display name
	emails  map[string]string // user id -> asserted email
	layouts map[string]string // user id -> feed layout
}

// NewMemoryUserProfiles returns an empty in-memory profile store.
func NewMemoryUserProfiles() *MemoryUserProfiles {
	return &MemoryUserProfiles{names: map[string]string{}, emails: map[string]string{}, layouts: map[string]string{}}
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

// SetEmail implements UserProfileStore.
func (s *MemoryUserProfiles) SetEmail(_ context.Context, userID, email string, _ int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if email == "" {
		delete(s.emails, userID)
		return nil
	}
	s.emails[userID] = email
	return nil
}

// Email implements UserProfileStore.
func (s *MemoryUserProfiles) Email(_ context.Context, userID string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.emails[userID], nil
}

// MemberPreferences implements UserProfileStore.
func (s *MemoryUserProfiles) MemberPreferences(_ context.Context, userID string) (wire.MemberPreferences, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if layout, ok := s.layouts[userID]; ok {
		return wire.MemberPreferences{FeedLayout: layout}, nil
	}
	return wire.MemberPreferences{FeedLayout: wire.FeedLayoutTime}, nil
}

// SetFeedLayout implements UserProfileStore.
func (s *MemoryUserProfiles) SetFeedLayout(_ context.Context, userID, layout string, _ int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.layouts[userID] = layout
	return nil
}

// MySQLUserProfiles is the MySQL UserProfileStore (table user_profiles).
type MySQLUserProfiles struct{ DB *DB }

// NewMySQLUserProfiles returns a MySQL-backed profile store.
func NewMySQLUserProfiles(db *DB) *MySQLUserProfiles { return &MySQLUserProfiles{DB: db} }

var _ UserProfileStore = (*MySQLUserProfiles)(nil)

// DisplayNames implements UserProfileStore in one query (user ids compare
// exactly: the column is utf8mb4_bin). A row holding only an email is no
// display name.
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
		`SELECT user_id, display_name FROM user_profiles WHERE display_name <> '' AND user_id IN (`+inPlaceholders(len(ids))+`)`, args...)
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
// A row holds both facts; clearing the display name keeps the email (and
// "" is the column's "none").
func (s *MySQLUserProfiles) SetDisplayName(ctx context.Context, userID, name string, nowMs int64) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO user_profiles (user_id, display_name, updated_at_ms) VALUES (?, ?, ?)
    ON DUPLICATE KEY UPDATE display_name = VALUES(display_name), updated_at_ms = VALUES(updated_at_ms)`, userID, name, nowMs)
	return err
}

// SetEmail implements UserProfileStore. It leaves the display name as it
// was (a new row starts with none).
func (s *MySQLUserProfiles) SetEmail(ctx context.Context, userID, email string, nowMs int64) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO user_profiles (user_id, display_name, email, updated_at_ms) VALUES (?, '', ?, ?)
    ON DUPLICATE KEY UPDATE email = VALUES(email), updated_at_ms = VALUES(updated_at_ms)`, userID, email, nowMs)
	return err
}

// Email implements UserProfileStore.
func (s *MySQLUserProfiles) Email(ctx context.Context, userID string) (string, error) {
	var email string
	err := s.DB.QueryRowContext(ctx, `SELECT email FROM user_profiles WHERE user_id = ?`, userID).Scan(&email)
	if errors.Is(err, sql.ErrNoRows) {
		return "", nil
	}
	return email, err
}

// MemberPreferences implements UserProfileStore. No row is the defaults; a
// row written for a name or an email holds the column default ('time').
func (s *MySQLUserProfiles) MemberPreferences(ctx context.Context, userID string) (wire.MemberPreferences, error) {
	p := wire.MemberPreferences{FeedLayout: wire.FeedLayoutTime}
	err := s.DB.QueryRowContext(ctx, `SELECT feed_layout FROM user_profiles WHERE user_id = ?`, userID).Scan(&p.FeedLayout)
	if errors.Is(err, sql.ErrNoRows) {
		return wire.MemberPreferences{FeedLayout: wire.FeedLayoutTime}, nil
	}
	return p, err
}

// SetFeedLayout implements UserProfileStore. It leaves the display name and
// email as they were (a new row starts with neither).
func (s *MySQLUserProfiles) SetFeedLayout(ctx context.Context, userID, layout string, nowMs int64) error {
	_, err := s.DB.ExecContext(ctx, `INSERT INTO user_profiles (user_id, display_name, feed_layout, updated_at_ms) VALUES (?, '', ?, ?)
    ON DUPLICATE KEY UPDATE feed_layout = VALUES(feed_layout), updated_at_ms = VALUES(updated_at_ms)`, userID, layout, nowMs)
	return err
}
