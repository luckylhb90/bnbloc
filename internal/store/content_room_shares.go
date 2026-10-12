package store

import (
	"context"
	"sort"
)

// Room shares of content components (docs/prd/2026-10-12-experts-and-
// teams.md, "Gap closure" 2): sharing a component with a room gives its
// human members a copy and records the share, so a member who joins the
// room later gets a copy too, while the share stands. The author revokes
// it (later joiners get nothing; existing copies stay). One row per (room,
// component), moved forward by a later share; deleted with the component.

// ContentRoomShare is a standing share of an author's component with a
// room, at Version.
type ContentRoomShare struct {
	TenantID    string
	RoomID      string
	ComponentID string
	Version     string
	SharedBy    string
	Note        string
	CreatedAtMs int64
	UpdatedAtMs int64
}

// ContentRoomShareQuery filters room shares; zero fields do not filter (at
// least one is set by every caller).
type ContentRoomShareQuery struct {
	RoomID      string
	ComponentID string
}

// ContentRoomShareStore persists room shares (part of the content store).
type ContentRoomShareStore interface {
	// PutContentRoomShare upserts the share of (room, component): a new
	// share, or the existing one moved to s's version, sharer, note and
	// update time (its tenant and CreatedAtMs kept). A component that does
	// not exist is ErrContentNotFound.
	PutContentRoomShare(ctx context.Context, s ContentRoomShare) error
	// ListContentRoomShares returns matching shares, oldest first (at most
	// MaxContentList).
	ListContentRoomShares(ctx context.Context, q ContentRoomShareQuery) ([]ContentRoomShare, error)
	// DeleteContentRoomShare revokes a share; false when there was none.
	DeleteContentRoomShare(ctx context.Context, roomID, componentID string) (bool, error)
}

func roomShareKey(roomID, componentID string) string { return roomID + "\x00" + componentID }

func (s *MemoryContentComponents) PutContentRoomShare(_ context.Context, sh ContentRoomShare) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.components[sh.ComponentID]; !ok {
		return ErrContentNotFound
	}
	k := roomShareKey(sh.RoomID, sh.ComponentID)
	if prev, ok := s.roomShares[k]; ok {
		// A re-share changes what the SQL upsert changes.
		prev.Version, prev.SharedBy, prev.Note, prev.UpdatedAtMs = sh.Version, sh.SharedBy, sh.Note, sh.UpdatedAtMs
		sh = prev
	}
	s.roomShares[k] = sh
	return nil
}

func (s *MemoryContentComponents) ListContentRoomShares(_ context.Context, q ContentRoomShareQuery) ([]ContentRoomShare, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []ContentRoomShare{}
	for _, sh := range s.roomShares {
		if (q.RoomID == "" || sh.RoomID == q.RoomID) && (q.ComponentID == "" || sh.ComponentID == q.ComponentID) {
			out = append(out, sh)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].CreatedAtMs != out[j].CreatedAtMs {
			return out[i].CreatedAtMs < out[j].CreatedAtMs
		}
		return roomShareKey(out[i].RoomID, out[i].ComponentID) < roomShareKey(out[j].RoomID, out[j].ComponentID)
	})
	if len(out) > MaxContentList {
		out = out[:MaxContentList]
	}
	return out, nil
}

func (s *MemoryContentComponents) DeleteContentRoomShare(_ context.Context, roomID, componentID string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	k := roomShareKey(roomID, componentID)
	_, ok := s.roomShares[k]
	delete(s.roomShares, k)
	return ok, nil
}
