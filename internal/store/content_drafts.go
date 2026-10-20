package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"
)

// Content drafts (docs/prd/2026-10-12-experts-and-teams.md, "Runtime gap
// closure" 4): an unsaved expert or skill pack an agent drafted in its
// workspace (the Expert builder), staged so another agent of the room can
// wear it for one quick try (the room tools' components try). A draft is not a
// content component: no Library, listing, search, visibility rule or usage
// count reads this table. It is scoped to the job that staged it (only a
// job that job dispatches may wear it, in its room) and expires; the
// retention pass deletes expired rows and blob collection then reclaims the
// bundle.

// ContentDraftIDPrefix starts every draft id; a job whose expert id has it
// wears a draft (contentuse.Effective).
const ContentDraftIDPrefix = "cd_"

// ContentDraftTTL is how long a staged draft can be worn: long enough for a
// queued dispatch to start and run (the room tools wait at most 20
// minutes), short enough that a draft is never a second library. A product
// constant of the try flow, not deployment data.
const ContentDraftTTL = time.Hour

// IsContentDraftID reports whether id names a staged draft.
func IsContentDraftID(id string) bool { return strings.HasPrefix(id, ContentDraftIDPrefix) }

// ContentDraft is one staged draft.
type ContentDraft struct {
	ID       string
	TenantID string
	RoomID   string
	// JobID is the job whose agent staged it: only jobs it dispatches wear
	// the draft.
	JobID string
	// Requester is the person the staging job serves; Author is the agent
	// owner whose agent wrote the files (the trust owner: someone else's
	// script-bearing draft forces the sandbox).
	Requester     string
	Author        string
	Kind          string
	Name          string
	DisplayName   string
	Version       string
	ManifestJSON  json.RawMessage
	BundleBlobKey string
	SHA256        string
	SizeBytes     int64
	CreatedAtMs   int64
	ExpiresAtMs   int64
}

// Live reports whether the draft may still be worn at nowMs (liveAt).
func (d ContentDraft) Live(nowMs int64) bool { return liveAt(d.ExpiresAtMs, nowMs) }

// ErrContentDraftExists rejects a second draft under one id.
var ErrContentDraftExists = errors.New("store: content draft exists")

// ContentDraftStore persists staged drafts (part of the content store).
type ContentDraftStore interface {
	// PutContentDraft stages d; a taken id is ErrContentDraftExists.
	PutContentDraft(ctx context.Context, d ContentDraft) error
	// GetContentDraft returns a draft, expired or not (ErrContentNotFound
	// once the retention pass deleted it).
	GetContentDraft(ctx context.Context, id string) (ContentDraft, error)
}

func (s *MemoryContentComponents) PutContentDraft(_ context.Context, d ContentDraft) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.drafts == nil {
		s.drafts = map[string]ContentDraft{}
	}
	if _, taken := s.drafts[d.ID]; taken {
		return ErrContentDraftExists
	}
	s.drafts[d.ID] = d
	return nil
}

func (s *MemoryContentComponents) GetContentDraft(_ context.Context, id string) (ContentDraft, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	d, ok := s.drafts[id]
	if !ok {
		return ContentDraft{}, ErrContentNotFound
	}
	return d, nil
}

// purgeExpiredDrafts deletes drafts expired at nowMs (the retention pass).
func (s *MemoryContentComponents) purgeExpiredDrafts(nowMs int64) int64 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var n int64
	for id, d := range s.drafts {
		if !d.Live(nowMs) {
			delete(s.drafts, id)
			n++
		}
	}
	return n
}
