package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/extgov"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/extreg"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/notify"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/roomlog"
	"github.com/luckylhb90/bnbloc/internal/sharecard"
	"github.com/luckylhb90/bnbloc/internal/sharetarget"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Share targets (docs/prd/2026-10-21-share-target-components.md): a member
// shares a room, a message or a deliverable, as a card, to a conversation of
// another app through an installed share-target component.
//
// The flow has two halves. The member starts a session in the web (the item
// is read as the member, the card is built and signed into a ticket); the
// host app's own page, which the component serves and which runs in that
// app's client where its bridge is, picks the conversations and sends the
// card by ticket. The gateway is the one place that decides what the card
// says and that calls the target (signed with its credential, audited).

// maxShareBody bounds a request of the share flow: ids, a bounded destination
// list and the other app's token.
const maxShareBody = 32 << 10

// shareTargetTicketTTL is how long a started share can be completed: long enough to
// pick conversations and confirm, short enough that a leaked address is of
// little use.
const shareTargetTicketTTL = 15 * time.Minute

// shareTargetsPrefix is where the share page and its two calls live; a path
// under it answers CORS for the page's opaque origin (shareTicketPath).
const shareTargetsPrefix = "/v1/share-targets/"

// shareTicketPath reports a path under the share page's base: answered with
// CORS for any origin, without credentials (withCORS): the page is sandboxed,
// so its origin is opaque, and its credential is the ticket in the path.
func shareTicketPath(path string) bool { return strings.HasPrefix(path, shareTargetsPrefix) }

// shareTargets lists the installed share targets distributed to the
// organization (extpipe.TenantDistribution: an off rollout or a tenant
// outside the scope hides it, like a notifier; a share target is the
// organization's infrastructure and has no per-member install).
func (s *Service) shareTargets(ctx context.Context, tenantID string) ([]extpipe.Record, error) {
	recs, err := s.corePipeline().List(ctx, extreg.KindShareTarget)
	if err != nil {
		return nil, err
	}
	out := recs[:0:0]
	for _, rec := range recs {
		if extpipe.TenantDistribution(rec, tenantID).Distributed {
			out = append(out, rec)
		}
	}
	return out, nil
}

// shareTarget is the named target distributed to tenantID, or false.
func (s *Service) shareTarget(ctx context.Context, tenantID, name string) (extpipe.Record, bool, error) {
	recs, err := s.shareTargets(ctx, tenantID)
	if err != nil {
		return extpipe.Record{}, false, err
	}
	for _, rec := range recs {
		if rec.Name == name {
			return rec, true, nil
		}
	}
	return extpipe.Record{}, false, nil
}

