package gateway

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/notify"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A room report (docs/prd/2026-10-20-never-a-dead-end.md §2.2, the
// "report" fix): a member tells their organization's admins, in words they
// reviewed, what a room could not do, linked to the reply it is about. It
// goes through the one notification emitter as wire.NotificationRoomReport
// (an admin may turn it off in their notification settings).
//
//	POST /v1/rooms/{roomId}/report   a member's note to the admins

// handleRoomReport answers POST /v1/rooms/{roomId}/report: a member who
// may post in the room queues the note to the room organization's admins
// and learns whether it was queued for anyone. The same note about the
// same reply is sent once.
func (s *Service) handleRoomReport(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	roomID := r.PathValue("roomId")
	var req wire.RoomReportRequest
	if !httpx.DecodeJSON(w, r, 16<<10, &req) {
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" || !utf8.ValidString(text) || utf8.RuneCountInString(text) > wire.MaxRoomReportText {
		httpx.Error(w, fmt.Sprintf("the report needs a text of at most %d characters", wire.MaxRoomReportText), http.StatusBadRequest)
		return
	}
	rc, ok := s.roomClaims(claims, roomID)
	if m, member := s.roomMember(claims.Subject, roomID); !ok || !member || !m.MayChat() {
		httpx.Error(w, "only the room's members who may post in it report it", http.StatusForbidden)
		return
	}
	var admins []string
	if s.OrgAdmins != nil {
		var err error
		if admins, err = s.OrgAdmins(rc.Tenant); err != nil {
			obs.ServerError(w, "read the organization's admins failed", err)
			httpx.Error(w, "read the organization's admins failed", http.StatusInternalServerError)
			return
		}
	}
	queued := false
	if s.Alerts != nil && len(admins) > 0 {
		sum := sha256.Sum256([]byte(text))
		var err error
		queued, err = s.Alerts.Emit(tenant.NewContext(r.Context(), rc.Tenant), notify.Event{
			Type: wire.NotificationRoomReport, RoomID: roomID, Recipients: admins, Actor: claims.Subject,
			Title: claims.Subject + " reports what an agent could not do", Text: text,
			Seq: req.Seq, ChannelID: strings.TrimSpace(req.ChannelID),
			DedupKey: "room-report:" + roomID + ":" + claims.Subject + ":" + strconv.FormatInt(req.Seq, 10) + ":" + hex.EncodeToString(sum[:8]),
		})
		if err != nil {
			obs.ServerError(w, "queue the report failed", err)
			httpx.Error(w, "queue the report failed", http.StatusInternalServerError)
			return
		}
	}
	s.audit(r.Context(), rc.Tenant, store.ExtAuditRecord{
		Kind: wire.AuditKindAssist, Name: "room-report", Action: "report", Actor: claims.Subject,
		Outcome: wire.AuditOutcomeAllow, Detail: fmt.Sprintf("room %s, %d admins, queued %t", roomID, len(admins), queued),
	})
	httpx.WriteJSON(w, http.StatusOK, wire.RoomReportReply{Queued: queued})
}
