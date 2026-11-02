package room

import (
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A member's drafts of a channel's or thread's composer
// (docs/prd/2026-10-28-channels-and-threads.md §2.3, C6): kept here so
// every device shows them, theirs only, latest write wins, one per channel.
// Never read by an agent or anyone else.
//
//	GET    /v1/rooms/{roomId}/channels/{channelId}/draft  → wire.ChannelDraft
//	PUT    /v1/rooms/{roomId}/channels/{channelId}/draft  wire.ChannelDraftRequest → wire.ChannelDraft
//	DELETE /v1/rooms/{roomId}/channels/{channelId}/draft
//
// Only a member who may post keeps one (a visitor composes nothing), and a
// channel they may not read does not exist for them (liveChannel, 404).

// draftChannel admits the caller and reads the channel the draft is of; it
// answers the refusal itself.
func (s *Service) draftChannel(w http.ResponseWriter, r *http.Request) (channelCaller, store.RoomChannel, bool) {
	roomID := r.PathValue("roomId")
	who, ok := s.humanCaller(w, r, roomID)
	if !ok {
		return channelCaller{}, store.RoomChannel{}, false
	}
	if who.role == store.RoleVisitor {
		httpx.Error(w, "a visitor keeps no drafts", http.StatusForbidden)
		return channelCaller{}, store.RoomChannel{}, false
	}
	ch, _, ok := s.liveChannel(w, r, roomID)
	return who, ch, ok
}

func draftView(d store.ChannelDraft) wire.ChannelDraft {
	return wire.ChannelDraft{Text: d.Text, UpdatedAtMs: d.UpdatedAtMs}
}

func (s *Service) handleGetChannelDraft(w http.ResponseWriter, r *http.Request) {
	who, ch, ok := s.draftChannel(w, r)
	if !ok {
		return
	}
	d, _, err := s.Channels.ChannelDraft(r.Context(), ch.RoomID, ch.ChannelID, who.user)
	if err != nil {
		obs.ServerError(w, "read channel draft failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, draftView(d))
}

func (s *Service) handlePutChannelDraft(w http.ResponseWriter, r *http.Request) {
	who, ch, ok := s.draftChannel(w, r)
	if !ok {
		return
	}
	var body wire.ChannelDraftRequest
	if !httpx.DecodeJSON(w, r, maxJSONBodyBytes, &body) {
		return
	}
	if len(body.Text) > wire.MaxChatTextBytes {
		httpx.Error(w, "a draft is at most as long as a message", http.StatusRequestEntityTooLarge)
		return
	}
	if strings.TrimSpace(body.Text) == "" {
		if err := s.Channels.DeleteChannelDraft(r.Context(), ch.RoomID, ch.ChannelID, who.user); err != nil {
			obs.ServerError(w, "delete channel draft failed", err)
			httpx.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, wire.ChannelDraft{})
		return
	}
	d := store.ChannelDraft{RoomID: ch.RoomID, ChannelID: ch.ChannelID, UserID: who.user, Text: body.Text, UpdatedAtMs: s.Now()}
	if err := s.Channels.PutChannelDraft(r.Context(), d); err != nil {
		obs.ServerError(w, "save channel draft failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, draftView(d))
}

func (s *Service) handleDeleteChannelDraft(w http.ResponseWriter, r *http.Request) {
	who, ch, ok := s.draftChannel(w, r)
	if !ok {
		return
	}
	if err := s.Channels.DeleteChannelDraft(r.Context(), ch.RoomID, ch.ChannelID, who.user); err != nil {
		obs.ServerError(w, "delete channel draft failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
