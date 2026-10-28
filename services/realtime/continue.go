package realtimehttp

import (
	"context"
	"errors"
	"net/http"
	"regexp"
	"strings"

	"github.com/luckylhb90/bnbloc/component"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A card's decision that lets an agent's work go on
// (docs/prd/2026-10-28-held-change-continues.md): POST
// /internal/v1/rooms/{roomId}/continue, signed by the gateway as the member
// who decided the card, posts the service's words in the room as that member,
// to the agent that ran the job the card belongs to, with an automatic mark.
//
// The message carries no authority of its own: it is a typed message in every
// respect (PostChat checks the member's membership, the channel's setup,
// borrow rules at drive time). What is checked here is only that the job is
// the member's own and over, so a card cannot be used to talk to an agent of
// a job that was not theirs, nor to speak into a turn still running.

// continueKeyShape bounds a service's idempotency key.
var continueKeyShape = regexp.MustCompile(`^[A-Za-z0-9_.:-]{1,96}$`)

// handleInternalContinue serves POST /internal/v1/rooms/{roomId}/continue.
func (s *Service) handleInternalContinue(w http.ResponseWriter, r *http.Request) {
	roomID, caller, ok := s.roomCaller(w, r)
	if !ok {
		return
	}
	if s.Dispatches == nil || s.BridgeJobs == nil {
		httpx.Unavailable(w, "continue")
		return
	}
	var in wire.ContinueRequest
	if !httpx.DecodeJSON(w, r, maxChatBodyBytes, &in) {
		return
	}
	reply, err := s.Continue(r.Context(), roomID, caller, in)
	switch {
	case err == nil:
		httpx.WriteJSON(w, http.StatusOK, reply)
	case membership.IsRefusal(err):
		httpx.Error(w, err.Error(), http.StatusForbidden)
	default:
		if refusal, refused := isPostRefusal(err); refused {
			// The channel's setup refuses the member's post: nothing is posted, and the
			// decision the card made stands.
			httpx.WriteJSON(w, http.StatusOK, wire.ContinueReply{Why: refusal.msg})
			return
		}
		if errors.Is(err, errAgentsNotAdmitted) {
			httpx.WriteJSON(w, http.StatusOK, wire.ContinueReply{Why: errAgentsNotAdmitted.Error()})
			return
		}
		obs.ServerError(w, "continue failed", err)
		httpx.Error(w, "continue failed", http.StatusInternalServerError)
	}
}

// Continue posts in as caller (the member), to the agent of in.Job. A job that
// is not the member's, not of this room, or not over posts nothing and says
// why in the reply (a 200: the card's own decision stands).
func (s *Service) Continue(ctx context.Context, roomID, caller string, in wire.ContinueRequest) (wire.ContinueReply, error) {
	in.Job, in.Component, in.Key = strings.TrimSpace(in.Job), strings.TrimSpace(in.Component), strings.TrimSpace(in.Key)
	text := strings.TrimSpace(in.Text)
	switch {
	case in.Job == "" || text == "" || len(text) > component.MaxCardContinue:
		return wire.ContinueReply{Why: "nothing to send"}, nil
	case !continueKeyShape.MatchString(in.Key) || !component.ValidName(in.Component):
		return wire.ContinueReply{Why: "invalid continue"}, nil
	}
	job, err := s.Dispatches.GetAgentRun(ctx, in.Job)
	switch {
	case errors.Is(err, store.ErrAgentJobNotFound):
		return wire.ContinueReply{Why: "that request is gone"}, nil
	case err != nil:
		return wire.ContinueReply{}, err
	case job.RoomID != roomID || job.RequesterUserID != caller:
		return wire.ContinueReply{Why: "that request is not yours in this room"}, nil
	case !job.Status.Terminal():
		return wire.ContinueReply{Why: "that request is still running"}, nil
	}
	posted, err := s.PostChat(ctx, ChatPost{
		RoomID:      roomID,
		Author:      caller,
		Text:        text,
		ClientMsgID: "continue-" + in.Job + "-" + in.Key,
		ChannelID:   job.ChannelID,
		Recipient:   &ChatRecipient{OwnerID: job.OwnerUserID, Provider: job.Provider, Core: job.Core},
		Continued:   &wire.ContinueMarker{JobID: job.JobID, Component: in.Component},
	})
	if err != nil {
		return wire.ContinueReply{}, err
	}
	return wire.ContinueReply{Posted: true, Sequence: posted.Sequence}, nil
}
