package realtimehttp

import (
	"context"
	"errors"
	"net/http"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/roomctx"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Forwarding (docs/prd/2026-10-28-channels-and-threads.md §2.3): a new
// message that names its source by room and sequence and carries none of
// its text. Whoever reads the forward opens the source through
// GET /v1/rooms/{roomId}/messages/{seq}, which answers only a reader of the
// source's channel, so a forward never shows a message to anyone who could
// not read it where it is.

// errBadForward: forwardOf names no live message the author may read now.
var errBadForward = errors.New("forwardOf names no message you can read")

// forwardSource checks the author may read the forwarded message now (a
// person in its room, a reader of its channel) and returns its reference
// with its channel filled in.
func (s *Service) forwardSource(ctx context.Context, author string, ref wire.ForwardRef) (wire.ForwardRef, error) {
	roomID, err := roomctx.Normalize(ref.RoomID)
	if err != nil || ref.Seq <= 0 || s.Chat == nil {
		return wire.ForwardRef{}, errBadForward
	}
	m, err := membership.RoomScope(ctx, s.Membership, roomID, author, membership.Human)
	if err != nil {
		return wire.ForwardRef{}, errBadForward
	}
	msg, found, err := s.Chat.GetRoomMessage(ctx, roomID, ref.Seq)
	if err != nil {
		return wire.ForwardRef{}, err
	}
	if !found || !wire.PersistedFeedKind(msg.Kind) || !s.memberMayRead(ctx, roomID, author, m, msg.ChannelID) {
		return wire.ForwardRef{}, errBadForward
	}
	return wire.ForwardRef{RoomID: roomID, ChannelID: msg.ChannelID, Seq: ref.Seq}, nil
}

// handleGetMessage answers one live message of the room to a reader of its
// channel (a forward's source, a link to it); 404 for anyone else, as for a
// message that does not exist.
func (s *Service) handleGetMessage(w http.ResponseWriter, r *http.Request) {
	roomID, userID, m, ok := s.room(w, r, membership.AnyMember)
	if !ok {
		return
	}
	seq := parseInt(r.PathValue("seq"))
	if seq <= 0 || s.Chat == nil {
		httpx.Error(w, "invalid message", http.StatusBadRequest)
		return
	}
	msg, found, err := s.Chat.GetRoomMessage(r.Context(), roomID, seq)
	if err != nil {
		obs.ServerError(w, "message read failed", err)
		httpx.Error(w, "message read failed", http.StatusInternalServerError)
		return
	}
	if !found || !wire.PersistedFeedKind(msg.Kind) || !s.memberMayRead(r.Context(), roomID, userID, m, msg.ChannelID) {
		httpx.Error(w, "message not found", http.StatusNotFound)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, wire.FeedMessage{
		Sequence: msg.Seq, ChannelID: msg.ChannelID, Kind: msg.Kind, Payload: msg.Payload, AtUnixMs: msg.CreatedAtMs, EditedAtMs: msg.EditedAtMs,
	})
}
