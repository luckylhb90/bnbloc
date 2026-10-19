package store

import (
	"context"
	"database/sql"
	"errors"
)

// A work version's stored cover (docs/prd/2026-10-19-work-covers.md §2.3):
// a published work is immutable, so a version published without a cover
// gets one beside its bundle the first time someone opens it, as a room
// version gets its thumbnail (artifact_versions.thumb_*). The bytes are a
// blob (blob_key, counted by blob collection); the gateway checks them
// (tarbundle.CoverImage) and serves the bundle's own cover before this one.

// Stored cover sources, lowest rank first. A version keeps the first stored
// cover; a later one replaces it only from a strictly higher-ranked source.
const (
	// ContentCoverRender: drawn by any reader's viewer (an image scaled, a
	// PDF's first page, a video's frame).
	ContentCoverRender = "render"
	// ContentCoverAI: drawn by the image model for the work's author.
	ContentCoverAI = "ai"
)

// ContentCoverRank orders stored cover sources; 0 = unknown source.
func ContentCoverRank(source string) int {
	switch source {
	case ContentCoverRender:
		return 1
	case ContentCoverAI:
		return 2
	}
	return 0
}

// ContentVersionCover is one work version's stored cover.
type ContentVersionCover struct {
	ComponentID string
	Version     string
	BlobKey     string
	MimeType    string
	Rank        int
	CreatedBy   string
	CreatedAtMs int64
}

// ContentVersionCoverStore keeps the versions' stored covers. Content ids
// are global (no tenant): the gateway decides who may read or write one. A
// component's covers are deleted with it.
type ContentVersionCoverStore interface {
	// GetContentVersionCover returns a version's stored cover (found=false
	// for none).
	GetContentVersionCover(ctx context.Context, componentID, version string) (ContentVersionCover, bool, error)
	// SetContentVersionCover stores c when its version has no cover or one
	// of a lower rank. It returns the cover the version keeps and whether
	// this write landed; a version that does not exist is
	// ErrContentNotFound.
	SetContentVersionCover(ctx context.Context, c ContentVersionCover) (ContentVersionCover, bool, error)
}

var (
	_ ContentVersionCoverStore = (*MemoryContentComponents)(nil)
	_ ContentVersionCoverStore = (*MySQLContentComponents)(nil)
)

func coverKey(componentID, version string) string { return componentID + "\x00" + version }

func (s *MemoryContentComponents) GetContentVersionCover(_ context.Context, componentID, version string) (ContentVersionCover, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	c, ok := s.covers[coverKey(componentID, version)]
	return c, ok, nil
}

func (s *MemoryContentComponents) SetContentVersionCover(_ context.Context, c ContentVersionCover) (ContentVersionCover, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	found := false
	for _, v := range s.versions[c.ComponentID] {
		found = found || v.Version == c.Version
	}
	if !found {
		return ContentVersionCover{}, false, ErrContentNotFound
	}
	k := coverKey(c.ComponentID, c.Version)
	if kept, ok := s.covers[k]; ok && kept.Rank >= c.Rank {
		return kept, false, nil
	}
	s.covers[k] = c
	return c, true, nil
}

const contentCoverCols = `component_id, version, blob_key, mime_type, cover_rank, created_by, created_at_ms`

func (s *MySQLContentComponents) GetContentVersionCover(ctx context.Context, componentID, version string) (ContentVersionCover, bool, error) {
	var c ContentVersionCover
	err := s.DB.QueryRowContext(ctx, `SELECT `+contentCoverCols+` FROM content_version_covers WHERE component_id = ? AND version = ?`, componentID, version).
		Scan(&c.ComponentID, &c.Version, &c.BlobKey, &c.MimeType, &c.Rank, &c.CreatedBy, &c.CreatedAtMs)
	if errors.Is(err, sql.ErrNoRows) {
		return ContentVersionCover{}, false, nil
	}
	return c, err == nil, err
}

// SetContentVersionCover inserts the cover for an existing version (a
// duplicate is ignored, so it counts no row even with clientFoundRows),
// else replaces a lower-ranked one in one conditional UPDATE, so concurrent
// writers settle on the highest-ranked (then first) cover without a read
// race.
func (s *MySQLContentComponents) SetContentVersionCover(ctx context.Context, c ContentVersionCover) (ContentVersionCover, bool, error) {
	res, err := s.DB.ExecContext(ctx, `INSERT IGNORE INTO content_version_covers (`+contentCoverCols+`)
  SELECT ?, ?, ?, ?, ?, ?, ? FROM content_component_versions WHERE component_id = ? AND version = ?`,
		c.ComponentID, c.Version, c.BlobKey, c.MimeType, c.Rank, c.CreatedBy, c.CreatedAtMs, c.ComponentID, c.Version)
	if err != nil {
		return ContentVersionCover{}, false, err
	}
	landed, _ := res.RowsAffected()
	if landed == 0 {
		if res, err = s.DB.ExecContext(ctx, `UPDATE content_version_covers SET blob_key = ?, mime_type = ?, cover_rank = ?, created_by = ?, created_at_ms = ?
  WHERE component_id = ? AND version = ? AND cover_rank < ?`,
			c.BlobKey, c.MimeType, c.Rank, c.CreatedBy, c.CreatedAtMs, c.ComponentID, c.Version, c.Rank); err != nil {
			return ContentVersionCover{}, false, err
		}
		landed, _ = res.RowsAffected()
	}
	kept, found, err := s.GetContentVersionCover(ctx, c.ComponentID, c.Version)
	if err != nil {
		return ContentVersionCover{}, false, err
	}
	if !found {
		return ContentVersionCover{}, false, ErrContentNotFound
	}
	return kept, landed > 0, nil
}
