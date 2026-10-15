package room

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/membership"
	"github.com/luckylhb90/bnbloc/internal/svcauth"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// App notices (docs/prd/2026-10-15-app-identity-schedules-notify-ai.md §5):
// who a notice an app sends reaches. The room service owns rooms and their
// members, so it resolves a notice's to; the gateway sends it through the
// notification emitter (one producer seam), with the app's title and the
// room's name this answers.
//
//	POST /internal/v1/app/notify-targets   wire.AppNotifyRequest's to → wire.AppNotifyTargets
//
// "room" is the room's people who may act in it (the app's human owners:
// not visitors, not agent hosts): any signed-in caller may reach them (a
// sign-up telling its organizers). A list of logins is the owners' only,
// and keeps those who belong to the room's organization (anyone else is
// dropped silently, so a page cannot probe who exists). An anonymous
// visitor notifies no one.

// The refusals of a notice's to.
var (
	errAppNotifyTo     = errors.New(`to is "room" or a list of logins`)
	errAppNotifyLogins = errors.New("only the app's owners (the room's members) may notify people by login")
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
	recipients, err := s.appNoticeRecipients(a, in.To)
	if err != nil {
		writeAppError(w, err)
		return
	}
	out := wire.AppNotifyTargets{Recipients: recipients}
	if art, err := s.Artifacts.GetArtifact(a.ctx, a.scope.AppID); err == nil {
		out.AppTitle = art.Title
	}
	if room, err := s.Store.GetRoom(a.ctx, a.scope.RoomID); err == nil {
		out.RoomName = room.Name
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// appNoticeRecipients resolves to.
func (s *Service) appNoticeRecipients(a *appCall, to json.RawMessage) ([]string, error) {
	var room string
	if json.Unmarshal(to, &room) == nil {
		if room != wire.AppNotifyRoom {
			return nil, appdataInvalid(errAppNotifyTo.Error())
		}
		members, err := s.Store.Members(a.ctx, a.scope.RoomID)
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
	if !a.owner {
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
		_, ok, err := s.Tenants.GetMember(a.ctx, a.tenantID, l)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, l)
		}
	}
	return out, nil
}
