package notify

import (
	"context"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/internal/store"
)

// Reply references (R9). A notifier that answers replies by "the message the
// member quoted" would otherwise keep (sent message id, recipient) → reply
// token in its own memory, which breaks with several notifier replicas or
// after a restart. Instead it registers the binding with bnbloc right after
// sending (POST /v1/notifier/reply-refs) and later posts the reply with the
// quoted message id and the replying account (POST /v1/notifier/replies
// {ref, sender}); bnbloc resolves the token. Matching on the recipient
// account is the server-side check that the reply comes from the member the
// message was sent to: a forwarded message quoted by someone else resolves
// to nothing.

// MaxReplyRefLen bounds a reference and an account id (characters).
const MaxReplyRefLen = 128

// ErrReplyRefInvalid reports a missing or over-long reference / account id.
var ErrReplyRefInvalid = errors.New("notify: ref and recipient are required (at most 128 characters each)")

// ErrReplyRefConflict reports a reference already bound to another token.
var ErrReplyRefConflict = errors.New("notify: reference already bound to another reply token")

// ValidReplyRef reports whether v is a usable reference or account id.
func ValidReplyRef(v string) bool {
	return strings.TrimSpace(v) == v && v != "" && utf8.ValidString(v) && utf8.RuneCountInString(v) <= MaxReplyRefLen
}

// Register binds (notifier, ref, recipient) to the live token the notifier
// was delivered. The token must belong to notifier (ErrReplyTokenUnknown
// otherwise, as for a reply); the reference expires with the token.
func (r *ReplyTokens) Register(ctx context.Context, notifier, token, ref, recipient string) (store.NotificationReplyToken, error) {
	if !ValidReplyRef(ref) || !ValidReplyRef(recipient) {
		return store.NotificationReplyToken{}, ErrReplyRefInvalid
	}
	tok, err := r.Resolve(ctx, token)
	if err != nil {
		return store.NotificationReplyToken{}, err
	}
	if tok.Notifier != notifier {
		return store.NotificationReplyToken{}, ErrReplyTokenUnknown
	}
	err = r.Store.PutNotificationReplyRef(ctx, store.NotificationReplyRef{
		Notifier: notifier, Ref: ref, Recipient: recipient, TokenHash: tok.TokenHash,
		CreatedAtMs: r.now().UnixMilli(), ExpiresAtMs: tok.ExpiresAtMs,
	})
	if errors.Is(err, store.ErrNotificationReplyRefConflict) {
		return store.NotificationReplyToken{}, ErrReplyRefConflict
	}
	if err != nil {
		return store.NotificationReplyToken{}, err
	}
	return tok, nil
}

// ResolveRef returns the live token a reply quoting ref, sent by the
// receiving system's account sender, answers through notifier:
// ErrReplyTokenUnknown when nothing was registered for that pair (including
// a sender other than the message's recipient), ErrReplyTokenExpired once
// the token expired.
func (r *ReplyTokens) ResolveRef(ctx context.Context, notifier, ref, sender string) (store.NotificationReplyToken, error) {
	if r == nil || r.Store == nil || !ValidReplyRef(ref) || !ValidReplyRef(sender) {
		return store.NotificationReplyToken{}, ErrReplyTokenUnknown
	}
	row, ok, err := r.Store.GetNotificationReplyRef(ctx, notifier, ref, sender)
	if err != nil {
		return store.NotificationReplyToken{}, err
	}
	if !ok {
		return store.NotificationReplyToken{}, ErrReplyTokenUnknown
	}
	if r.now().UnixMilli() >= row.ExpiresAtMs {
		return store.NotificationReplyToken{}, ErrReplyTokenExpired
	}
	tok, err := r.resolveHash(ctx, row.TokenHash)
	if err != nil {
		return store.NotificationReplyToken{}, err
	}
	if tok.Notifier != notifier {
		return store.NotificationReplyToken{}, ErrReplyTokenUnknown
	}
	return tok, nil
}
