package room

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
)

// newChannelID mints a channel id: "ch_" + 8 random bytes hex.
func newChannelID() string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return "ch_" + hex.EncodeToString(b)
}

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
		writeErr(w, http.StatusForbidden, "caller is not a member of room")
		return
	}
	channels := s.Channels.ListChannels(r.Context(), roomID)
	if len(channels) == 0 {
		// Older rooms may predate channels (or a previous best-effort channel
		// insert may have failed). Heal by creating the locked default channel on
		// first list so every room always has a selectable message stream.
		def := store.RoomChannel{
			ChannelID:   newChannelID(),
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
	writeJSON(w, http.StatusOK, map[string]any{"channels": out})
}

// handleGetPeopleChat returns the room's people chat without creating it:
// 404 until a member first opens it. The room view reads it on entry (header
// badge, routing a deep link into the panel) without minting a channel for
// every room a member merely visits.
func (s *Service) handleGetPeopleChat(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	if !s.isHumanMember(r, roomID, caller(r)) {
		writeErr(w, http.StatusForbidden, "caller is not a member of room")
		return
	}
	for _, c := range s.Channels.ListChannels(r.Context(), roomID) {
		if c.IsPeople() {
			writeJSON(w, http.StatusOK, toChannelView(c))
			return
		}
	}
	writeErr(w, http.StatusNotFound, "the room has no people chat yet")
}

// handlePeopleChat returns the room's people chat — a human-only side
// conversation agents never see — creating it on first open. Any member may
// open it; repeated and concurrent calls return the same channel.
func (s *Service) handlePeopleChat(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	if !s.isHumanMember(r, roomID, caller(r)) {
		writeErr(w, http.StatusForbidden, "caller is not a member of room")
		return
	}
	ch, err := s.Channels.EnsurePeopleChannel(r.Context(), roomID, caller(r), s.Now())
	if err != nil {
		obs.ServerError(w, "open people chat failed", err)
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toChannelView(ch))
}

// handleCreateChannel adds a channel. Any member may create one; the creator
// (plus owner/admin) may later rename it, but only owner/admin may delete it.
func (s *Service) handleCreateChannel(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	if !s.isHumanMember(r, roomID, caller(r)) {
		writeErr(w, http.StatusForbidden, "caller is not a member of room")
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &body) {
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	ch, err := s.createChannel(r.Context(), roomID, name, caller(r))
	if err != nil {
		obs.ServerError(w, "create channel failed", err)
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, toChannelView(ch))
}

// createChannel adds a non-default channel named name, created by creator
// (the creator may later rename it). Every channel-adding path (the route, a
// handoff import, a template instantiation) goes through it.
func (s *Service) createChannel(ctx context.Context, roomID, name, creator string) (store.RoomChannel, error) {
	ch := store.RoomChannel{
		ChannelID:   newChannelID(),
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
		writeErr(w, http.StatusForbidden, "caller is not a member of room")
		return
	}
	ch, ok := s.Channels.GetChannel(r.Context(), roomID, r.PathValue("channelId"))
	if !ok {
		writeErr(w, http.StatusNotFound, "channel not found")
		return
	}
	if ch.IsDefault || ch.IsPeople() {
		writeErr(w, http.StatusConflict, "the default channel and the people chat cannot be renamed")
		return
	}
	if !role.CanInvite() && ch.CreatedBy != user {
		writeErr(w, http.StatusForbidden, "only the owner, an admin, or the channel creator may rename")
		return
	}
	var body struct {
		Name string `json:"name"`
	}
	if !decode(w, r, &body) {
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		writeErr(w, http.StatusBadRequest, "name is required")
		return
	}
	// Conditional rename: if a delete lands between the permission check and
	// this write, RenameChannel fails instead of resurrecting the row.
	if err := s.Channels.RenameChannel(r.Context(), roomID, ch.ChannelID, name); err != nil {
		if errors.Is(err, store.ErrChannelNotFound) {
			writeErr(w, http.StatusNotFound, "channel not found")
			return
		}
		obs.ServerError(w, "rename channel failed", err)
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	ch.Name = name
	writeJSON(w, http.StatusOK, toChannelView(ch))
}

// handleDeleteChannel soft-deletes a channel (owner/admin only); the default
// channel cannot be deleted — every room must keep a message stream.
func (s *Service) handleDeleteChannel(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	if role, ok := s.Store.MemberRole(r.Context(), roomID, caller(r)); !ok || !role.CanInvite() {
		writeErr(w, http.StatusForbidden, "only the owner or an admin may delete channels")
		return
	}
	channelID := r.PathValue("channelId")
	ch, ok := s.Channels.GetChannel(r.Context(), roomID, channelID)
	if !ok {
		writeErr(w, http.StatusNotFound, "channel not found")
		return
	}
	if ch.IsDefault || ch.IsPeople() {
		writeErr(w, http.StatusConflict, "the default channel and the people chat cannot be deleted")
		return
	}
	if err := s.Channels.DeleteChannel(r.Context(), roomID, channelID, s.Now()); err != nil {
		obs.ServerError(w, "delete channel failed", err)
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.WriteHeader(http.StatusOK)
}
