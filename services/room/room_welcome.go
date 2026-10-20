package room

import (
	"errors"
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A room's welcome and quick prompts
// (docs/prd/2026-10-20-room-setup-by-conversation.md §2.1): what the empty
// room shows every member, a quick prompt's click putting its text into the
// message box. They are the room's own, set when it is created (a room
// package's install) and changed by its owner or an admin:
//
//	PATCH /v1/rooms/{roomId}/welcome   wire.RoomWelcomeRequest → wire.RoomDetail
//
// Their bounds are a room package's: contentpkg.CheckRoomStart decides
// both, so a room saved as a package always validates and a package
// installed always fits.

// cleanStart trims a welcome and quick prompts as they are kept and checks
// them (contentpkg.CheckRoomStart).
func cleanStart(welcome string, prompts []wire.RoomQuickPrompt) (string, []wire.RoomQuickPrompt, error) {
	welcome = strings.TrimSpace(welcome)
	out := make([]wire.RoomQuickPrompt, 0, len(prompts))
	check := make([]contentpkg.QuickPrompt, 0, len(prompts))
	for _, q := range prompts {
		q = wire.RoomQuickPrompt{Title: strings.TrimSpace(q.Title), Prompt: strings.TrimSpace(q.Prompt)}
		out = append(out, q)
		check = append(check, contentpkg.QuickPrompt{Title: q.Title, Prompt: q.Prompt})
	}
	return welcome, out, contentpkg.CheckRoomStart(welcome, check)
}

// handlePatchWelcome changes a room's welcome or quick prompts: its owner's
// or an admin's (like its experts and skills).
func (s *Service) handlePatchWelcome(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	role, ok, err := s.Store.MemberRole(r.Context(), roomID, caller(r))
	if err != nil {
		obs.ServerError(w, "read membership failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !ok || !role.CanInvite() {
		httpx.Error(w, "only the room's owner or admins change its welcome and quick prompts", http.StatusForbidden)
		return
	}
	var body wire.RoomWelcomeRequest
	if !httpx.DecodeJSON(w, r, maxStartBodyBytes, &body) {
		return
	}
	rm, err := s.Store.GetRoom(r.Context(), roomID)
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	welcome, prompts := rm.Welcome, rm.QuickPrompts
	if body.Welcome != nil {
		welcome = *body.Welcome
	}
	if body.QuickPrompts != nil {
		prompts = *body.QuickPrompts
	}
	welcome, prompts, err = cleanStart(welcome, prompts)
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.Store.UpdateRoom(r.Context(), roomID, store.RoomPatch{Welcome: &welcome, QuickPrompts: &prompts}); err != nil {
		if errors.Is(err, store.ErrRoomNotFound) {
			httpx.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		obs.ServerError(w, "set room welcome failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	rm.Welcome, rm.QuickPrompts = welcome, prompts
	detail, err := s.roomDetail(r.Context(), rm)
	if err != nil {
		obs.ServerError(w, "count members failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, detail)
}

// maxStartBodyBytes bounds a create or welcome body: twelve quick prompts
// of 4000 characters and a welcome fit, with room for JSON escapes.
const maxStartBodyBytes = 512 << 10
