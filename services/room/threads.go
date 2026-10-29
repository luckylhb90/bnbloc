package room

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A room's threads as the room service shows them
// (docs/prd/2026-10-28-channels-and-threads.md §2.2): realtime opens a
// thread (it holds the root message); here they are listed, renamed,
// archived and followed (the channel routes: PATCH, archive, restore,
// follow).
//
//	GET /v1/rooms/{roomId}/channel-threads   ?parent=<channel> one channel's; ?followed=1 the caller's
//	                                 → wire.ThreadList, the threads the caller may read
//
// A thread's readers are its parent's (store.ThreadOf): the listing asks the
// one rule (MayReadChannel) of each.

func (s *Service) handleListThreads(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	member, ok := s.requireReadAs(w, r, roomID)
	if !ok {
		return
	}
	parent := strings.TrimSpace(r.URL.Query().Get("parent"))
	followed := r.URL.Query().Get("followed") == "1"
	threads, err := s.Channels.ListThreads(r.Context(), roomID, parent)
	user := ""
	if member {
		user = caller(r)
	}
	var follows map[string]store.ChannelFollow
	if err == nil {
		follows, err = s.followsOf(r.Context(), roomID, user)
	}
	if err != nil {
		obs.ServerError(w, "list threads failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	access, ok := s.readerAccess(w, r, roomID)
	if !ok {
		return
	}
	out := wire.ThreadList{Threads: []wire.RoomChannel{}}
	for _, th := range threads {
		if !access.MayReadChannel(th) {
			continue
		}
		f := follows[th.ChannelID]
		if followed && f.ChannelID == "" {
			continue
		}
		out.Threads = append(out.Threads, channelView(th, f))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// patchThread changes a thread's title, the one thing of a thread anyone who
// may post edits (§2.2); its section and settings are its parent's.
func (s *Service) patchThread(w http.ResponseWriter, r *http.Request, roomID string, who channelCaller, th store.RoomChannel) {
	if who.role == store.RoleVisitor {
		httpx.Error(w, "visitors do not edit threads", http.StatusForbidden)
		return
	}
	var body wire.ChannelPatchRequest
	if !httpx.DecodeJSON(w, r, maxJSONBodyBytes, &body) {
		return
	}
	if body.Name == nil || body.Purpose != nil || body.Rules != nil || body.SectionID != nil || body.Position != nil || body.OwnerID != nil || body.Settings != nil {
		httpx.Error(w, "a thread's title is all that changes: its section and settings are its channel's", http.StatusBadRequest)
		return
	}
	title, err := cleanThreadTitle(*body.Name)
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if err := s.Channels.UpdateChannel(r.Context(), roomID, th.ChannelID, store.ChannelPatch{Name: &title}); err != nil {
		if errors.Is(err, store.ErrChannelNotFound) {
			httpx.Error(w, "channel not found", http.StatusNotFound)
			return
		}
		obs.ServerError(w, "rename thread failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.publishChannelUpdated(r.Context(), roomID, th.ChannelID, th.SectionID)
	s.viewFor(w, r, roomID, th.ChannelID, who.user)
}

// cleanThreadTitle is a title one line, 1–MaxThreadTitle runes.
func cleanThreadTitle(raw string) (string, error) {
	t := strings.Join(strings.Fields(raw), " ")
	switch {
	case t == "":
		return "", errBadRequest("a thread's title is required")
	case utf8.RuneCountInString(t) > wire.MaxThreadTitle:
		return "", errBadRequest("a thread's title is too long")
	}
	return t, nil
}

// publishThreadUpdated tells the thread's parent stream about it (its root's
// summary line), scoped to the parent: its readers are the thread's.
func (s *Service) publishThreadUpdated(ctx context.Context, th store.RoomChannel) {
	s.Publisher.Publish(ctx, wire.Event{
		RoomID: th.RoomID, ChannelID: th.ParentID, Kind: wire.EventThreadUpdated,
		Payload: th.ThreadUpdated(), AtUnixMs: s.Now(),
	})
}
