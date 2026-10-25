package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"sync"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// ErrAttachmentExists refuses an attachment id already taken: ids are global
// (the primary key carries no tenant), so a put never rewrites a row.
var ErrAttachmentExists = errors.New("store: attachment id already exists")

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

// AttachmentStore persists attachment metadata in the context tenant. Rows
// are removed only by data retention (RetentionStore) and their blobs only
// by the blob collection, which deletes a content-addressed blob once no row
// references it (a blob may back several rows). Implementations must be
// safe for concurrent use.
type AttachmentStore interface {
	// PutAttachment inserts a row; ErrAttachmentExists when the id is taken
	// in any room or tenant (insert-only).
	PutAttachment(ctx context.Context, a Attachment) error
	// GetAttachment returns the ctx tenant's attachment of the room;
	// found=false for an unknown id, another room's or another tenant's.
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
	// The id alone is the key on MySQL: a taken id in any room or tenant
	// refuses the put.
	for k := range s.m {
		if strings.HasSuffix(k, "\x00"+a.ID) {
			return ErrAttachmentExists
		}
	}
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
type MySQLAttachments struct{ DB *DB }

func NewMySQLAttachments(db *DB) *MySQLAttachments {
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
  VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		a.ID, tid, a.RoomID, a.Uploader, a.Filename, a.ContentType, a.Size, a.SHA256, a.BlobKey, a.CreatedAtMs)
	return dupAs(err, ErrAttachmentExists)
}

// GetAttachment reads in the ctx tenant, like every room-scoped read: a
// request for a room of another tenant than the caller's token already runs
// in the room's tenant (the gateway's room tenant scope).
func (s *MySQLAttachments) GetAttachment(ctx context.Context, roomID, id string) (Attachment, bool, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return Attachment{}, false, err
	}
	var a Attachment
	err = s.DB.QueryRowContext(ctx, `SELECT id, room_id, uploader, filename, content_type, size_bytes, sha256, blob_key, created_at_ms
  FROM attachments WHERE tenant_id = ? AND room_id = ? AND id = ?`, tid, roomID, id).Scan(
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
