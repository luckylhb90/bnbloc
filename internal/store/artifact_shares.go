package store

import (
	"context"
	"errors"
	"sort"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// ErrArtifactShareNotFound reports an unknown share id (or one outside the
// context tenant).
var ErrArtifactShareNotFound = errors.New("store: artifact share not found")

// ArtifactShare is one public share link of an artifact
// (docs/prd/2026-10-09-artifact-shares-and-site-previews.md). The token
// "bsh_<ShareID>_<secret>" is shown once at create; only TokenHash (the
// secret's sha256 hex) is stored.
type ArtifactShare struct {
	ShareID    string
	TokenHash  string
	TenantID   string
	RoomID     string
	ArtifactID string
	// Version is the pinned version; 0 follows the artifact's latest.
	Version     int
	CreatedBy   string
	CreatedAtMs int64
	ExpiresAtMs int64
	// RevokedAtMs is 0 while the link is not revoked.
	RevokedAtMs    int64
	RevokedBy      string
	ViewCount      int64
	LastViewedAtMs int64
}

// Active reports whether the link itself is live at nowMs (not revoked, not
// expired). Tenant policy and the artifact's existence are checked by the
// caller.
func (s ArtifactShare) Active(nowMs int64) bool {
	return s.RevokedAtMs == 0 && nowMs < s.ExpiresAtMs
}

// endedAtMs is when the link stopped working: its revocation or expiry,
// whichever came first.
func (s ArtifactShare) endedAtMs() int64 {
	if s.RevokedAtMs != 0 && s.RevokedAtMs < s.ExpiresAtMs {
		return s.RevokedAtMs
	}
	return s.ExpiresAtMs
}

// TenantArtifactShare is a share as the org-admin list shows it: with the
// artifact's title.
type TenantArtifactShare struct {
	ArtifactShare
	ArtifactTitle string
}

// TenantShareQuery selects the context tenant's shares, newest first.
type TenantShareQuery struct {
	// ActiveAtMs > 0 returns only links active at that time.
	ActiveAtMs int64
	// EndedAfterMs drops links that stopped working at or before it.
	EndedAfterMs int64
	// Limit bounds the result (1..500, default 500).
	Limit int
}

// ArtifactShareStore persists share links. Every method except
// ResolveArtifactShare is scoped to the context tenant.
type ArtifactShareStore interface {
	// CreateArtifactShare stores a new link in the context tenant.
	CreateArtifactShare(ctx context.Context, sh ArtifactShare) (ArtifactShare, error)
	// GetArtifactShare returns one link of the context tenant.
	GetArtifactShare(ctx context.Context, shareID string) (ArtifactShare, error)
	// ListArtifactShares returns an artifact's links that stopped working
	// after endedAfterMs (active ones included), newest first (≤ 200).
	ListArtifactShares(ctx context.Context, roomID, artifactID string, endedAfterMs int64) ([]ArtifactShare, error)
	// ListTenantArtifactShares lists the context tenant's links of artifacts
	// that still exist, with their titles.
	ListTenantArtifactShares(ctx context.Context, q TenantShareQuery) ([]TenantArtifactShare, error)
	// RevokeArtifactShare revokes a link (idempotent: the first revocation
	// is kept) and returns it as stored.
	RevokeArtifactShare(ctx context.Context, shareID, by string, atMs int64) (ArtifactShare, error)
	// ResolveArtifactShare looks a link up by id in ANY tenant: the token
	// carries no tenant, and share ids are globally unique (the primary
	// key). The caller must verify the token secret against TokenHash
	// before trusting the row, and then works in the row's tenant.
	ResolveArtifactShare(ctx context.Context, shareID string) (ArtifactShare, error)
	// CountArtifactShareView records one view of a link of the context
	// tenant.
	CountArtifactShareView(ctx context.Context, shareID string, atMs int64) error
}

const (
	maxArtifactShareList       = 200
	maxTenantArtifactShareList = 500
)

func tenantShareLimit(n int) int {
	if n <= 0 || n > maxTenantArtifactShareList {
		return maxTenantArtifactShareList
	}
	return n
}

var _ ArtifactShareStore = (*MemoryArtifacts)(nil)

func (s *MemoryArtifacts) CreateArtifactShare(ctx context.Context, sh ArtifactShare) (ArtifactShare, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return ArtifactShare{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.shares[sh.ShareID]; dup {
		return ArtifactShare{}, errors.New("store: duplicate share id")
	}
	sh.TenantID = tid
	sh.RevokedAtMs, sh.RevokedBy, sh.ViewCount, sh.LastViewedAtMs = 0, "", 0, 0
	s.shares[sh.ShareID] = sh
	return sh, nil
}

func (s *MemoryArtifacts) GetArtifactShare(ctx context.Context, shareID string) (ArtifactShare, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return ArtifactShare{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sh, ok := s.shares[shareID]
	if !ok || sh.TenantID != tid {
		return ArtifactShare{}, ErrArtifactShareNotFound
	}
	return sh, nil
}

func (s *MemoryArtifacts) ListArtifactShares(ctx context.Context, roomID, artifactID string, endedAfterMs int64) ([]ArtifactShare, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []ArtifactShare{}
	for _, sh := range s.shares {
		if sh.TenantID == tid && sh.RoomID == roomID && sh.ArtifactID == artifactID && sh.endedAtMs() > endedAfterMs {
			out = append(out, sh)
		}
	}
	sortSharesNewestFirst(out, func(i int) ArtifactShare { return out[i] })
	if len(out) > maxArtifactShareList {
		out = out[:maxArtifactShareList]
	}
	return out, nil
}

func (s *MemoryArtifacts) ListTenantArtifactShares(ctx context.Context, q TenantShareQuery) ([]TenantArtifactShare, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []TenantArtifactShare{}
	for _, sh := range s.shares {
		if sh.TenantID != tid || sh.endedAtMs() <= q.EndedAfterMs || (q.ActiveAtMs > 0 && !sh.Active(q.ActiveAtMs)) {
			continue
		}
		a, ok := s.artifacts[sh.ArtifactID]
		if !ok || a.TenantID != tid {
			continue
		}
		out = append(out, TenantArtifactShare{ArtifactShare: sh, ArtifactTitle: a.Title})
	}
	sortSharesNewestFirst(out, func(i int) ArtifactShare { return out[i].ArtifactShare })
	if limit := tenantShareLimit(q.Limit); len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// sortSharesNewestFirst orders by (createdAtMs, shareId) descending, the
// MySQL list order.
func sortSharesNewestFirst[T any](list []T, at func(int) ArtifactShare) {
	sort.SliceStable(list, func(i, j int) bool {
		a, b := at(i), at(j)
		if a.CreatedAtMs != b.CreatedAtMs {
			return a.CreatedAtMs > b.CreatedAtMs
		}
		return a.ShareID > b.ShareID
	})
}

func (s *MemoryArtifacts) RevokeArtifactShare(ctx context.Context, shareID, by string, atMs int64) (ArtifactShare, error) {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return ArtifactShare{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sh, ok := s.shares[shareID]
	if !ok || sh.TenantID != tid {
		return ArtifactShare{}, ErrArtifactShareNotFound
	}
	if sh.RevokedAtMs == 0 {
		sh.RevokedAtMs, sh.RevokedBy = atMs, by
		s.shares[shareID] = sh
	}
	return sh, nil
}

func (s *MemoryArtifacts) ResolveArtifactShare(_ context.Context, shareID string) (ArtifactShare, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	sh, ok := s.shares[shareID]
	if !ok {
		return ArtifactShare{}, ErrArtifactShareNotFound
	}
	return sh, nil
}

func (s *MemoryArtifacts) CountArtifactShareView(ctx context.Context, shareID string, atMs int64) error {
	tid, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	sh, ok := s.shares[shareID]
	if !ok || sh.TenantID != tid {
		return ErrArtifactShareNotFound
	}
	sh.ViewCount++
	sh.LastViewedAtMs = atMs
	s.shares[shareID] = sh
	return nil
}
