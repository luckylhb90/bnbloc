package realtimehttp

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"time"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/notify"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// The channel gate (docs/prd/2026-10-28-channels-and-threads.md, slice 1):
// whether a member may post in a channel now, by the channel's and the
// room's setup. It is asked by PostChat only, so every post (typed,
// scheduled, dispatched) meets it once, after the membership check (Chat:
// visitors never post) and before anything is stored:
//
//   - an archived channel is read-only (C10);
//   - "admins" posting: only the room's owner and admins post; an agent an
//     admin asks still replies (its reply is not a post);
//   - the room's rules: when the room requires agreeing (C16), a member who
//     has not agreed to the current version is refused;
//   - slow mode (C22): a member who posted in the channel less than its
//     interval ago is refused with how long to wait.
//
// The last step records the post (store.ClaimChannelPost): posting joins
// the channel (C18), in the same write that enforces slow mode.

// postRefusal is a post the channel's setup refuses: its status, the stable
// code the web shows (wire), the message, and for slow mode the wait.
type postRefusal struct {
	status       int
	code         string
	msg          string
	retryAfterMs int64
}

func (e postRefusal) Error() string { return e.msg }

// writePostRefusal answers a refused post with the one error body, its code
// and, for slow mode, the wait (also as Retry-After, in whole seconds).
func writePostRefusal(w http.ResponseWriter, e postRefusal) {
	if e.retryAfterMs > 0 {
		w.Header().Set("Retry-After", strconv.FormatInt((e.retryAfterMs+999)/1000, 10))
	}
	httpx.WriteJSON(w, e.status, httpx.ErrorBody{Error: e.msg, Code: e.code, RetryAfterMs: e.retryAfterMs})
}

// admitPost is the one owner of "may author post in ch now". m is the
// author's membership (PostChat's Chat check). It returns a postRefusal or a
// read failure; on success the post is recorded (the author follows the
// channel).
func (s *Service) admitPost(ctx context.Context, roomID, author string, m membership.Member, ch store.RoomChannel) error {
	// Every rule below applies to a scheduled post and a dispatch (an agent's
	// task posted as its requester) exactly as to a typed one, which refuses
	// them with the same code (decision: pending owner; the PRD does not say
	// whether such posts are exempt, and refusing is the fail-closed reading).
	// decision: channels PRD C10 (an archived channel is read-only).
	if ch.Archived() {
		return postRefusal{status: http.StatusForbidden, code: wire.ChannelArchived, msg: "this channel is archived: it is read-only until an owner restores it"}
	}
	// decision: channels PRD §2.1 "who posts" (admins: the room's owner and
	// admins, store.Role.CanInvite, the room's one owner-or-admin rule).
	if ch.Settings.WhoPosts == wire.ChannelPostersAdmins && !m.Role.CanInvite() {
		return postRefusal{status: http.StatusForbidden, code: wire.ChannelAdminsOnly, msg: "only the room's owner and admins post in this channel"}
	}
	if err := s.checkRulesAgreed(ctx, roomID, author); err != nil {
		return err
	}
	if s.Channels == nil || ch.ChannelID == "" {
		return nil
	}
	// decision: channels PRD C22 (slow mode is the channel owners' value, no
	// platform default) and C18 (posting joins). Slow mode applies to every
	// post of the member, typed, scheduled or dispatched as them, and to
	// owners and admins alike (decision: pending owner; the PRD names no
	// exemption, so none is made). The claim is taken in the owner's memory
	// before the message is stored, so two posts cannot both pass; a post
	// whose store then fails has still spent the interval (decision:
	// pending owner). Its mark is written behind (post_marks.go).
	gap := int64(ch.Settings.SlowModeSeconds) * 1000
	wait, err := s.postMarks.claim(ctx, s, roomID, ch.ChannelID, author, s.now().UnixMilli(), gap)
	if err != nil {
		return fmt.Errorf("channel post claim failed: %w", err)
	}
	if wait > 0 {
		return postRefusal{status: http.StatusTooManyRequests, code: wire.ChannelSlowMode, retryAfterMs: wait,
			msg: fmt.Sprintf("slow mode: you can post in this channel again in %s", time.Duration(wait)*time.Millisecond)}
	}
	return nil
}

// checkRulesAgreed refuses a member who must agree to the room's current
// rules first (C16). Visitors never reach it (they do not post). No room
// reader (in-memory dev, tests) has no rules to agree to.
func (s *Service) checkRulesAgreed(ctx context.Context, roomID, author string) error {
	if s.RoomRoles == nil {
		return nil
	}
	room, err := s.roleStore().GetRoom(ctx, roomID)
	if err != nil {
		return fmt.Errorf("room read failed: %w", err)
	}
	if !room.RequireRulesAgreement {
		return nil
	}
	mem, ok, err := s.roleStore().GetMember(ctx, roomID, author)
	if err != nil {
		return fmt.Errorf("membership read failed: %w", err)
	}
	// decision: channels PRD C16 (a change to the rules asks once more: an
	// agreement to an older version is no agreement). No membership row is
	// refused as not agreed (fail closed).
	if !ok || room.RulesAgreementDue(mem) {
		return postRefusal{status: http.StatusForbidden, code: wire.RulesAgreementRequired,
			msg: fmt.Sprintf("this room asks you to agree to its rules (version %d) before you post", room.RulesVersion)}
	}
	return nil
}

// joinMentioned makes the room's human members a posted message mentions
// follow its channel (C18: being mentioned joins). Mentions are read by the
// one parser notifications use (notify.MentionCandidates) and count only for
// a human member. Best effort: a failed write is logged, never fails the post
// that is already delivered.
func (s *Service) joinMentioned(ctx context.Context, roomID, channelID, author, text string) {
	if s.Channels == nil || channelID == "" {
		return
	}
	for _, c := range notify.MentionCandidates(text) {
		if c == author || !membership.HumanMember(s.Membership, roomID, c) || !s.memberMayReadChannel(ctx, roomID, channelID, c) {
			continue
		}
		if err := s.channelStore().FollowChannel(ctx, roomID, channelID, c, s.now().UnixMilli(), store.FollowMarks{}); err != nil {
			log.Printf("realtime: room=%s channel=%s: a mention's join failed for %s: %v", roomID, channelID, c, err)
		}
	}
}

// isPostRefusal reports a refusal of the channel gate.
func isPostRefusal(err error) (postRefusal, bool) {
	var e postRefusal
	return e, errors.As(err, &e)
}
