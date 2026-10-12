package store

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"sort"
	"sync"

	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// Borrow quota requests: a borrower asks an agent owner for more runs on the
// owner's room authorization, and the owner approves (raising the grant's run
// quota) or declines. The requester and the deciding owner are always the
// authenticated callers; the store additionally refuses a decision by anyone
// but the request's owner.

// QuotaRequestStatus is a quota request's lifecycle state.
type QuotaRequestStatus string

// Quota request statuses.
const (
	QuotaRequestPending  QuotaRequestStatus = "pending"
	QuotaRequestApproved QuotaRequestStatus = "approved"
	QuotaRequestDeclined QuotaRequestStatus = "declined"
)

// Quota request errors.
var (
	ErrQuotaRequestNotFound = errors.New("store: quota request not found")
	// ErrQuotaRequestPending: an open request already exists for the same
	// (room, owner, provider, requester); the existing one is returned.
	ErrQuotaRequestPending = errors.New("store: a pending quota request already exists")
	// ErrQuotaRequestDecided: the request is no longer pending.
	ErrQuotaRequestDecided = errors.New("store: quota request already decided")
	// ErrQuotaRequestExists: the request id is taken in the tenant.
	ErrQuotaRequestExists = errors.New("store: quota request exists")
)

// QuotaRequest is one borrower's request for more runs on an owner's room
// authorization of a provider (the grant discriminator, e.g. "core:codex").
type QuotaRequest struct {
	RequestID       string
	TenantID        string
	RoomID          string
	OwnerUserID     string
	RequesterUserID string
	Provider        string
	// RequestedRuns is how many more runs the borrower asked for (nil = the
	// borrower left the amount to the owner).
	RequestedRuns *int
	Note          string
	Status        QuotaRequestStatus
	// GrantedRuns is how many runs an approval added to the grant's quota.
	GrantedRuns *int
	CreatedAtMs int64
	DecidedAtMs int64
	DecidedBy   string
}

// QuotaRequestQuery filters ListQuotaRequests. Exactly one of OwnerUserID /
// RequesterUserID is expected (the caller's side); RoomID and Status narrow.
type QuotaRequestQuery struct {
	OwnerUserID     string
	RequesterUserID string
	RoomID          string
	Status          QuotaRequestStatus
	Limit           int
}

// QuotaDecision moves a pending request to approved/declined. OwnerUserID is
// the deciding caller; a decision by anyone but the request's owner is
// ErrQuotaRequestNotFound (the request is not theirs to decide).
type QuotaDecision struct {
	RequestID   string
	OwnerUserID string
	Status      QuotaRequestStatus
	GrantedRuns *int
	AtMs        int64
}

// QuotaRequestStore persists quota requests. Implementations are tenant-scoped
// by ctx and safe for concurrent use.
type QuotaRequestStore interface {
	// CreateQuotaRequest stores a new pending request. When an open request
	// already exists for the same (room, owner, provider, requester) it returns
	// that request with ErrQuotaRequestPending; a taken request id is
	// ErrQuotaRequestExists.
	CreateQuotaRequest(ctx context.Context, r QuotaRequest) (QuotaRequest, error)
	GetQuotaRequest(ctx context.Context, requestID string) (QuotaRequest, error)
	// ListQuotaRequests returns matching requests newest-first (then by
	// request id, descending; limit<=0 → 100, capped at MaxQuotaRequestList).
	ListQuotaRequests(ctx context.Context, q QuotaRequestQuery) ([]QuotaRequest, error)
	// DecideQuotaRequest atomically moves a pending request owned by
	// d.OwnerUserID to d.Status. ErrQuotaRequestDecided when it is no longer
	// pending.
	DecideQuotaRequest(ctx context.Context, d QuotaDecision) (QuotaRequest, error)
	// ReopenQuotaRequest returns an approved request to pending: the
	// compensating step when the grant raise that follows an approval fails
	// (the authorization was revoked in between), so the request is never left
	// approved with no effect. ErrQuotaRequestNotFound for an unknown
	// request, ErrQuotaRequestDecided when it is not approved (pending
	// included), ErrQuotaRequestPending when a newer request for the same
	// agent is open.
	ReopenQuotaRequest(ctx context.Context, requestID string) error
}

// quotaPendingKey is the fixed-length digest admitting at most one open request
// per (tenant, room, owner, provider, requester). Parts cannot contain NUL.
func quotaPendingKey(tnt string, r QuotaRequest) string {
	sum := sha256.Sum256([]byte(tnt + "\x00" + r.RoomID + "\x00" + r.OwnerUserID + "\x00" +
		r.Provider + "\x00" + r.RequesterUserID))
	return "qp_" + base64.RawURLEncoding.EncodeToString(sum[:])
}

