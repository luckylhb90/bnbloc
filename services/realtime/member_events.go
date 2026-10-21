package realtimehttp

import (
	"errors"
	"fmt"
	"net/http"
	"time"

	"github.com/luckylhb90/bnbloc/internal/audit"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/schedule"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A member's own event (docs/prd/2026-10-21-room-events.md §2.2): a room's
// member tells the room something happened, from a script, a CI job with
// their API token or the CLI (bnbloc events send):
//
//	POST /v1/rooms/{roomId}/events   wire.MemberEvent → wire.MemberEventReply
//
// A member who may chat (never a read-only visitor) sends it; the server
// sets its source (bnbloc/member/<subject>), id and time
// (schedule.MemberEventOf: never a platform type, bounded like every
// event), and dispatches it to that room's tasks only, within the source's
// and each task's rate limits (dispatchEvent). Each send is audited (its
// type and subject, never its data).

func (s *Service) registerMemberEventRoutes(mux *http.ServeMux) {
	mux.HandleFunc("POST /v1/rooms/{roomId}/events", s.handleMemberEvent)
}

func (s *Service) handleMemberEvent(w http.ResponseWriter, r *http.Request) {
	roomID, userID, _, ok := s.room(w, r, membership.Chat)
	if !ok || !s.schedulesReady(w) {
		return
	}
	var in wire.MemberEvent
	if !httpx.DecodeJSON(w, r, wire.MaxEventDataBytes+4<<10, &in) {
		return
	}
	now := time.Now()
	ev, err := schedule.MemberEventOf(userID, in, now)
	if errors.Is(err, wire.ErrEventDataTooLarge) {
		httpx.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
		return
	}
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	tid := tenant.FromContext(r.Context())
	n, dropped, err := s.dispatchEvent(r.Context(), eventDelivery{event: ev, reach: store.EventScheduleQuery{Tenants: []string{tid}, RoomID: roomID}}, now)
	if err != nil {
		obs.ServerError(w, "dispatch event failed", err)
		httpx.Error(w, "dispatch event failed", http.StatusInternalServerError)
		return
	}
	audit.Record(r.Context(), s.Audit, store.ExtAuditRecord{
		Kind: wire.AuditKindEvent, Name: roomID, Action: "send", Actor: userID, TenantID: tid, Outcome: wire.AuditOutcomeOK, AtMs: now.UnixMilli(),
		Detail: fmt.Sprintf("%s (subject %q) sent by %s: %d task(s) set, %d dropped", ev.Type, ev.Subject, userID, n, dropped),
	})
	httpx.WriteJSON(w, http.StatusAccepted, wire.MemberEventReply{Event: ev, Triggered: n, Dropped: dropped})
}
