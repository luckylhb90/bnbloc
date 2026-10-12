package notify

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"time"

	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Reply from the messenger (N1). A notification that accepts a reply
// (wire.NotificationPayload.Reply) carries one opaque ReplyToken per
// recipient. The notifier component sends it back, with the member's text,
// to POST /v1/notifier/replies (the gateway), signed with the component's
// signing secret; the gateway resolves the token and posts the text into the
// room as that member through the normal chat path.
//
// Tokens are stored server-side (only their SHA-256), not self-contained
// signed tokens: they are minted by the delivering service (realtime), which
// holds no session-signing key; they stay short and opaque to the notifier
// and the messenger; and they are revocable — uninstalling a notifier
// deletes its tokens — with no dependency on any key rotation.

// ReplyTokenTTL bounds how long a notification can be answered.
const ReplyTokenTTL = 7 * 24 * time.Hour

// replyTokenPrefix marks a reply token (recognizable in a leaked log line).
const replyTokenPrefix = "bnr_"

// Reply token resolution errors.
var (
	ErrReplyTokenUnknown = errors.New("notify: unknown reply token")
	ErrReplyTokenExpired = errors.New("notify: reply token expired")
)

// ReplyTokens mints and resolves reply tokens over a store.
type ReplyTokens struct {
	Store store.NotificationReplyTokenStore
	Now   func() time.Time
}

func (r *ReplyTokens) now() time.Time {
	if r.Now != nil {
		return r.Now()
	}
	return time.Now()
}

// HashReplyToken is the stored (and looked-up) form of a token.
func HashReplyToken(token string) string {
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:])
}

// Mint issues a reply token for userID on payload p delivered through
// notifier. It returns "" (no error) when p accepts no reply or no store is
// configured: the notification is still delivered, it just cannot be
// answered.
func (r *ReplyTokens) Mint(ctx context.Context, notifier string, p wire.NotificationPayload, userID string) (string, error) {
	if r == nil || r.Store == nil || p.Reply == nil || p.RoomID == "" || userID == "" {
		return "", nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := replyTokenPrefix + hex.EncodeToString(b)
	now := r.now()
	row := store.NotificationReplyToken{
		TokenHash: HashReplyToken(token), TenantID: p.TenantID, Notifier: notifier, EventID: p.ID,
		RoomID: p.RoomID, ChannelID: p.Reply.ChannelID, UserID: userID,
		CreatedAtMs: now.UnixMilli(), ExpiresAtMs: now.Add(ReplyTokenTTL).UnixMilli(),
	}
	if rc := p.Reply.Recipient; rc != nil {
		row.ReplyOwnerID, row.ReplyProvider, row.ReplyCore = rc.OwnerID, rc.Provider, rc.Core
	}
	if err := r.Store.PutNotificationReplyToken(ctx, row); err != nil {
		return "", err
	}
	return token, nil
}

// Resolve returns the live row of token (ErrReplyTokenUnknown /
// ErrReplyTokenExpired otherwise).
func (r *ReplyTokens) Resolve(ctx context.Context, token string) (store.NotificationReplyToken, error) {
	if token == "" {
		return store.NotificationReplyToken{}, ErrReplyTokenUnknown
	}
	return r.resolveHash(ctx, HashReplyToken(token))
}

// resolveHash returns the live token row stored under tokenHash.
func (r *ReplyTokens) resolveHash(ctx context.Context, tokenHash string) (store.NotificationReplyToken, error) {
	if r == nil || r.Store == nil {
		return store.NotificationReplyToken{}, ErrReplyTokenUnknown
	}
	row, ok, err := r.Store.GetNotificationReplyToken(ctx, tokenHash)
	if err != nil {
		return store.NotificationReplyToken{}, err
	}
	if !ok {
		return store.NotificationReplyToken{}, ErrReplyTokenUnknown
	}
	if r.now().UnixMilli() >= row.ExpiresAtMs {
		return store.NotificationReplyToken{}, ErrReplyTokenExpired
	}
	return row, nil
}

// Prune deletes expired tokens (bounded batch).
func (r *ReplyTokens) Prune(ctx context.Context) (int64, error) {
	if r == nil || r.Store == nil {
		return 0, nil
	}
	return r.Store.PruneNotificationReplyTokens(ctx, r.now().UnixMilli(), 1000)
}

// ReplyClientMsgID is the chat idempotency id of a notifier reply: derived
// from the notifier, the member and the receiving system's message id, so a
// retried reply dedupes through the chat path's clientMsgId and two members
// (or two notifiers) can never collide.
func ReplyClientMsgID(notifier, userID, idempotencyKey string) string {
	sum := sha256.Sum256([]byte(notifier + "\x00" + userID + "\x00" + idempotencyKey))
	return "ntfr_" + hex.EncodeToString(sum[:16])
}

// roomReplyTo addresses a reply as human chat to the room channel (a
// mention: the member answers the people in the room).
func roomReplyTo(channelID string) *wire.NotificationReply {
	return &wire.NotificationReply{ChannelID: channelID}
}

// agentReplyTo addresses a reply to the agent a finished or failed job ran
// on, exactly as the member's request addressed it (owner, provider, core):
// replying continues the conversation with that agent, and borrow rules are
// enforced downstream as for any chat.
func agentReplyTo(channelID string, j wire.AgentJob) *wire.NotificationReply {
	if j.Provider == "" {
		return roomReplyTo(channelID)
	}
	return &wire.NotificationReply{ChannelID: channelID, Recipient: &wire.NotificationReplyRecipient{
		OwnerID: j.OwnerUserID, Provider: j.Provider, Core: j.Core,
	}}
}
