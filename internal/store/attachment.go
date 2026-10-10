package store

import (
	"context"
	"database/sql"
	"errors"
	"sync"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// Attachment is one uploaded chat attachment's metadata. The binary itself
// lives in the blob store keyed by BlobKey; this row carries only what the feed
// and the download endpoint need, scoped to the room it was uploaded to.
type Attachment struct {
	ID          string
	RoomID      string
	Uploader    string
	Filename    string
	ContentType string
	Size        int64
	SHA256      string
	BlobKey     string
	CreatedAtMs int64
}

// AttachmentStore persists attachment metadata. Rows are removed only by data
// retention (RetentionStore) and their blobs only by the blob collection,
// which deletes a content-addressed blob once no row references it (a blob
// may back several rows). Implementations must be safe for concurrent use.
type AttachmentStore interface {
	PutAttachment(ctx context.Context, a Attachment) error
	GetAttachment(ctx context.Context, roomID, id string) (Attachment, bool, error)
}

// MemoryAttachments is an in-memory AttachmentStore for dev/tests.
type MemoryAttachments struct {
	mu sync.Mutex
	m  map[string]Attachment
}

func NewMemoryAttachments() *MemoryAttachments {
	return &MemoryAttachments{m: map[string]Attachment{}}
}

var _ AttachmentStore = (*MemoryAttachments)(nil)

func memAttachmentKey(tid, roomID, id string) string {
	return tid + "\x00" + roomID + "\x00" + id
}

func (s *MemoryAttachments) PutAttachment(ctx context.Context, a Attachment) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.m[memAttachmentKey(tid, a.RoomID, a.ID)] = a
	return nil
}

func (s *MemoryAttachments) GetAttachment(ctx context.Context, roomID, id string) (Attachment, bool, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return Attachment{}, false, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	a, ok := s.m[memAttachmentKey(tid, roomID, id)]
	return a, ok, nil
}

// MySQLAttachments implements AttachmentStore over database/sql.
type MySQLAttachments struct{ DB *sql.DB }

func NewMySQLAttachments(db *sql.DB) *MySQLAttachments {
	return &MySQLAttachments{DB: db}
}

var _ AttachmentStore = (*MySQLAttachments)(nil)

func (s *MySQLAttachments) PutAttachment(ctx context.Context, a Attachment) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	_, err = s.DB.ExecContext(ctx, `INSERT INTO attachments
  (id, tenant_id, room_id, uploader, filename, content_type, size_bytes, sha256, blob_key, created_at_ms)
  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
  ON DUPLICATE KEY UPDATE id = id`,
		a.ID, tid, a.RoomID, a.Uploader, a.Filename, a.ContentType, a.Size, a.SHA256, a.BlobKey, a.CreatedAtMs)
	return err
}

func (s *MySQLAttachments) GetAttachment(ctx context.Context, roomID, id string) (Attachment, bool, error) {
	// Attachment id is the primary key (globally unique), so the lookup is keyed
	// on (room_id, id) alone, not the caller's tenant. This lets a member whose
	// token was minted in a different tenant than the room (a multi-tenant
	// member, or the bridge acting as one) read an attachment they are already
	// authorized for; authorization is enforced upstream by the room-membership
	// check, not by this row lookup.
	var a Attachment
	err := s.DB.QueryRowContext(ctx, `SELECT id, room_id, uploader, filename, content_type, size_bytes, sha256, blob_key, created_at_ms
  FROM attachments WHERE room_id = ? AND id = ?`, roomID, id).Scan(
		&a.ID, &a.RoomID, &a.Uploader, &a.Filename, &a.ContentType, &a.Size, &a.SHA256, &a.BlobKey, &a.CreatedAtMs,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Attachment{}, false, nil
	}
	if err != nil {
		return Attachment{}, false, err
	}
	return a, true, nil
}