// handleShareTargets lists the share targets the member may use
// (GET /v1/my/share-targets).
func (s *Service) handleShareTargets(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	recs, err := s.shareTargets(r.Context(), claims.Tenant)
	if err != nil {
		obs.ServerError(w, "load share targets failed", err)
		httpx.Error(w, "load share targets failed", http.StatusInternalServerError)
		return
	}
	out := wire.ShareTargetList{Targets: make([]wire.ShareTargetInfo, 0, len(recs))}
	for _, rec := range recs {
		out.Targets = append(out.Targets, wire.ShareTargetInfo{Name: rec.Name, Version: rec.Version})
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// handleShareSession starts a share (POST /v1/my/share-targets/{name}/sessions):
// the item is checked, read as the member and turned into a card, and the
// card is signed into a ticket that names the member, their organization and
// the target. The answer is the share page's address.
func (s *Service) handleShareSession(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	var req wire.ShareSessionRequest
	if !httpx.DecodeJSON(w, r, maxShareBody, &req) {
		return
	}
	if msg := checkShareItem(req.Item); msg != "" {
		httpx.Error(w, msg, http.StatusBadRequest)
		return
	}
	rec, found, err := s.shareTarget(r.Context(), claims.Tenant, r.PathValue("name"))
	if err != nil {
		obs.ServerError(w, "load share targets failed", err)
		httpx.Error(w, "load share targets failed", http.StatusInternalServerError)
		return
	}
	if !found {
		httpx.Error(w, "share target not found", http.StatusNotFound)
		return
	}
	origin := s.publicOrigin(r)
	card, err := s.shareCard(r.Context(), claims, origin, req.Item)
	switch {
	case errors.Is(err, errAssistNotFound), errors.Is(err, errSiteNotFound):
		httpx.Error(w, "item not found", http.StatusNotFound)
		return
	case err != nil:
		obs.ServerError(w, "read the shared item failed", err)
		httpx.Error(w, "read the shared item failed", http.StatusBadGateway)
		return
	}
	data, err := json.Marshal(card)
	if err != nil {
		obs.ServerError(w, "encode the card failed", err)
		httpx.Error(w, "encode the card failed", http.StatusInternalServerError)
		return
	}
	ticket := s.Signer.Issue(auth.Claims{
		Subject: claims.Subject, Tenant: claims.Tenant, Role: claims.Role,
		ExpiresAtUnix: s.nowTime().Add(shareTargetTicketTTL).Unix(),
		Kind:          auth.KindShareTarget, ID: rec.Name, Data: string(data),
	})
	httpx.WriteJSON(w, http.StatusOK, wire.ShareSession{URL: origin + shareTargetsPrefix + url.PathEscape(rec.Name) + "/pages/" + ticket + "/"})
}

// shareTicket verifies the ticket in the path and that it names the target in
// the path; it writes the refusal otherwise. The card it carries is decoded.
func (s *Service) shareTicket(w http.ResponseWriter, r *http.Request) (auth.Claims, wire.ShareCard, bool) {
	claims, err := s.Signer.VerifyKind(r.PathValue("ticket"), auth.KindShareTarget)
	if err != nil || claims.ID != r.PathValue("name") {
		httpx.Error(w, "the share link expired or is not valid: start sharing again from bnbloc", http.StatusUnauthorized)
		return auth.Claims{}, wire.ShareCard{}, false
	}
	var card wire.ShareCard
	if json.Unmarshal([]byte(claims.Data), &card) != nil {
		httpx.Error(w, "the share link is not valid", http.StatusUnauthorized)
		return auth.Claims{}, wire.ShareCard{}, false
	}
	return claims, card, true
}

// shareTicketRecord is the target a ticket names, distributed to the ticket's
// organization (a target taken off in the meantime stops its tickets).
func (s *Service) shareTicketRecord(w http.ResponseWriter, r *http.Request, claims auth.Claims) (extpipe.Record, bool) {
	rec, found, err := s.shareTarget(r.Context(), claims.Tenant, claims.ID)
	switch {
	case err != nil:
		obs.ServerError(w, "load share targets failed", err)
		httpx.Error(w, "load share targets failed", http.StatusInternalServerError)
		return extpipe.Record{}, false
	case !found:
		httpx.Error(w, "share target not found", http.StatusNotFound)
		return extpipe.Record{}, false
	}
	return rec, true
}

// handleSharePage serves the share page: the component's document, as the
// gateway read it from the target, opened by the member's client at the top
// level. A sandbox policy gives it an opaque origin (it cannot read the web
// client's session or storage), and it is never cached or sent as a referrer:
// its address carries the ticket.
func (s *Service) handleSharePage(w http.ResponseWriter, r *http.Request) {
	claims, _, ok := s.shareTicket(w, r)
	if !ok {
		return
	}
	rec, ok := s.shareTicketRecord(w, r, claims)
	if !ok {
		return
	}
	ep, err := sharetarget.Kind.Endpoint(rec, extgov.Guard{Net: extgov.NewNetGuard(rec.Network)})
	if err != nil {
		httpx.Error(w, "the share target is not usable", http.StatusConflict)
		return
	}
	page, err := sharetarget.FetchPage(r.Context(), ep)
	if err != nil {
		obs.ServerError(w, "read the share page failed", err)
		httpx.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", "sandbox allow-scripts allow-forms allow-modals")
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("Cache-Control", "no-store")
	h.Set("X-Content-Type-Options", "nosniff")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(page)
}

// handleShareSessionInfo answers what the page reads of its session: the card,
// the address to send it to and the link back to the shared item
// (GET <page URL>session).
func (s *Service) handleShareSessionInfo(w http.ResponseWriter, r *http.Request) {
	_, card, ok := s.shareTicket(w, r)
	if !ok {
		return
	}
	base := s.publicOrigin(r) + shareTargetsPrefix + url.PathEscape(r.PathValue("name")) + "/pages/" + r.PathValue("ticket") + "/"
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, wire.ShareSessionInfo{Card: card, SendURL: base + "send", BackURL: card.URL})
}

// handleShareSend sends the session's card (POST <page URL>send): the ticket
// names the member, the target and the card, so the page chooses only the
// conversations. The target is called signed with its credential and the
// outcome audited (the item's kind and the result, never the card text or the
// other app's token).
func (s *Service) handleShareSend(w http.ResponseWriter, r *http.Request) {
	claims, card, ok := s.shareTicket(w, r)
	if !ok {
		return
	}
	var req wire.ShareSend
	if !httpx.DecodeJSON(w, r, maxShareBody, &req) {
		return
	}
	if msg := checkShareDest(req); msg != "" {
		httpx.Error(w, msg, http.StatusBadRequest)
		return
	}
	rec, ok := s.shareTicketRecord(w, r, claims)
	if !ok {
		return
	}
	ep, err := sharetarget.Kind.Endpoint(rec, extgov.Guard{Net: extgov.NewNetGuard(rec.Network)})
	if err != nil {
		httpx.Error(w, "the share target is not usable: "+err.Error(), http.StatusConflict)
		return
	}
	secret, hasSecret, err := s.componentCredentials().Get(r.Context(), rec.Kind, rec.Name)
	switch {
	case err != nil:
		obs.ServerError(w, "read share target credential failed", err)
		httpx.Error(w, "read share target credential failed", http.StatusInternalServerError)
		return
	case !hasSecret:
		httpx.Error(w, "the share target has no credential; rotate one first", http.StatusConflict)
		return
	}
	err = sharetarget.Send(r.Context(), ep, secret, sharetarget.Share{
		DeliveryID: req.DeliveryID, Dest: req.Dest, Card: card,
		Sharer: sharetarget.Sharer{Subject: claims.Subject}, HostToken: req.HostToken,
	}, s.nowTime())
	if err != nil {
		// A Refusal's text is the target's reason; a transport failure's is
		// already a fixed sentence (sharetarget.Send): neither holds the token.
		obs.ServerError(w, "share target did not deliver", err)
		s.auditComponent(r.Context(), claims, rec, "share", wire.AuditOutcomeFailed, card.Kind)
		httpx.Error(w, err.Error(), http.StatusBadGateway)
		return
	}
	s.auditComponent(r.Context(), claims, rec, "share", wire.AuditOutcomeDelivered, card.Kind)
	httpx.WriteJSON(w, http.StatusOK, wire.ShareResult{Status: "ok"})
}

// checkShareDest is the send request's one shape rule: the message of the
// first thing wrong, "" when it is well formed.
func checkShareDest(req wire.ShareSend) string {
	if id := strings.TrimSpace(req.DeliveryID); id == "" || len(id) > wire.MaxShareIDBytes {
		return "deliveryId is required (at most 128 bytes)"
	}
	if tok := strings.TrimSpace(req.HostToken); tok == "" || len(req.HostToken) > wire.MaxShareHostTokenBytes {
		return "hostToken is required"
	}
	if n := len(req.Dest); n == 0 || n > wire.MaxShareDest {
		return "dest needs 1 to 20 conversations"
	}
	for _, d := range req.Dest {
		if d.Type != wire.ShareDestUser && d.Type != wire.ShareDestGroup {
			return `dest type must be "user" or "group"`
		}
		if id := strings.TrimSpace(d.ID); id == "" || len(id) > wire.MaxShareIDBytes {
			return "a dest id is required (at most 128 bytes)"
		}
	}
	return ""
}

// checkShareItem is the item's shape rule: the message of the first thing
// wrong, "" when it is well formed.
func checkShareItem(it wire.ShareItem) string {
	for _, id := range []string{it.RoomID, it.ChannelID, it.ArtifactID} {
		if len(id) > wire.MaxShareIDBytes {
			return "an item id is too long"
		}
	}
	switch it.Kind {
	case wire.ShareItemRoom:
		if it.RoomID == "" {
			return "a room item needs roomId"
		}
	case wire.ShareItemMessage:
		if it.RoomID == "" || it.Seq <= 0 {
			return "a message item needs roomId and seq"
		}
	case wire.ShareItemDeliverable:
		if it.RoomID == "" || it.ArtifactID == "" {
			return "a deliverable item needs roomId and artifactId"
		}
	default:
		return "unknown item kind"
	}
	return ""
}

// shareCard reads the item as the member (the room service, realtime and
// the artifact routes decide what they may read; a room they are not in is
// errAssistNotFound) and builds its card. The link is the item's member
// link: opening it signs the viewer in and checks their access like any
// other.
func (s *Service) shareCard(ctx context.Context, claims auth.Claims, origin string, it wire.ShareItem) (sharecard.Card, error) {
	room, err := s.memberRoom(ctx, claims, it.RoomID)
	if err != nil {
		return sharecard.Card{}, err
	}
	switch it.Kind {
	case wire.ShareItemRoom:
		return sharecard.New(it.Kind, room.Name, room.Description, notify.RoomURL(origin, it.RoomID)), nil
	case wire.ShareItemDeliverable:
		art, _, err := s.deliverableVersion(ctx, claims, it.RoomID, it.ArtifactID)
		if err != nil {
			return sharecard.Card{}, err
		}
		title := firstNonEmpty(art.Title, art.FileName)
		return sharecard.New(it.Kind, title, room.Name, notify.ArtifactURL(origin, it.RoomID, it.ArtifactID, it.Version)), nil
	default:
		// The newest row below seq+1 is the message itself or an earlier
		// one: only the message with that sequence counts.
		rows, err := s.roomHistory(ctx, memberReader(claims), it.RoomID, it.ChannelID, it.Seq+1, 1)
		if err != nil {
			return sharecard.Card{}, err
		}
		for _, row := range rows {
			if row.Sequence != it.Seq {
				continue
			}
			if entries := roomlog.DecodeRow(row); len(entries) > 0 {
				return sharecard.New(it.Kind, room.Name, entries[0].Text, notify.MessageURL(origin, it.RoomID, it.ChannelID, it.Seq)), nil
			}
		}
		return sharecard.Card{}, errAssistNotFound
	}
}
