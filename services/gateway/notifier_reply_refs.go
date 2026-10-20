package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/extreg"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/notify"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Stateless notifier replies (R9): POST /v1/notifier/reply-refs.
//
// A notifier that recognizes a reply by the message the member quoted
// registers, right after sending, which of its sent messages (ref) went to
// which receiving-system account (recipient) with which reply token. A reply
// then names {ref, sender} instead of the token (POST /v1/notifier/replies),
// so no notifier replica needs process memory: whichever replica receives
// the reply resolves it here. The recipient check — the reply comes from the
// account the message was sent to — is bnbloc's: a reference resolves only
// for the account it was registered with.

// errReplyFormAmbiguous: a reply names neither or both of {replyToken} and
// {ref, sender}.
var errReplyFormAmbiguous = errors.New("send either replyToken or ref with sender")

// verifyNotifierCall authenticates a notifier's signed call (N1 scheme):
// X-Bnbloc-Component names the notifier, and its signature covers the raw
// body (verifyComponentCall). It answers the refusal itself and returns
// ok=false then.
func (s *Service) verifyNotifierCall(w http.ResponseWriter, r *http.Request) (extpipe.Record, []byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxNotifierReplyBodyBytes))
	if err != nil {
		httpx.Error(w, "body too large or unreadable", http.StatusRequestEntityTooLarge)
		return extpipe.Record{}, nil, false
	}
	if s.NotifierReplies == nil {
		obs.ServerError(w, "notifier replies are not available", nil)
		httpx.Error(w, "notifier replies are not available", http.StatusServiceUnavailable)
		return extpipe.Record{}, nil, false
	}
	rec, ok := s.verifyComponentCall(w, r, extreg.KindNotifier, strings.TrimSpace(r.Header.Get(wire.ComponentHeader)), body)
	return rec, body, ok
}

// resolveNotifierReply resolves the token a reply answers through notifier:
// the token itself, or the registered reference {ref, sender}.
func (s *Service) resolveNotifierReply(ctx context.Context, notifier string, req wire.NotifierReplyRequest) (store.NotificationReplyToken, error) {
	token, ref, sender := strings.TrimSpace(req.ReplyToken), req.Ref, req.Sender
	byRef := ref != "" || sender != ""
	switch {
	case token != "" && byRef, token == "" && (ref == "" || sender == ""):
		return store.NotificationReplyToken{}, errReplyFormAmbiguous
	case byRef:
		return s.replyTokens().ResolveRef(ctx, notifier, ref, sender)
	}
	return s.replyTokens().Resolve(ctx, token)
}

// handleNotifierReplyRef registers a reply reference for a token the calling
// notifier was delivered. 204 on success (re-registering the same binding
// is a no-op); 400 malformed or a ref/recipient over 128 characters; 404 an
// unknown token or one delivered through another notifier; 409 the
// reference is already bound to another token; 410 the token expired.
func (s *Service) handleNotifierReplyRef(w http.ResponseWriter, r *http.Request) {
	rec, body, ok := s.verifyNotifierCall(w, r)
	if !ok {
		return
	}
	var req wire.NotifierReplyRefRequest
	if err := json.Unmarshal(body, &req); err != nil {
		httpx.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	_, err := s.replyTokens().Register(r.Context(), rec.Name, strings.TrimSpace(req.ReplyToken), req.Ref, req.Recipient)
	switch {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.Is(err, notify.ErrReplyRefInvalid):
		httpx.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, notify.ErrReplyTokenUnknown):
		httpx.Error(w, "unknown reply token", http.StatusNotFound)
	case errors.Is(err, notify.ErrReplyTokenExpired):
		httpx.Error(w, "reply token expired", http.StatusGone)
	case errors.Is(err, notify.ErrReplyRefConflict):
		httpx.Error(w, "reference already registered for another reply token", http.StatusConflict)
	default:
		obs.ServerError(w, "register reply reference failed", err)
		httpx.Error(w, "register reply reference failed", http.StatusInternalServerError)
	}
}
