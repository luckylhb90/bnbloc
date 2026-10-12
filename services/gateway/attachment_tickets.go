package gateway

import (
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/roomctx"
)

// Chat attachments render as <img src> / <a href download>, which cannot
// carry the session bearer. A member mints a ticket for ONE attachment
// (POST …/attachments/{attachmentId}/ticket) and the URL carries that
// instead (?ticket=): copying the link, browser history and proxy logs see
// a credential that reads one file for a few minutes, never the session.
// Same mechanism as the site ticket (sites.go): a signed token of its own
// kind whose ID names what it reads. The download is an ordinary proxied
// read, so the room scope and realtime's membership check run on every use.

// attachmentTicketTTL bounds an attachment ticket. The client re-mints an
// expired one when a link is opened again (web lib/attachmentTickets.ts).
const attachmentTicketTTL = 15 * time.Minute

// attachmentTicketScope is a ticket's ID: the one attachment it reads.
func attachmentTicketScope(roomID, attachmentID string) string {
	return roomID + "/" + attachmentID
}

// handleAttachmentTicket mints a member's attachment ticket:
// POST /v1/rooms/{roomId}/attachments/{attachmentId}/ticket →
// {url: "/v1/rooms/{roomId}/attachments/{attachmentId}?ticket=…",
// expiresAtMs}. The caller must reach the room; whether the attachment
// exists and the caller is still a member is decided when it is read.
func (s *Service) handleAttachmentTicket(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	roomID, attachmentID := r.PathValue("roomId"), r.PathValue("attachmentId")
	if roomctx.ValidateID(roomID) != nil || attachmentID == "" || strings.Contains(attachmentID, "/") {
		httpx.Error(w, "invalid attachment path", http.StatusBadRequest)
		return
	}
	claims, ok := s.roomClaims(claims, roomID)
	if !ok {
		httpx.Error(w, "room scope not authorized", http.StatusForbidden)
		return
	}
	expires := s.nowTime().Add(attachmentTicketTTL)
	ticket := s.Signer.Issue(auth.Claims{
		Subject:       claims.Subject,
		Tenant:        claims.Tenant,
		Role:          claims.Role,
		ExpiresAtUnix: expires.Unix(),
		Kind:          auth.KindAttachment,
		ID:            attachmentTicketScope(roomID, attachmentID),
	})
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"url": "/v1/rooms/" + url.PathEscape(roomID) + "/attachments/" + url.PathEscape(attachmentID) +
			"?ticket=" + url.QueryEscape(ticket),
		"expiresAtMs": expires.UnixMilli(),
	})

}

// attachmentTicketClaims authenticates an attachment download by its
// ?ticket=: valid only for the attachment the path names. The tenant is the
// one the ticket was minted in (the room's), so no acting-tenant input
// applies.
func (s *Service) attachmentTicketClaims(r *http.Request, p apiPath) (auth.Claims, bool) {
	ticket := strings.TrimSpace(r.URL.Query().Get("ticket"))
	if ticket == "" || !p.isAttachmentDownload(r.Method) {
		return auth.Claims{}, false
	}
	c, err := s.Signer.VerifyKind(ticket, auth.KindAttachment)
	if err != nil || c.ID != attachmentTicketScope(p.RoomID, p.segs[4]) {
		return auth.Claims{}, false
	}
	return c, true
}
