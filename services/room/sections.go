package room

import (
	"errors"
	"net/http"
	"slices"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// A room's sections (docs/prd/2026-10-28-channels-and-threads.md §2.1a):
// named groups of channels with their own owners and rules. Slice 1 makes
// open sections only (every room member sees their channels); members-only
// sections come with their membership (slice 2).
//
//	POST   /v1/rooms/{roomId}/sections               wire.SectionCreateRequest → wire.RoomSection
//	PATCH  /v1/rooms/{roomId}/sections/{sectionId}   wire.SectionPatchRequest → wire.RoomSection
//	DELETE /v1/rooms/{roomId}/sections/{sectionId}   its channels move to the top level
//
// The sections are listed with the channels (GET .../channels).

// mayCreateSection is the ONE rule of who makes a section: the room's owner
// and admins, and any member who may chat while the room's channel creation
// policy is not "admins" (a section is where members create channels, C15;
// §2.1a "people form a section for their team"). The creator owns it.
// decision: pending owner (the PRD names who makes channels, C15, not who
// makes sections; this reads C15's policy for both).
func mayCreateSection(room store.Room, role store.Role) bool {
	if role.CanInvite() {
		return true
	}
	return role != store.RoleVisitor && room.ChannelCreationPolicy() != wire.ChannelCreationAdmins
}

// mayManageSection is the ONE rule of who changes or deletes a section: its
// owners and the room's owner and admins. decision: channels PRD C12 (the
// room's owners and admins manage every section's owners) and §2.1a.
func mayManageSection(role store.Role, user string, sec store.RoomSection) bool {
	return role.CanInvite() || (role != store.RoleVisitor && sec.IsOwner(user))
}

// sectionOwners validates a section's owners: members of the room who may
// post, each once (subjects compare exactly).
func (s *Service) sectionOwners(r *http.Request, roomID string, owners []string) ([]string, int, error) {
	out := []string{}
	for _, o := range owners {
		o = strings.TrimSpace(o)
		if o == "" || slices.Contains(out, o) {
			continue
		}
		m, ok, err := s.Store.GetMember(r.Context(), roomID, o)
		if err != nil {
			return nil, http.StatusInternalServerError, err
		}
		if !ok || !m.Human() || m.Role == store.RoleVisitor {
			return nil, http.StatusBadRequest, errBadRequest("a section's owner is a member of the room who may post: " + o)
		}
		out = append(out, o)
	}
	return out, 0, nil
}

func (s *Service) handleCreateSection(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	who, ok := s.humanCaller(w, r, roomID)
	if !ok {
		return
	}
	var body wire.SectionCreateRequest
	if !httpx.DecodeJSON(w, r, maxJSONBodyBytes, &body) {
		return
	}
	name := strings.TrimSpace(body.Name)
	if name == "" {
		httpx.Error(w, "name is required", http.StatusBadRequest)
		return
	}
	// decision: channels PRD §8 slice 1 (open sections only; members-only
	// ones need their membership and MayReadChannel, slice 2).
	if v := strings.TrimSpace(body.Visibility); v != "" && v != wire.SectionOpen {
		httpx.Error(w, "only open sections can be made yet", http.StatusBadRequest)
		return
	}
	rules, err := channelText(&body.Rules, wire.MaxChannelRules, "rules")
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	room, err := s.Store.GetRoom(r.Context(), roomID)
	if err != nil {
		httpx.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if !mayCreateSection(room, who.role) {
		httpx.Error(w, "the room's channel creation policy keeps sections to its owner and admins", http.StatusForbidden)
		return
	}
	sec := store.RoomSection{
		SectionID: ids.New("sec_"), TenantID: tenant.FromContext(r.Context()), RoomID: roomID, Name: name,
		Position: body.Position, Visibility: wire.SectionOpen, Rules: rules, Owners: []string{who.user},
		// decision: channels PRD C17 (joining an open section is only
		// following).
		JoinPolicy: "open", CreatedBy: who.user, CreatedAtMs: s.Now(),
	}
	if err := s.Channels.PutSection(r.Context(), sec); err != nil {
		obs.ServerError(w, "create section failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.publishChannelUpdated(r.Context(), roomID, "", sec.SectionID)
	httpx.WriteJSON(w, http.StatusOK, sectionView(sec))
}

// liveSection reads the path's section, answering 404 itself.
func (s *Service) liveSection(w http.ResponseWriter, r *http.Request, roomID string) (store.RoomSection, bool) {
	sec, ok, err := s.Channels.GetSection(r.Context(), roomID, r.PathValue("sectionId"))
	if err != nil {
		obs.ServerError(w, "read section failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return store.RoomSection{}, false
	}
	if !ok {
		httpx.Error(w, "section not found", http.StatusNotFound)
		return store.RoomSection{}, false
	}
	return sec, true
}

func (s *Service) handlePatchSection(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	who, ok := s.humanCaller(w, r, roomID)
	if !ok {
		return
	}
	sec, ok := s.liveSection(w, r, roomID)
	if !ok {
		return
	}
	if !mayManageSection(who.role, who.user, sec) {
		httpx.Error(w, "only the section's owners and the room's owner and admins change a section", http.StatusForbidden)
		return
	}
	var body wire.SectionPatchRequest
	if !httpx.DecodeJSON(w, r, maxJSONBodyBytes, &body) {
		return
	}
	patch := store.SectionPatch{Position: body.Position}
	if body.Name != nil {
		name := strings.TrimSpace(*body.Name)
		if name == "" {
			httpx.Error(w, "name is required", http.StatusBadRequest)
			return
		}
		patch.Name = &name
	}
	if body.Rules != nil {
		rules, err := channelText(body.Rules, wire.MaxChannelRules, "rules")
		if err != nil {
			httpx.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		patch.Rules = &rules
	}
	if body.Owners != nil {
		owners, status, err := s.sectionOwners(r, roomID, *body.Owners)
		if err != nil {
			httpx.WriteStatus(w, status, err.Error(), err)
			return
		}
		patch.Owners = &owners
	}
	if err := s.Channels.UpdateSection(r.Context(), roomID, sec.SectionID, patch); err != nil {
		if errors.Is(err, store.ErrSectionNotFound) {
			httpx.Error(w, "section not found", http.StatusNotFound)
			return
		}
		obs.ServerError(w, "update section failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.publishChannelUpdated(r.Context(), roomID, "", sec.SectionID)
	sec, ok = s.liveSection(w, r, roomID)
	if !ok {
		return
	}
	httpx.WriteJSON(w, http.StatusOK, sectionView(sec))
}

// handleDeleteSection deletes a section; its channels move to the room's
// top level (nothing is deleted with it).
func (s *Service) handleDeleteSection(w http.ResponseWriter, r *http.Request) {
	roomID := r.PathValue("roomId")
	who, ok := s.humanCaller(w, r, roomID)
	if !ok {
		return
	}
	sec, ok := s.liveSection(w, r, roomID)
	if !ok {
		return
	}
	if !mayManageSection(who.role, who.user, sec) {
		httpx.Error(w, "only the section's owners and the room's owner and admins delete a section", http.StatusForbidden)
		return
	}
	if err := s.Channels.DeleteSection(r.Context(), roomID, sec.SectionID, s.Now()); err != nil {
		obs.ServerError(w, "delete section failed", err)
		httpx.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	s.publishChannelUpdated(r.Context(), roomID, "", sec.SectionID)
	w.WriteHeader(http.StatusOK)
}
