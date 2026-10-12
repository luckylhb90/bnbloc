package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/notify"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/rbac"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Reply from the messenger (N1): POST /v1/notifier/replies.
//
// A notifier component posts a member's reply to a notification it
// delivered. The route is public (no session: the caller is the notifier
// service, normally on the deployment's private network) and authenticated
// by the component's signing secret — the same X-Bnbloc-Signature scheme
// bnbloc signs deliveries with, here over the reply body, with the
// component named in X-Bnbloc-Component. The member, the room and the
// addressing come only from the reply token bnbloc minted for that member at
// delivery; the text is posted through realtime's ordinary chat endpoint as
// that member, so membership (at post time), the chat size limits, clientMsgId
// idempotency, agent-job creation and borrow enforcement are the chat path's
// own.
const (
	maxNotifierReplyBodyBytes = 64 * 1024
	// maxNotifierReplyTextBytes matches realtime's chat text cap (refused
	// here before the token is spent on a doomed post).
	maxNotifierReplyTextBytes = 16 * 1024
	maxReplyIdempotencyKey    = 200
	notifierReplyTimeout      = 10 * time.Second
)

func (s *Service) replyTokens() *notify.ReplyTokens {
	return &notify.ReplyTokens{Store: s.NotifierReplies, Now: s.nowTime}
}

// handleNotifierReply verifies a notifier's signed reply and posts it into
// the room as the token's member.
func (s *Service) handleNotifierReply(w http.ResponseWriter, r *http.Request) {
	ctx := r.Context()
	rec, body, ok := s.verifyNotifierCall(w, r)
	if !ok {
		return
	}
	var req wire.NotifierReplyRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httpx.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	key := strings.TrimSpace(req.IdempotencyKey)
	if key == "" || len(key) > maxReplyIdempotencyKey {
		httpx.Error(w, "idempotencyKey required (at most 200 bytes)", http.StatusBadRequest)
		return
	}
	if len(req.Text) > maxNotifierReplyTextBytes {
		httpx.Error(w, "message too long", http.StatusRequestEntityTooLarge)
		return
	}
	text := strings.TrimSpace(req.Text)
	if text == "" {
		httpx.Error(w, "empty message", http.StatusBadRequest)
		return
	}
	tok, err := s.resolveNotifierReply(ctx, rec.Name, req)
	switch {
	case errors.Is(err, errReplyFormAmbiguous):
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return
	case errors.Is(err, notify.ErrReplyTokenExpired):
		httpx.Error(w, "reply token expired", http.StatusGone)
		return
	case errors.Is(err, notify.ErrReplyTokenUnknown):
		httpx.Error(w, "unknown reply token", http.StatusNotFound)
		return
	case err != nil:
		obs.ServerError(w, "read reply token failed", err)
		httpx.Error(w, "read reply token failed", http.StatusInternalServerError)
		return
	case tok.Notifier != rec.Name:
		// A token is bound to the notifier it was delivered through.
		httpx.Error(w, "unknown reply token", http.StatusNotFound)
		return
	}
	if reason := s.notifierReplyDenied(ctx, rec, tok); reason != "" {
		s.auditNotifierReply(ctx, rec, tok, "deny", reason)
		httpx.Error(w, reason, http.StatusForbidden)
		return
	}
	if !s.rateAllow(tok.UserID) {
		httpx.Error(w, "rate limit exceeded", http.StatusTooManyRequests)
		return
	}
	seq, status, msg, cause := s.postNotifierReply(ctx, tok, text, notify.ReplyClientMsgID(rec.Name, tok.UserID, key))
	if status != http.StatusOK {
		s.auditNotifierReply(ctx, rec, tok, "deny", msg)
		httpx.WriteStatus(w, status, msg, cause)
		return
	}
	s.auditNotifierReply(ctx, rec, tok, "allow", "")
	httpx.WriteJSON(w, http.StatusOK, wire.NotifierReplyResponse{RoomID: tok.RoomID, Sequence: seq})
}

