package room

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// App notices (docs/prd/2026-10-15-app-identity-schedules-notify-ai.md §5):
// who a notice an app sends reaches. The room service owns rooms and their
// members, so it resolves a notice's to; the gateway sends it through the
// notification emitter (one producer seam), with the app's title and the
// room's name this answers.
//
//	POST /internal/v1/app/notify-targets          an app's notice (its scope's caller)  → wire.AppNotifyTargets
//	POST /internal/v1/rooms/{roomId}/notice-targets?as=  a room's notice (as: an ai schedule's owner) → wire.AppNotifyTargets
//
// "room" is the room's people who may act in it (the app's human owners:
// not visitors, not agent hosts): any signed-in caller may reach them (a
// sign-up telling its organizers). A list of logins is the owners' only,
// and keeps those who belong to the room's organization (anyone else is
// dropped silently, so a page cannot probe who exists). An anonymous
// visitor notifies no one.

// The refusals of a notice's to.
var (
	errAppNotifyTo     = errors.New(`to is "room", a list of logins or "group:<name>"`)
	errAppNotifyLogins = errors.New("only the app's owners (the room's members) may notify people by login or a group")
	// errAppNotifyGroup marks a group to (noticeGroup resolves it).
	errAppNotifyGroup = errors.New("a group notice")
)

func (s *Service) handleAppNotifyTargets(w http.ResponseWriter, r *http.Request, a *appCall) {
	if a.kind != svcauth.AppMember || a.subject == "" {
		httpx.WriteJSON(w, http.StatusForbidden, httpx.ErrorBody{Error: "notices are for people who signed in", Code: wire.AppSignInRequired})
		return
	}
	var in wire.AppNotifyRequest
	if !httpx.DecodeJSON(w, r, wire.MaxAppRequestBytes, &in) {
		return
	}
	out, err := s.noticeTargets(a.ctx, a.tenantID, a.scope.RoomID, a.owner, in.To)
	if err != nil {
		writeAppError(w, err)
		return
	}
	if art, err := s.Artifacts.GetArtifact(a.ctx, a.scope.AppID); err == nil {
		out.AppTitle = art.Title
	}
	if room, err := s.Store.GetRoom(a.ctx, a.scope.RoomID); err == nil {
		out.RoomName = room.Name
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// handleRoomNoticeTargets resolves a room notice's to for its sender (as,
// named by the gateway, the only caller): an ai schedule's owner, who must
// still be a member who may act in the room (else 403: the schedule
// pauses).
func (s *Service) handleRoomNoticeTargets(w http.ResponseWriter, r *http.Request) {
	ctx, roomID, sender := r.Context(), r.PathValue("roomId"), strings.TrimSpace(r.URL.Query().Get("as"))
	tid, err := tenant.Require(ctx)
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}
	m, ok, err := s.Store.GetMember(ctx, roomID, sender)
	if err != nil {
		obs.ServerError(w, "read membership failed", err)
		httpx.Error(w, "read membership failed", http.StatusInternalServerError)
		return
	}
	if !ok || !(membership.Member{Role: m.Role, AgentHost: m.AgentHost}).MayChat() {
		httpx.Error(w, "the sender may not act in this room", http.StatusForbidden)
		return
	}
	var in wire.AppNotifyRequest
	if !httpx.DecodeJSON(w, r, wire.MaxAppRequestBytes, &in) {
		return
	}
	out, err := s.noticeTargets(ctx, tid, roomID, true, in.To)
	if err != nil {
		writeAppError(w, err)
		return
	}
	if room, err := s.Store.GetRoom(ctx, roomID); err == nil {
		out.RoomName = room.Name
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// noticeGroup is a "group:<name>" to's group: the owner's only, a name of
// at most wire.MaxNotifyGroupLen printable characters; "" for any other to.
func noticeGroup(to json.RawMessage, owner bool) (string, error) {
	var s string
	if json.Unmarshal(to, &s) != nil || !strings.HasPrefix(s, wire.AppNotifyGroupPrefix) {
		return "", nil
	}
	if !owner {
		return "", errAppNotifyLogins
	}
	name := strings.TrimSpace(strings.TrimPrefix(s, wire.AppNotifyGroupPrefix))
	if name == "" || utf8.RuneCountInString(name) > wire.MaxNotifyGroupLen || strings.IndexFunc(name, unicode.IsControl) >= 0 {
		return "", appdataInvalid(fmt.Sprintf("a group name is 1 to %d characters", wire.MaxNotifyGroupLen))
	}
	return name, nil
}

// noticeTargets resolves a notice's to: a group, or the members it reaches.
func (s *Service) noticeTargets(ctx context.Context, tenantID, roomID string, owner bool, to json.RawMessage) (wire.AppNotifyTargets, error) {
	group, err := noticeGroup(to, owner)
	if err != nil || group != "" {
		return wire.AppNotifyTargets{Recipients: []string{}, Group: group}, err
	}
	recipients, err := s.noticeRecipients(ctx, tenantID, roomID, owner, to)
	return wire.AppNotifyTargets{Recipients: recipients}, err
}

// noticeRecipients is the one resolution of a notice's to in a room: the
// room's members who may act in it, or (owner only) the logins that belong
// to the organization.
func (s *Service) noticeRecipients(ctx context.Context, tenantID, roomID string, owner bool, to json.RawMessage) ([]string, error) {
	var room string
	if json.Unmarshal(to, &room) == nil {
		if strings.HasPrefix(room, wire.AppNotifyGroupPrefix) {
			return nil, errAppNotifyGroup
		}
		if room != wire.AppNotifyRoom {
			return nil, appdataInvalid(errAppNotifyTo.Error())
		}
		members, err := s.Store.Members(ctx, roomID)
		if err != nil {
			return nil, err
		}
		out := []string{}
		for _, m := range members {
			if (membership.Member{Role: m.Role, AgentHost: m.AgentHost}).MayChat() {
				out = append(out, m.UserID)
			}
		}
		if len(out) > wire.MaxAppNotifyRecipients {
			return nil, appdataInvalid(fmt.Sprintf("the room has more than %d members to notify", wire.MaxAppNotifyRecipients))
		}
		return out, nil
	}
	var logins []string
	if err := json.Unmarshal(to, &logins); err != nil || len(logins) == 0 {
		return nil, appdataInvalid(errAppNotifyTo.Error())
	}
	if !owner {
		return nil, errAppNotifyLogins
	}
	if len(logins) > wire.MaxAppNotifyLogins {
		return nil, appdataInvalid(fmt.Sprintf("a notice names at most %d logins", wire.MaxAppNotifyLogins))
	}
	if s.Tenants == nil {
		return nil, errors.New("no organization directory")
	}
	seen := map[string]bool{}
	out := []string{}
	for _, l := range logins {
		l = strings.TrimSpace(l)
		if l == "" || seen[l] {
			continue
		}
		seen[l] = true
		_, ok, err := s.Tenants.GetMember(ctx, tenantID, l)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, l)
		}
	}
	return out, nil
}
