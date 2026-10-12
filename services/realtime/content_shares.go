package realtimehttp

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/audit"
	"github.com/luckylhb90/bnbloc/internal/contentshare"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Room shares reach later joiners (docs/prd/2026-10-12-experts-and-
// teams.md, "Gap closure" 2): bnbloc-room publishes member_joined when a
// person becomes a member of a room, and this publish tap gives them a copy
// of each content component an author shared with the room and has not
// revoked, as a share would have (contentshare.Copy), with the
// content_shared notification and a share audit row. Best-effort and
// detached: the join already succeeded, and a failure is logged.

// memberJoinedTimeout bounds the copies of one join.
const memberJoinedTimeout = 30 * time.Second

// onMemberJoined starts the room-share step for a member_joined event.
func (s *Service) onMemberJoined(ctx context.Context, ev wire.Event) {
	if s.Content == nil || ev.Kind != wire.EventMemberJoined {
		return
	}
	member := joinedMember(ev)
	tid := tenant.FromContext(ctx)
	if member == "" || tid == "" {
		return
	}
	go func() {
		ctx, cancel := context.WithTimeout(tenant.NewContext(context.WithoutCancel(ctx), tid), memberJoinedTimeout)
		defer cancel()
		s.grantRoomShares(ctx, tid, ev.RoomID, member)
	}()
}

// grantRoomShares copies the room's standing shares to member (a human
// member of the room), then notifies and audits each copy created or moved.
func (s *Service) grantRoomShares(ctx context.Context, tid, roomID, member string) {
	human := func(user string) bool { return membership.HumanMember(s.Membership, roomID, user) }
	if !human(member) {
		return
	}
	now := time.Now().UnixMilli()
	grants, err := contentshare.GrantRoomShares(ctx, s.Content, tid, roomID, member, human, now)
	if err != nil {
		slog.Warn("bnbloc realtime: room shares for a new member failed", "event", "bnbloc.content.room_share_failed",
			"room", roomID, "member", member, "error", err)
	}
	for _, g := range grants {
		if g.Result.Status == wire.ContentShareUnchanged {
			continue
		}
		label := contentshare.Label(g.Source)
		if s.Audit != nil {
			audit.Record(ctx, s.Audit, store.ExtAuditRecord{
				Kind: wire.AuditKindContent, Name: g.Source.Name + "@" + g.Share.Version, Action: "share", Actor: g.Share.SharedBy,
				TenantID: tid, Outcome: wire.AuditOutcomeAllow, AtMs: now,
				Detail: label + " v" + g.Share.Version + " shared by " + g.Share.SharedBy + " with " + member + " (" + g.Result.Status + ", joined room " + roomID + ")",
			})
		}
		s.notifyRoomShare(ctx, g, member)
	}
}

// notifyRoomShare sends the content_shared notification of a copy given on
// join (contentshare.Announce: one version notifies a member once).
func (s *Service) notifyRoomShare(ctx context.Context, g contentshare.Grant, member string) {
	if s.Notifications == nil || s.Notifications.Emitter == nil {
		return
	}
	e := s.Notifications.Emitter
	link := ""
	if base := strings.TrimRight(strings.TrimSpace(e.PublicURL), "/"); base != "" {
		link = base + "/#/library"
	}
	for _, ev := range contentshare.Announce(contentshare.Share{
		Source: g.Source, Version: g.Share.Version, SharedBy: g.Share.SharedBy, Note: g.Share.Note, Link: link, ViaRoom: true,
	}, []string{member}) {
		if _, err := e.Emit(ctx, ev); err != nil {
			slog.Warn("bnbloc realtime: content notification enqueue failed", "event", "bnbloc.notify.enqueue_failed",
				"type", wire.NotificationContentShared, "error", err)
		}
	}
}

// joinedMember reads a member_joined payload: typed in-process, a decoded
// JSON map through the internal ingest.
func joinedMember(ev wire.Event) string {
	p, _ := wire.DecodePayload[wire.MemberJoinedPayload](ev.Payload)
	return strings.TrimSpace(p.UserID)
}
