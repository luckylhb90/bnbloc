package store

import (
	"context"
	"errors"
	"sort"
)

// Content publish requests (docs/prd/2026-10-12-experts-and-teams.md §1,
// step 4): an author asks to publish one version of their component to the
// organization, and an org admin approves or declines it — the
// request/approve pattern of borrow quota requests. A component has at most
// one pending request; a newer request supersedes (withdraws) it. Requests
// are deleted with their component (DeleteContentComponent): like content
// components themselves they are user content, outside retention.

// ContentRequestStatus is a publish request's lifecycle state.
type ContentRequestStatus string

// Publish request statuses.
const (
	ContentRequestPending   ContentRequestStatus = "pending"
	ContentRequestApproved  ContentRequestStatus = "approved"
	ContentRequestDeclined  ContentRequestStatus = "declined"
	ContentRequestWithdrawn ContentRequestStatus = "withdrawn"
)

// Publish request errors.
var (
	ErrContentRequestNotFound = errors.New("store: content publish request not found")
	// ErrContentRequestDecided: the request is no longer pending.
	ErrContentRequestDecided = errors.New("store: content publish request already decided")
)

// ContentPublishRequest asks to publish Version of the author's component
// ComponentID to the organization TenantID.
type ContentPublishRequest struct {
	ID          string
	TenantID    string
	ComponentID string
	Kind        string
	Name        string
	Version     string
	RequestedBy string
	Note        string
	Status      ContentRequestStatus
	// DecidedBy / DecisionNote / DecidedAtMs record the decision (or the
	// withdrawal: DecidedBy is then the author, or empty when a newer
	// request superseded it).
	DecidedBy    string
	DecisionNote string
	DecidedAtMs  int64
	CreatedAtMs  int64
}

// ContentRequestQuery filters a listing; zero fields do not filter.
type ContentRequestQuery struct {
	TenantID    string
	ComponentID string
	RequestedBy string
	Status      ContentRequestStatus
	Limit       int
}

// ContentRequestDecision moves a pending request of TenantID to Status
// (approved, declined or withdrawn).
type ContentRequestDecision struct {
	RequestID string
	TenantID  string
	Status    ContentRequestStatus
	By        string
	Note      string
	AtMs      int64
}

// ContentPublishRequestStore persists publish requests (part of the content
// store).
type ContentPublishRequestStore interface {
	// CreateContentRequest stores r as pending. A pending request of the
	// same component is withdrawn in the same step (superseded).
	CreateContentRequest(ctx context.Context, r ContentPublishRequest) (ContentPublishRequest, error)
	GetContentRequest(ctx context.Context, id string) (ContentPublishRequest, error)
	// ListContentRequests returns matching requests, newest first (limit
	// <= 0 → 100, capped at MaxContentList).
	ListContentRequests(ctx context.Context, q ContentRequestQuery) ([]ContentPublishRequest, error)
	// DecideContentRequest atomically moves a pending request of
	// d.TenantID to d.Status: ErrContentRequestNotFound when there is no
	// such request in that tenant, ErrContentRequestDecided when it is no
	// longer pending.
	DecideContentRequest(ctx context.Context, d ContentRequestDecision) (ContentPublishRequest, error)
	// ReopenContentRequest returns an approved request to pending: the
	// compensating step when publishing the approved version fails, so a
	// request is never left approved with no effect. Only an approved
	// request reopens: ErrContentRequestNotFound for an unknown one,
	// ErrContentRequestDecided for one that is not approved (pending,
	// declined or withdrawn) or when a newer request is pending.
	ReopenContentRequest(ctx context.Context, id string) error
}

func contentRequestLimit(n int) int { return clampLimit(n, 100, MaxContentList) }

func (q ContentRequestQuery) admits(r ContentPublishRequest) bool {
	return (q.TenantID == "" || r.TenantID == q.TenantID) &&
		(q.ComponentID == "" || r.ComponentID == q.ComponentID) &&
		(q.RequestedBy == "" || r.RequestedBy == q.RequestedBy) &&
		(q.Status == "" || r.Status == q.Status)
}

func (s *MemoryContentComponents) CreateContentRequest(_ context.Context, r ContentPublishRequest) (ContentPublishRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.requests[r.ID]; ok {
		return ContentPublishRequest{}, ErrContentVersionConflict
	}
	for id, prev := range s.requests {
		if prev.ComponentID == r.ComponentID && prev.Status == ContentRequestPending {
			prev.Status, prev.DecidedAtMs = ContentRequestWithdrawn, r.CreatedAtMs
			s.requests[id] = prev
		}
	}
	r.Status = ContentRequestPending
	s.requests[r.ID] = r
	return r, nil
}

func (s *MemoryContentComponents) GetContentRequest(_ context.Context, id string) (ContentPublishRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.requests[id]
	if !ok {
		return ContentPublishRequest{}, ErrContentRequestNotFound
	}
	return r, nil
}

func (s *MemoryContentComponents) ListContentRequests(_ context.Context, q ContentRequestQuery) ([]ContentPublishRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []ContentPublishRequest{}
	for _, r := range s.requests {
		if q.admits(r) {
			out = append(out, r)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAtMs != out[j].CreatedAtMs {
			return out[i].CreatedAtMs > out[j].CreatedAtMs
		}
		return out[i].ID > out[j].ID
	})
	if n := contentRequestLimit(q.Limit); len(out) > n {
		out = out[:n]
	}
	return out, nil
}

func (s *MemoryContentComponents) DecideContentRequest(_ context.Context, d ContentRequestDecision) (ContentPublishRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.requests[d.RequestID]
	if !ok || r.TenantID != d.TenantID {
		return ContentPublishRequest{}, ErrContentRequestNotFound
	}
	if r.Status != ContentRequestPending {
		return r, ErrContentRequestDecided
	}
	r.Status, r.DecidedBy, r.DecisionNote, r.DecidedAtMs = d.Status, d.By, d.Note, d.AtMs
	s.requests[r.ID] = r
	return r, nil
}

func (s *MemoryContentComponents) ReopenContentRequest(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.requests[id]
	if !ok {
		return ErrContentRequestNotFound
	}
	if r.Status != ContentRequestApproved {
		return ErrContentRequestDecided
	}
	for oid, other := range s.requests {
		if oid != id && other.ComponentID == r.ComponentID && other.Status == ContentRequestPending {
			return ErrContentRequestDecided
		}
	}
	r.Status, r.DecidedBy, r.DecisionNote, r.DecidedAtMs = ContentRequestPending, "", "", 0
	s.requests[id] = r
	return nil
}
