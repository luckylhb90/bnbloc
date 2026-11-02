package realtimehttp

import (
	"context"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Message marks (docs/prd/2026-10-28-channels-and-threads.md §2.3):
//
//	GET    /v1/rooms/{roomId}/marks                 → wire.MessageMarks
//	PUT    /v1/rooms/{roomId}/messages/{seq}/marks  wire.MessageMarkRequest → wire.MessageMark
//	DELETE /v1/rooms/{roomId}/messages/{seq}/marks  wire.MessageMarkRequest
//
// A reaction is any member who may post (one emoji per call, at most
// wire.MaxMessageReactions distinct on a message); a pin is the channel's,
// put or taken off by those who manage the channel (store.MayManageChannel);
// a save is the member's own and announced to nobody. Every mark needs the
// message's channel to be readable by the caller (404 otherwise), and a
// reaction or pin is announced on that channel only (message_marked).

func (s *Service) registerMarkRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/rooms/{roomId}/marks", s.handleListMarks)
	mux.HandleFunc("PUT /v1/rooms/{roomId}/messages/{seq}/marks", s.handleMark(true))
	mux.HandleFunc("DELETE /v1/rooms/{roomId}/messages/{seq}/marks", s.handleMark(false))
}

func (s *Service) marksReady(w http.ResponseWriter) bool {
	if s.Marks == nil || s.Chat == nil {
		obs.ServerError(w, "message marks unavailable", nil)
		httpx.Error(w, "message marks unavailable", http.StatusServiceUnavailable)
		return false
	}
	return true
}

func (s *Service) handleListMarks(w http.ResponseWriter, r *http.Request) {
	roomID, userID, m, ok := s.room(w, r, membership.Human)
	if !ok || !s.marksReady(w) {
		return
	}
	marks, err := s.Marks.ListMessageMarks(r.Context(), roomID, wire.MaxRoomMarks)
	if err != nil {
		obs.ServerError(w, "mark read failed", err)
		httpx.Error(w, "mark read failed", http.StatusInternalServerError)
		return
	}
	access := &channelAccess{svc: s, ctx: r.Context(), roomID: roomID, reader: readerOf(userID, m)}
	out := []wire.MessageMark{}
	for _, mk := range marks {
		if mk.Kind == wire.MarkSaved && mk.UserID != userID {
			continue
		}
		if access.mayRead(mk.ChannelID) {
			out = append(out, mk.View())
		}
	}
	httpx.WriteJSON(w, http.StatusOK, wire.MessageMarks{Marks: out})
}

// markRequest is a checked mark request: whose it is and its value.
func markRequest(body wire.MessageMarkRequest, userID string) (owner, value, problem string) {
	switch body.Kind {
	case wire.MarkReaction:
		v := strings.TrimSpace(body.Value)
		switch {
		case v == "":
			return "", "", "a reaction names its emoji"
		case len(v) > wire.MaxReactionBytes || !utf8.ValidString(v) || strings.ContainsAny(v, " \t\r\n"):
			return "", "", "a reaction is one emoji"
		}
		return userID, v, ""
	case wire.MarkPin:
		return "", "", ""
	case wire.MarkSaved:
		return userID, "", ""
	}
	return "", "", "kind is one of reaction, pin, saved"
}

