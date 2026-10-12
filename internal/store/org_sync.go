package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"sort"
	"sync"
)

// OrgSyncSubject is the directory attributes an auth provider last asserted
// for a user (organization sync, R11). Sign-in records them so a resync can
// re-apply changed mapping rules without waiting for the next sign-in.
type OrgSyncSubject struct {
	UserID      string
	Email       string
	DisplayName string
	Department  string
	Groups      []string
	SyncedAtMs  int64
}

// OrgSyncStore persists organization-sync state: the last asserted attributes
// per subject and the default rooms sync already added a user to (a default
// room is added once; a member who leaves it is not re-added). Implementations
// must be safe for concurrent use.
type OrgSyncStore interface {
	// PutOrgSyncSubject upserts a subject's attributes (every field
	// replaced; nil Groups are stored as []).
	PutOrgSyncSubject(ctx context.Context, s OrgSyncSubject) error
	// ListOrgSyncSubjects returns every subject, by user id.
	ListOrgSyncSubjects(ctx context.Context) ([]OrgSyncSubject, error)
	DeleteOrgSyncSubject(ctx context.Context, userID string) error
	// DefaultRoomGranted reports whether sync already added userID to roomID.
	DefaultRoomGranted(ctx context.Context, userID, roomID string) (bool, error)
	// RecordDefaultRoom records that sync added userID to roomID (idempotent).
	RecordDefaultRoom(ctx context.Context, userID, roomID string, atMs int64) error
}

// MemoryOrgSync is the in-memory OrgSyncStore (dev / tests).
type MemoryOrgSync struct {
	mu       sync.Mutex
	subjects map[string]OrgSyncSubject
	rooms    map[string]bool // user\x00room
}

// NewMemoryOrgSync constructs an empty in-memory org-sync store.
func NewMemoryOrgSync() *MemoryOrgSync {
	return &MemoryOrgSync{subjects: map[string]OrgSyncSubject{}, rooms: map[string]bool{}}
}

var _ OrgSyncStore = (*MemoryOrgSync)(nil)

func (s *MemoryOrgSync) PutOrgSyncSubject(_ context.Context, sub OrgSyncSubject) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	sub.Groups = append([]string{}, sub.Groups...)
	s.subjects[sub.UserID] = sub
	return nil
}

func (s *MemoryOrgSync) ListOrgSyncSubjects(_ context.Context) ([]OrgSyncSubject, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]OrgSyncSubject, 0, len(s.subjects))
	for _, sub := range s.subjects {
		sub.Groups = append([]string{}, sub.Groups...)
		out = append(out, sub)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].UserID < out[j].UserID })
	return out, nil
}

func (s *MemoryOrgSync) DeleteOrgSyncSubject(_ context.Context, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.subjects, userID)
	return nil
}

func (s *MemoryOrgSync) DefaultRoomGranted(_ context.Context, userID, roomID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rooms[userID+"\x00"+roomID], nil
}

func (s *MemoryOrgSync) RecordDefaultRoom(_ context.Context, userID, roomID string, _ int64) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.rooms[userID+"\x00"+roomID] = true
	return nil
}

// MySQLOrgSync implements OrgSyncStore over database/sql.
type MySQLOrgSync struct{ DB *sql.DB }

// NewMySQLOrgSync constructs a MySQL-backed org-sync store.
func NewMySQLOrgSync(db *sql.DB) *MySQLOrgSync { return &MySQLOrgSync{DB: db} }

var _ OrgSyncStore = (*MySQLOrgSync)(nil)

func (s *MySQLOrgSync) PutOrgSyncSubject(ctx context.Context, sub OrgSyncSubject) error {
	groups := sub.Groups
	if groups == nil {
		groups = []string{}
	}
	raw, err := jsonArg(groups)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `
INSERT INTO org_sync_subjects (user_id, email, display_name, department, groups_json, synced_at_ms)
VALUES (?, ?, ?, ?, ?, ?)
ON DUPLICATE KEY UPDATE email = VALUES(email), display_name = VALUES(display_name),
  department = VALUES(department), groups_json = VALUES(groups_json), synced_at_ms = VALUES(synced_at_ms)`,
		sub.UserID, sub.Email, sub.DisplayName, sub.Department, raw, sub.SyncedAtMs)
	return err
}

func (s *MySQLOrgSync) ListOrgSyncSubjects(ctx context.Context) ([]OrgSyncSubject, error) {
	return queryAll(ctx, s.DB, `
SELECT user_id, email, display_name, department, groups_json, synced_at_ms FROM org_sync_subjects ORDER BY user_id`,
		func(row rowScanner) (OrgSyncSubject, error) {
			var sub OrgSyncSubject
			var raw string
			if err := row.Scan(&sub.UserID, &sub.Email, &sub.DisplayName, &sub.Department, &raw, &sub.SyncedAtMs); err != nil {
				return sub, err
			}
			sub.Groups = []string{}
			return sub, json.Unmarshal([]byte(raw), &sub.Groups)
		})
}

func (s *MySQLOrgSync) DeleteOrgSyncSubject(ctx context.Context, userID string) error {
	_, err := s.DB.ExecContext(ctx, `DELETE FROM org_sync_subjects WHERE user_id = ?`, userID)
	return err
}

func (s *MySQLOrgSync) DefaultRoomGranted(ctx context.Context, userID, roomID string) (bool, error) {
	var n int
	err := s.DB.QueryRowContext(ctx, `
SELECT COUNT(*) FROM org_sync_default_rooms WHERE user_id = ? AND room_id = ?`, userID, roomID).Scan(&n)
	return n > 0, err
}

func (s *MySQLOrgSync) RecordDefaultRoom(ctx context.Context, userID, roomID string, atMs int64) error {
	_, err := s.DB.ExecContext(ctx, `
INSERT IGNORE INTO org_sync_default_rooms (user_id, room_id, granted_at_ms) VALUES (?, ?, ?)`, userID, roomID, atMs)
	return err
}
