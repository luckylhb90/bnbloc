package room

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// channelView is the wire shape of a channel in list/create/rename responses.
// Kind is "people" for the room's people chat, omitted for an ordinary channel.
type channelView struct {
	ChannelID string `json:"channelId"`
	Name      string `json:"name"`
	IsDefault bool   `json:"isDefault"`
	Kind      string `json:"kind,omitempty"`
	CreatedBy string `json:"createdBy"`
}

func toChannelView(c store.RoomChannel) channelView {
	return channelView{ChannelID: c.ChannelID, Name: c.Name, IsDefault: c.IsDefault, Kind: c.Kind, CreatedBy: c.CreatedBy}
}

// handleListChannels returns a room's live ordinary channels (default first).
// The caller must be a member; this backs the channel sidebar AND the
// bridge's room-context materialization (it pages history for exactly these
// channels), so the people chat is never listed: agents must not see it, and
// the web reaches it through POST .../people-chat instead.
func (s *Service) handleListChannels(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	if !s.Store.IsMember(r.Context(), roomID, caller(r)) {
		httpx.Error(w, "caller is not a member of room", http.StatusForbidden)
		return
	}
	channels := s.Channels.ListChannels(r.Context(), roomID)
	if len(channels) == 0 {
		// Older rooms may predate channels (or a previous best-effort channel
		// insert may have failed). Heal by creating the locked default channel on
		// first list so every room always has a selectable message stream.
		def := store.RoomChannel{
			ChannelID:   ids.New("ch_"),
			TenantID:    tenant.FromContext(r.Context()),
			RoomID:      roomID,
			Name:        "general",
			IsDefault:   true,
			CreatedBy:   caller(r),
			CreatedAtMs: s.Now(),
		}
		if err := s.Channels.PutChannel(r.Context(), def); err == nil {
			channels = []store.RoomChannel{def}
		}
	}
	out := make([]channelView, 0, len(channels))
	for _, c := range channels {
		if c.IsPeople() {
			continue
		}
		out = append(out, toChannelView(c))
	}
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"channels": out})
}

// handleGetPeopleChat returns the room's people chat without creating it:
// 404 until a member first opens it. The room view reads it on entry (header
// badge, routing a deep link into the panel) without minting a channel for
// every room a member merely visits.
func (s *Service) handleGetPeopleChat(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	if !s.isHumanMember(r, roomID, caller(r)) {
		httpx.Error(w, "caller is not a member of room", http.StatusForbidden)
		return
	}
	for _, c := range s.Channels.ListChannels(r.Context(), roomID) {
		if c.IsPeople() {
			httpx.WriteJSON(w, http.StatusOK, toChannelView(c))
			return
		}
	}
	httpx.Error(w, "the room has no people chat yet", http.StatusNotFound)
}

// handlePeopleChat returns the room's people chat — a human-only side
// conversation agents never see — creating it on first open. Any member may
// open it; repeated and concurrent calls return the same channel.
func (s *Service) handlePeopleChat(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	if !s.isHumanMember(r, roomID, caller(r)) {
		httpx.Error(w, "caller is not a member of room", http.StatusForbidden)
		return
	}
	ch, err := s.Channels.EnsurePeopleChannel(r.Context(), roomID, caller(r), s.Now())
	if err != nil {
		obs.ServerError(w, "open people chat failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toChannelView(ch))
}

// handleCreateChannel adds a channel. Any member may create one; the creator
// (plus owner/admin) may later rename it, but only owner/admin may delete it.
func (s *Service) handleCreateChannel(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	if !s.isHumanMember(r, roomID, caller(r)) {
		httpx.Error(w, "caller is not a member of room", http.StatusForbidden)
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if !httpx.DecodeJSON(w, r, maxJSONBodyBytes, &body) {
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		httpx.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	ch, err := s.createChannel(r.Context(), roomID, name, caller(r))
	if err != nil {
		obs.ServerError(w, "create channel failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, toChannelView(ch))
}

// createChannel adds a non-default channel named name, created by creator
// (the creator may later rename it). Every channel-adding path (the route, a
// handoff import, a template instantiation) goes through it.
func (s *Service) createChannel(ctx context.Context, roomID, name, creator string) (store.RoomChannel, error) {
	ch := store.RoomChannel{
		ChannelID:   ids.New("ch_"),
		TenantID:    tenant.FromContext(ctx),
		RoomID:      roomID,
		Name:        strings.TrimSpace(name),
		CreatedBy:   creator,
		CreatedAtMs: s.Now(),
	}
	if err := s.Channels.PutChannel(ctx, ch); err != nil {
		return store.RoomChannel{}, err
	}
	return ch, nil
}

// handleRenameChannel renames a channel. Owner/admin or the channel's creator
// may rename; the default channel is immutable.
func (s *Service) handleRenameChannel(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	user := caller(r)
	role, ok := s.Store.MemberRole(r.Context(), roomID, user)
	if !ok {
		httpx.Error(w, "caller is not a member of room", http.StatusForbidden)
		return
	}
	ch, ok := s.Channels.GetChannel(r.Context(), roomID, r.PathValue("channelId"))
	if !ok {
		httpx.Error(w, "channel not found", http.StatusNotFound)
		return
	}
	if ch.IsDefault || ch.IsPeople() {
		httpx.Error(w, "the default channel and the people chat cannot be renamed", http.StatusConflict)
		return
	}
	if !role.CanInvite() && ch.CreatedBy != user {
		httpx.Error(w, "only the owner, an admin, or the channel creator may rename", http.StatusForbidden)
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if !httpx.DecodeJSON(w, r, maxJSONBodyBytes, &body) {
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		httpx.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	// Conditional rename: if a delete lands between the permission check and
	// this write, RenameChannel fails instead of resurrecting the row.
	if err := s.Channels.RenameChannel(r.Context(), roomID, ch.ChannelID, name); err != nil {
		if errors.Is(err, store.ErrChannelNotFound) {
			httpx.Error(w, "channel not found", http.StatusNotFound)
			return
		}
		obs.ServerError(w, "rename channel failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	ch.Name = name
	httpx.WriteJSON(w, http.StatusOK, toChannelView(ch))
}

// handleDeleteChannel soft-deletes a channel (owner/admin only); the default
// channel cannot be deleted — every room must keep a message stream.
func (s *Service) handleDeleteChannel(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	if role, ok := s.Store.MemberRole(r.Context(), roomID, caller(r)); !ok || !role.CanInvite() {
		httpx.Error(w, "only the owner or an admin may delete channels", http.StatusForbidden)
		return
	}
	channelID := r.PathValue("channelId")
	ch, ok := s.Channels.GetChannel(r.Context(), roomID, channelID)
	if !ok {
		httpx.Error(w, "channel not found", http.StatusNotFound)
		return
	}
	if ch.IsDefault || ch.IsPeople() {
		httpx.Error(w, "the default channel and the people chat cannot be deleted", http.StatusConflict)
		return
	}
	if err := s.Channels.DeleteChannel(r.Context(), roomID, channelID, s.Now()); err != nil {
		obs.ServerError(w, "delete channel failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}
