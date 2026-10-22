package membership

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"

	"github.com/luckylhb90/bnbloc/internal/svcauth"
)

// RoomRef names one room a member belongs to.
type RoomRef struct {
	RoomID string `json:"roomId"`
	Name   string `json:"name"`
}

// MemberRooms lists the rooms userID belongs to in tenantID, asking
// bnbloc-room's own detailed room listing (GET /v1/users/{id}/rooms?detail=1)
// as that user. It is the server-side membership source for cross-room reads
// (global search): a caller-supplied room list is only ever a filter on it.
// Any error fails closed (no rooms) and is returned.
func (c *RoomClient) MemberRooms(ctx context.Context, userID, tenantID string) ([]RoomRef, error) {
	endpoint := c.BaseURL + "/v1/users/" + url.PathEscape(userID) + "/rooms?detail=1"
	req, err := svcauth.NewRequest(ctx, http.MethodGet, endpoint, nil, userID, tenantID)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("membership: room listing status %d", resp.StatusCode)
	}
	var out struct {
		Rooms []RoomRef `json:"rooms"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Rooms, nil
}

// MemberRoomIDs lists the ids of the rooms userID belongs to in tenantID: the
// room service's bare id listing (GET /v1/users/{id}/rooms), which skips the
// per-room detail MemberRooms reads. For a reader that needs only which rooms
// (an unread summary), so it costs one query of ids. Any error fails closed
// (no rooms) and is returned.
func (c *RoomClient) MemberRoomIDs(ctx context.Context, userID, tenantID string) ([]string, error) {
	endpoint := c.BaseURL + "/v1/users/" + url.PathEscape(userID) + "/rooms"
	req, err := svcauth.NewRequest(ctx, http.MethodGet, endpoint, nil, userID, tenantID)
	if err != nil {
		return nil, err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("membership: room id listing status %d", resp.StatusCode)
	}
	var out struct {
		Rooms []string `json:"rooms"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	return out.Rooms, nil
}

// MemberRoomIDs lists the static set's rooms where userID is a human member.
func (s *Static) MemberRoomIDs(ctx context.Context, userID, tenantID string) ([]string, error) {
	rooms, err := s.MemberRooms(ctx, userID, tenantID)
	ids := make([]string, 0, len(rooms))
	for _, r := range rooms {
		ids = append(ids, r.RoomID)
	}
	return ids, err
}

// MemberRooms lists the static set's rooms where userID is a human member
// (the room id doubles as its name). The static set is single-tenant and ignores tenantID.
func (s *Static) MemberRooms(_ context.Context, userID, _ string) ([]RoomRef, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var out []RoomRef
	for roomID, set := range s.members {
		if m, ok := set[userID]; ok && !m.AgentHost {
			out = append(out, RoomRef{RoomID: roomID, Name: roomID})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].RoomID < out[j].RoomID })
	return out, nil
}