func (s *Service) handleMark(put bool) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		need := membership.Human
		var body wire.MessageMarkRequest
		if !httpx.DecodeJSON(w, r, maxSmallBodyBytes, &body) {
			return
		}
		if body.Kind != wire.MarkSaved {
			need = membership.Chat
		}
		roomID, userID, m, ok := s.room(w, r, need)
		if !ok || !s.marksReady(w) {
			return
		}
		seq := parseInt(r.PathValue("seq"))
		if seq <= 0 {
			httpx.Error(w, "invalid message", http.StatusBadRequest)
			return
		}
		owner, value, problem := markRequest(body, userID)
		if problem != "" {
			httpx.Error(w, problem, http.StatusBadRequest)
			return
		}
		ctx := r.Context()
		msg, found, err := s.Chat.GetRoomMessage(ctx, roomID, seq)
		if err != nil {
			obs.ServerError(w, "message read failed", err)
			httpx.Error(w, "message read failed", http.StatusInternalServerError)
			return
		}
		if !found || !s.memberMayRead(ctx, roomID, userID, m, msg.ChannelID) {
			httpx.Error(w, "message not found", http.StatusNotFound)
			return
		}
		if body.Kind == wire.MarkPin {
			allowed, err := s.mayPin(ctx, roomID, msg.ChannelID, userID, m)
			if err != nil {
				obs.ServerError(w, "channel read failed", err)
				httpx.Error(w, "channel read failed", http.StatusInternalServerError)
				return
			}
			if !allowed {
				httpx.Error(w, "only those who manage the channel pin its messages", http.StatusForbidden)
				return
			}
		}
		now := time.Now().UnixMilli()
		mark := store.MessageMark{RoomID: roomID, ChannelID: msg.ChannelID, Seq: seq, Kind: body.Kind, UserID: owner, Value: value, By: userID, CreatedAtMs: now}
		changed := false
		if put {
			if body.Kind == wire.MarkReaction {
				n, err := s.Marks.CountMessageReactions(ctx, roomID, seq)
				if err != nil {
					obs.ServerError(w, "mark read failed", err)
					httpx.Error(w, "mark read failed", http.StatusInternalServerError)
					return
				}
				if n >= wire.MaxMessageReactions && !s.reactionExists(ctx, roomID, seq, value) {
					httpx.Error(w, "this message has as many different reactions as a message takes", http.StatusConflict)
					return
				}
			}
			changed, err = s.Marks.PutMessageMark(ctx, mark)
		} else {
			changed, err = s.Marks.DeleteMessageMark(ctx, roomID, seq, body.Kind, owner, value)
		}
		if err != nil {
			obs.ServerError(w, "mark write failed", err)
			httpx.Error(w, "mark write failed", http.StatusInternalServerError)
			return
		}
		if changed && body.Kind != wire.MarkSaved {
			_, _ = s.publish(ctx, wire.Event{
				RoomID: roomID, ChannelID: msg.ChannelID, Kind: wire.EventMessageMarked,
				Payload: wire.MessageMarkedPayload{Mark: mark.View(), Removed: !put}, AtUnixMs: now,
			}, realtime.Durable{})
		}
		if !put {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, mark.View())
	}
}

// reactionExists reports whether a message already carries value (adding
// another member's same emoji never counts against the bound).
func (s *Service) reactionExists(ctx context.Context, roomID string, seq int64, value string) bool {
	marks, err := s.Marks.ListMessageMarks(ctx, roomID, wire.MaxRoomMarks)
	if err != nil {
		return false
	}
	for _, mk := range marks {
		if mk.Seq == seq && mk.Kind == wire.MarkReaction && mk.Value == value {
			return true
		}
	}
	return false
}

// mayPin asks the one rule of who manages a channel (store.MayManageChannel)
// for the message's channel ("" the room's default) and its section.
func (s *Service) mayPin(ctx context.Context, roomID, channelID, userID string, m membership.Member) (bool, error) {
	cs := s.channelStore()
	if cs == nil {
		return m.Role.CanInvite(), nil
	}
	var ch store.RoomChannel
	var found bool
	var err error
	if channelID == "" {
		ch, found, err = cs.DefaultChannel(ctx, roomID)
	} else {
		ch, found, err = cs.GetChannel(ctx, roomID, channelID)
	}
	if err != nil || !found {
		return false, err
	}
	var sec *store.RoomSection
	if ch.SectionID != "" {
		sections, err := cs.ListSections(ctx, roomID)
		if err != nil {
			return false, err
		}
		for i := range sections {
			if sections[i].SectionID == ch.SectionID {
				sec = &sections[i]
			}
		}
	}
	return store.MayManageChannel(m.Role, userID, ch, sec), nil
}