// MaxQuotaRequestList bounds one ListQuotaRequests page.
const MaxQuotaRequestList = 500

func quotaListLimit(n int) int { return clampLimit(n, 100, MaxQuotaRequestList) }

// MemoryQuotaRequests is the in-memory QuotaRequestStore.
type MemoryQuotaRequests struct {
	mu      sync.Mutex
	reqs    map[string]QuotaRequest // tenant\x00id -> request
	pending map[string]string       // pending key -> tenant\x00id
}

// NewMemoryQuotaRequests constructs an empty in-memory quota request store.
func NewMemoryQuotaRequests() *MemoryQuotaRequests {
	return &MemoryQuotaRequests{reqs: map[string]QuotaRequest{}, pending: map[string]string{}}
}

var _ QuotaRequestStore = (*MemoryQuotaRequests)(nil)

// CreateQuotaRequest stores a pending request (see QuotaRequestStore).
func (s *MemoryQuotaRequests) CreateQuotaRequest(ctx context.Context, r QuotaRequest) (QuotaRequest, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return QuotaRequest{}, err
	}
	r.TenantID = tnt
	r.Status = QuotaRequestPending
	pk := quotaPendingKey(tnt, r)
	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.pending[pk]; ok {
		return s.reqs[existing], ErrQuotaRequestPending
	}
	key := grantKey(tnt, r.RequestID)
	if _, taken := s.reqs[key]; taken {
		return QuotaRequest{}, ErrQuotaRequestExists
	}
	s.reqs[key] = r
	s.pending[pk] = key
	return r, nil
}

// GetQuotaRequest returns one request in the ctx tenant.
func (s *MemoryQuotaRequests) GetQuotaRequest(ctx context.Context, requestID string) (QuotaRequest, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return QuotaRequest{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.reqs[grantKey(tnt, requestID)]
	if !ok {
		return QuotaRequest{}, ErrQuotaRequestNotFound
	}
	return r, nil
}

// ListQuotaRequests returns matching requests newest-first.
func (s *MemoryQuotaRequests) ListQuotaRequests(ctx context.Context, q QuotaRequestQuery) ([]QuotaRequest, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return nil, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []QuotaRequest{}
	for _, r := range s.reqs {
		if r.TenantID != tnt {
			continue
		}
		if q.OwnerUserID != "" && r.OwnerUserID != q.OwnerUserID {
			continue
		}
		if q.RequesterUserID != "" && r.RequesterUserID != q.RequesterUserID {
			continue
		}
		if q.RoomID != "" && r.RoomID != q.RoomID {
			continue
		}
		if q.Status != "" && r.Status != q.Status {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAtMs != out[j].CreatedAtMs {
			return out[i].CreatedAtMs > out[j].CreatedAtMs
		}
		return out[i].RequestID > out[j].RequestID
	})
	if limit := quotaListLimit(q.Limit); len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

// DecideQuotaRequest moves a pending request to its decision.
func (s *MemoryQuotaRequests) DecideQuotaRequest(ctx context.Context, d QuotaDecision) (QuotaRequest, error) {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return QuotaRequest{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := grantKey(tnt, d.RequestID)
	r, ok := s.reqs[key]
	if !ok || r.OwnerUserID != d.OwnerUserID {
		return QuotaRequest{}, ErrQuotaRequestNotFound
	}
	if r.Status != QuotaRequestPending {
		return QuotaRequest{}, ErrQuotaRequestDecided
	}
	delete(s.pending, quotaPendingKey(tnt, r))
	r.Status = d.Status
	r.GrantedRuns = d.GrantedRuns
	r.DecidedAtMs = d.AtMs
	r.DecidedBy = d.OwnerUserID
	s.reqs[key] = r
	return r, nil
}

// ReopenQuotaRequest returns an approved request to pending.
func (s *MemoryQuotaRequests) ReopenQuotaRequest(ctx context.Context, requestID string) error {
	tnt, err := tenant.Require(ctx)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	key := grantKey(tnt, requestID)
	r, ok := s.reqs[key]
	if !ok {
		return ErrQuotaRequestNotFound
	}
	if r.Status != QuotaRequestApproved {
		return ErrQuotaRequestDecided
	}
	pk := quotaPendingKey(tnt, r)
	if _, taken := s.pending[pk]; taken {
		return ErrQuotaRequestPending
	}
	r.Status = QuotaRequestPending
	r.GrantedRuns = nil
	r.DecidedAtMs = 0
	r.DecidedBy = ""
	s.reqs[key] = r
	s.pending[pk] = key
	return nil
}