// notifierReplyDenied applies the distribution and org rules at post time:
// the notifier must still deliver to the token's tenant (rollout not off,
// tenant in scope, the member opted in to an opt-in notifier), the tenant
// must not be suspended, and the member must still belong to it with a role
// that may chat. Room membership is checked by realtime's chat endpoint.
func (s *Service) notifierReplyDenied(ctx context.Context, rec extpipe.Record, tok store.NotificationReplyToken) string {
	if !notify.Distributes(rec, tok.TenantID) {
		return "notifier is off or not enabled for this tenant"
	}
	if extpipe.NormalizeRollout(rec.Rollout) == extpipe.RolloutOptIn &&
		!notify.MemberInstalled(tenant.NewContext(ctx, tok.TenantID), s.ComponentInstalls, tok.UserID, rec.Name) {
		return "member has not turned this notifier on"
	}
	role, err := s.memberRole(tok.TenantID, tok.UserID)
	switch {
	case errors.Is(err, errNotTenantMember):
		return "member is no longer in the tenant"
	case err != nil:
		return "the member's organization cannot be checked"
	case s.tenantClosed(tok.TenantID, role):
		return "tenant suspended"
	case !rbac.Can(role, rbac.PermDriveAgent):
		return "insufficient org role"
	}
	return ""
}

// postNotifierReply posts text into the token's room as its member through
// realtime's chat endpoint (signed internal identity), addressed like the
// notification: to the agent for an agent outcome, to the room otherwise.
// It returns the sequence, or the status and message to answer with and
// the underlying error of a 5xx (logged by the handler).
func (s *Service) postNotifierReply(ctx context.Context, tok store.NotificationReplyToken, text, clientMsgID string) (int64, int, string, error) {
	if s.Backends.Realtime == "" {
		return 0, http.StatusServiceUnavailable, "realtime backend not configured", nil
	}
	chat := map[string]any{"text": text, "clientMsgId": clientMsgID}
	if tok.ChannelID != "" {
		chat["channelId"] = tok.ChannelID
	}
	if tok.ReplyProvider != "" {
		rc := map[string]any{"ownerId": tok.ReplyOwnerID, "provider": tok.ReplyProvider}
		if tok.ReplyCore != "" {
			rc["core"] = tok.ReplyCore
		}
		chat["recipient"] = rc
	}
	raw, err := json.Marshal(chat)
	if err != nil {
		return 0, http.StatusInternalServerError, "encode chat failed", err
	}
	ctx, cancel := context.WithTimeout(ctx, notifierReplyTimeout)
	defer cancel()
	target := strings.TrimRight(s.Backends.Realtime, "/") + "/v1/rooms/" + url.PathEscape(tok.RoomID) + "/chat"
	resp, err := callBackend(ctx, http.MethodPost, target, raw, tok.UserID, tok.TenantID)
	if err != nil {
		return 0, http.StatusBadGateway, "realtime unreachable", err
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		msg := httpx.ErrorText(answer)
		switch {
		case resp.StatusCode == http.StatusForbidden:
			return 0, http.StatusForbidden, "member is no longer in the room", nil
		case resp.StatusCode >= 500:
			return 0, http.StatusBadGateway, "post failed", fmt.Errorf("realtime chat answered %d: %s", resp.StatusCode, msg)
		case msg == "":
			msg = http.StatusText(resp.StatusCode)
		}
		return 0, resp.StatusCode, msg, nil
	}
	var out struct {
		Sequence int64 `json:"sequence"`
	}
	if err := json.Unmarshal(answer, &out); err != nil {
		return 0, http.StatusBadGateway, "undecodable realtime answer", err
	}
	return out.Sequence, http.StatusOK, "", nil
}

// auditNotifierReply records a reply decision in the extension audit trail
// (ids only, never the text or the token).
func (s *Service) auditNotifierReply(ctx context.Context, rec extpipe.Record, tok store.NotificationReplyToken, outcome, reason string) {
	if s.ExtAudit == nil {
		return
	}
	detail := "room=" + tok.RoomID + " event=" + tok.EventID
	if reason != "" {
		detail += ": " + reason
	}
	if err := s.ExtAudit.AppendExtAudit(ctx, store.ExtAuditRecord{
		Kind: rec.Kind, Name: rec.Name, Action: "reply", Actor: tok.UserID, Outcome: outcome,
		Detail: detail, TenantID: tok.TenantID, AtMs: s.nowTime().UnixMilli(),
	}); err != nil {
		slog.Warn("bnbloc-gateway: audit notifier reply failed", "notifier", rec.Name, "error", err)
	}
}
