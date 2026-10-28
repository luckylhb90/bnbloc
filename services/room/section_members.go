package room

import (
	"context"
	"errors"
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/audit"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A members-only section's membership (docs/prd/2026-10-28-channels-and-
// threads.md §2.1a, C11, C12, C17): who reads its channels. A member of the
// room joins at once ("open") or asks ("ask", a request its owners approve
// or decline); the section's owners and the room's owners and admins add
// and remove members. The room's owners and admins read the section only by
// joining it, and their joining is announced to the section's members and
// audited (C12). Every change publishes section_membership (room-wide):
// listings reload, and realtime re-scopes the member's streams at once.

// mayManageSectionMembers is the ONE rule of who adds, removes and approves
// a members-only section's members: its owners and the room's owners and
// admins (C12; mayManageSection).
func mayManageSectionMembers(role store.Role, user string, sec store.RoomSection) bool {
	return mayManageSection(role, user, sec)
}

// membersOnlySection reads the path's section for a membership route: a
// live members-only section the caller may know of (404 otherwise; an open
// section is joined by following its channels, 409).
func (s *Service) membersOnlySection(w http.ResponseWriter, r *http.Request, roomID string) (store.RoomSection, bool) {
	sec, ok := s.liveSection(w, r, roomID)
	if !ok {
		return store.RoomSection{}, false
	}
	if !sec.MembersOnly() {
		httpx.Error(w, "an open section is joined by following its channels", http.StatusConflict)
		return store.RoomSection{}, false
	}
	return sec, true
}

// sectionMemberCaller is a membership route's caller: a human member of the
// room who may post (a visitor reads open channels only and joins nothing,
// decision: pending owner).
func (s *Service) sectionMemberCaller(w http.ResponseWriter, r *http.Request, roomID string) (channelCaller, bool) {
	who, ok := s.humanCaller(w, r, roomID)
	if !ok {
		return channelCaller{}, false
	}
	if who.role == store.RoleVisitor {
		httpx.Error(w, "visitors are read-only: only a member who may post joins a section", http.StatusForbidden)
		return channelCaller{}, false
	}
	return who, true
}

// handleJoinSection: the caller joins an "open" section at once, asks to
// join an "ask" one (an "invite" one is not shown to them: 404), or, as the
// room's owner or an admin (or the section's owner), joins any (C12:
// announced and audited).
func (s *Service) handleJoinSection(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	who, ok := s.sectionMemberCaller(w, r, roomID)
	if !ok {
		return
	}
	sec, ok := s.membersOnlySection(w, r, roomID)
	if !ok {
		return
	}
	ctx := r.Context()
	if sec.JoinPolicy == wire.SectionJoinOpen || mayManageSectionMembers(who.role, who.user, sec) {
		if err := s.addSectionMember(ctx, roomID, sec, who.user, who); err != nil {
			obs.ServerError(w, "join section failed", err)
			httpx.Error(w, "join section failed", http.StatusInternalServerError)
			return
		}
		httpx.WriteJSON(w, http.StatusOK, wire.SectionJoinReply{Joined: true})
		return
	}
	if sec.JoinPolicy != wire.SectionJoinAsk {
		httpx.Error(w, "the section's owners add its members", http.StatusForbidden)
		return
	}
	if err := s.Channels.PutSectionJoinRequest(ctx, store.SectionJoinRequest{RoomID: roomID, SectionID: sec.SectionID, UserID: who.user, RequestedAtMs: s.Now()}); err != nil {
		obs.ServerError(w, "request to join section failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.publishSectionMembership(ctx, roomID, wire.SectionMembershipPayload{SectionID: sec.SectionID, UserID: who.user, Action: "requested", By: who.user})
	httpx.WriteJSON(w, http.StatusOK, wire.SectionJoinReply{Requested: true})
}

// handleLeaveSection: the caller leaves (their streams re-scope at once).
// An owner leaves only once another owner remains (decision: pending
// owner: a section nobody answers for is the room admins' to clean up).
func (s *Service) handleLeaveSection(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	who, ok := s.humanCaller(w, r, roomID)
	if !ok {
		return
	}
	sec, ok := s.membersOnlySection(w, r, roomID)
	if !ok {
		return
	}
	if sec.IsOwner(who.user) && len(sec.Owners) == 1 {
		httpx.Error(w, "you are the section's only owner: hand it to another owner first", http.StatusConflict)
		return
	}
	s.removeSectionMember(w, r, roomID, sec, who.user, who, "left")
}

// handleAddSectionMember adds a member of the room (or approves their
// request): the section's owners and the room's owners and admins.
func (s *Service) handleAddSectionMember(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	who, ok := s.humanCaller(w, r, roomID)
	if !ok {
		return
	}
	sec, ok := s.membersOnlySection(w, r, roomID)
	if !ok {
		return
	}
	if !mayManageSectionMembers(who.role, who.user, sec) {
		httpx.Error(w, "only the section's owners and the room's owner and admins add members", http.StatusForbidden)
		return
	}
	var body wire.SectionMemberRequest
	if !httpx.DecodeJSON(w, r, maxJSONBodyBytes, &body) {
		return
	}
	user := strings.TrimSpace(body.UserID)
	m, found, err := s.Store.GetMember(r.Context(), roomID, user)
	if err != nil {
		obs.ServerError(w, "read membership failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if !found || !m.Human() || m.Role == store.RoleVisitor {
		httpx.Error(w, "a section's member is a member of the room who may post", http.StatusBadRequest)
		return
	}
	if err := s.addSectionMember(r.Context(), roomID, sec, user, who); err != nil {
		obs.ServerError(w, "add section member failed", err)
		httpx.Error(w, "add section member failed", http.StatusInternalServerError)
		return
	}
	s.writeSection(w, r, roomID, sec)
}

// handleRemoveSectionMember removes a member (the section's owners and the
// room's owners and admins); an owner is removed by changing the owners
// first.
func (s *Service) handleRemoveSectionMember(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	who, ok := s.humanCaller(w, r, roomID)
	if !ok {
		return
	}
	sec, ok := s.membersOnlySection(w, r, roomID)
	if !ok {
		return
	}
	if !mayManageSectionMembers(who.role, who.user, sec) {
		httpx.Error(w, "only the section's owners and the room's owner and admins remove members", http.StatusForbidden)
		return
	}
	user := strings.TrimSpace(r.PathValue("userId"))
	if sec.IsOwner(user) {
		httpx.Error(w, "an owner of the section is removed by changing its owners first", http.StatusConflict)
		return
	}
	s.removeSectionMember(w, r, roomID, sec, user, who, "removed")
}

// handleSectionRequests lists the open join requests to those who approve
// them.
func (s *Service) handleSectionRequests(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	who, ok := s.humanCaller(w, r, roomID)
	if !ok {
		return
	}
	sec, ok := s.membersOnlySection(w, r, roomID)
	if !ok {
		return
	}
	if !mayManageSectionMembers(who.role, who.user, sec) {
		httpx.Error(w, "only the section's owners and the room's owner and admins see its requests", http.StatusForbidden)
		return
	}
	list, err := s.Channels.SectionJoinRequests(r.Context(), roomID, sec.SectionID)
	if err != nil {
		obs.ServerError(w, "list join requests failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	out := wire.SectionRequests{Requests: []wire.SectionJoinRequest{}}
	for _, q := range list {
		out.Requests = append(out.Requests, wire.SectionJoinRequest{UserID: q.UserID, RequestedAtMs: q.RequestedAtMs})
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// handleDeclineSectionRequest closes a request without joining.
func (s *Service) handleDeclineSectionRequest(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	who, ok := s.humanCaller(w, r, roomID)
	if !ok {
		return
	}
	sec, ok := s.membersOnlySection(w, r, roomID)
	if !ok {
		return
	}
	if !mayManageSectionMembers(who.role, who.user, sec) {
		httpx.Error(w, "only the section's owners and the room's owner and admins decline requests", http.StatusForbidden)
		return
	}
	user := strings.TrimSpace(r.PathValue("userId"))
	if err := s.Channels.TakeSectionJoinRequest(r.Context(), roomID, sec.SectionID, user); err != nil {
		if errors.Is(err, store.ErrNoJoinRequest) {
			httpx.Error(w, "no open request from this member", http.StatusNotFound)
			return
		}
		obs.ServerError(w, "decline join request failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.publishSectionMembership(r.Context(), roomID, wire.SectionMembershipPayload{SectionID: sec.SectionID, UserID: user, Action: "declined", By: who.user})
	w.WriteHeader(http.StatusNoContent)
}

// addSectionMember makes user a member of sec, announces it, and audits a
// join by one of the room's owners or admins (C12).
func (s *Service) addSectionMember(ctx context.Context, roomID string, sec store.RoomSection, user string, by channelCaller) error {
	if err := s.Channels.AddSectionMember(ctx, store.SectionMember{RoomID: roomID, SectionID: sec.SectionID, UserID: user, AddedBy: by.user, JoinedAtMs: s.Now()}); err != nil {
		return err
	}
	action := "added"
	if user == by.user {
		action = "joined"
	}
	admin := false
	if m, ok, err := s.Store.GetMember(ctx, roomID, user); err == nil && ok {
		admin = m.Role.CanInvite()
	}
	s.publishSectionMembership(ctx, roomID, wire.SectionMembershipPayload{SectionID: sec.SectionID, UserID: user, Action: action, By: by.user, Admin: admin})
	s.auditSectionMembership(ctx, roomID, sec, user, action, by.user)
	return nil
}

// removeSectionMember ends user's membership and answers the route.
func (s *Service) removeSectionMember(w http.ResponseWriter, r *http.Request, roomID string, sec store.RoomSection, user string, by channelCaller, action string) {
	if err := s.Channels.RemoveSectionMember(r.Context(), roomID, sec.SectionID, user); err != nil {
		if errors.Is(err, store.ErrNotSectionMember) {
			httpx.Error(w, "not a member of the section", http.StatusNotFound)
			return
		}
		obs.ServerError(w, "remove section member failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.publishSectionMembership(r.Context(), roomID, wire.SectionMembershipPayload{SectionID: sec.SectionID, UserID: user, Action: action, By: by.user})
	s.auditSectionMembership(r.Context(), roomID, sec, user, action, by.user)
	w.WriteHeader(http.StatusNoContent)
}

// publishSectionMembership announces a membership change (room-wide).
func (s *Service) publishSectionMembership(ctx context.Context, roomID string, p wire.SectionMembershipPayload) {
	if s.Publisher == nil {
		return
	}
	s.Publisher.Publish(ctx, wire.Event{RoomID: roomID, Kind: wire.EventSectionMembership, Payload: p, AtUnixMs: s.Now()})
}

// auditSectionMembership records who now reads (or no longer reads) a
// members-only section, and who let them in (C12: an admin's join is
// audited). Best effort, like every audit.
func (s *Service) auditSectionMembership(ctx context.Context, roomID string, sec store.RoomSection, user, action, by string) {
	audit.Record(context.WithoutCancel(ctx), s.Audit, store.ExtAuditRecord{
		Kind: wire.AuditKindSection, Name: roomID, Action: action, Actor: by, TenantID: sec.TenantID,
		Detail: "section=" + sec.SectionID + " member=" + user, AtMs: s.Now(),
	})
}
