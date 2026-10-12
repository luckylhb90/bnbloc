package gateway

import (
	"context"
	"errors"
	"net/http"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/contentuse"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/realtime"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Content usage counts, the room expert and live refresh of a room's uses
// (docs/prd/2026-10-12-experts-and-teams.md, step 6).

// applyContentUsage fills each view's usage counts: an expert's runs and
// distinct requesters (the caller's organization's jobs that wore it), a
// pack's members who turned it on for themselves. Counts are capped
// (store.MaxUsageCount).
func (s *Service) applyContentUsage(ctx context.Context, claims auth.Claims, views []wire.ContentComponent) error {
	var experts, packs []string
	for _, v := range views {
		if v.Kind == "expert" {
			experts = append(experts, v.ID)
		} else {
			packs = append(packs, v.ID)
		}
	}
	runs := map[string]store.ContentUsage{}
	if len(experts) > 0 && s.ContentJobs != nil {
		var err error
		if runs, err = s.ContentJobs.ExpertUsage(tenant.NewContext(ctx, claims.Tenant), experts); err != nil {
			return err
		}
	}
	members := map[string]int{}
	if len(packs) > 0 {
		var err error
		if members, err = s.ContentComponents.CountMemberUses(ctx, claims.Tenant, packs); err != nil {
			return err
		}
	}
	for i := range views {
		if u, ok := runs[views[i].ID]; ok {
			views[i].Runs, views[i].Members = u.Runs, u.Members
		} else if n, ok := members[views[i].ID]; ok {
			views[i].Members = n
		}
	}
	return nil
}

// roomExpertOf returns the id of a room's expert ("" for none).
func (s *Service) roomExpertOf(ctx context.Context, roomID string) (string, error) {
	uses, err := s.ContentComponents.ListContentUses(ctx, store.ContentUseRoomExpert, roomID)
	if err != nil || len(uses) == 0 {
		return "", err
	}
	return uses[0].ComponentID, nil
}

// putRoomExpert makes one of a room's pinned experts the room's expert,
// replacing the previous one: every agent turn in the room wears it under
// the chat's own expert. A team cannot be the room's expert (every turn
// would lead a team).
func (s *Service) putRoomExpert(w http.ResponseWriter, r *http.Request, claims auth.Claims, roomID string, c store.ContentComponent) {
	ctx := r.Context()
	if c.Kind != contentpkg.KindExpert {
		httpx.Error(w, "the room expert must be an expert", http.StatusBadRequest)
		return
	}
	pins, err := s.ContentComponents.ListContentUses(ctx, store.ContentUseRoom, roomID)
	if err != nil {
		obs.ServerError(w, "list room content uses failed", err)
		httpx.Error(w, "list room content uses failed", http.StatusInternalServerError)
		return
	}
	pinned, ok := "", false
	for _, u := range pins {
		if u.ComponentID == c.ID {
			pinned, ok = u.PinnedVersion, true
		}
	}
	if !ok {
		httpx.Error(w, "pin the expert in the room first", http.StatusBadRequest)
		return
	}
	v, err := s.ContentComponents.GetContentComponentVersion(ctx, c.ID, firstNonEmpty(pinned, c.LatestVersion))
	if err != nil {
		contentStoreErr(w, "load content version failed", err)
		return
	}
	m, err := contentuse.Manifest(v)
	if err != nil {
		obs.ServerError(w, "decode content manifest failed", err)
		httpx.Error(w, "decode content manifest failed", http.StatusInternalServerError)
		return
	}
	if m.Team != nil {
		httpx.Error(w, "a team cannot be the room expert; pick it per message", http.StatusBadRequest)
		return
	}
	current, err := s.ContentComponents.ListContentUses(ctx, store.ContentUseRoomExpert, roomID)
	if err != nil {
		obs.ServerError(w, "list room expert failed", err)
		httpx.Error(w, "list room expert failed", http.StatusInternalServerError)
		return
	}
	for _, u := range current {
		if u.ComponentID == c.ID {
			continue
		}
		if err := s.ContentComponents.DeleteContentUse(ctx, store.ContentUseRoomExpert, roomID, u.ComponentID); err != nil {
			obs.ServerError(w, "replace room expert failed", err)
			httpx.Error(w, "replace room expert failed", http.StatusInternalServerError)
			return
		}
	}
	u := store.ContentUse{TenantID: claims.Tenant, ScopeKind: store.ContentUseRoomExpert, ScopeID: roomID, ComponentID: c.ID, Kind: c.Kind,
		CreatedBy: claims.Subject, CreatedAtMs: s.nowTime().UnixMilli()}
	if err := s.ContentComponents.PutContentUse(ctx, u); err != nil {
		contentStoreErr(w, "save content use failed", err)
		return
	}
	s.contentUsesChanged(ctx, claims, u.ScopeKind, roomID, c.ID)
	w.WriteHeader(http.StatusNoContent)
}

// unpinRoomExpert clears the room's expert when its pin is removed (a room
// expert is always one of the room's pinned experts).
func (s *Service) unpinRoomExpert(ctx context.Context, roomID, componentID string) error {
	err := s.ContentComponents.DeleteContentUse(ctx, store.ContentUseRoomExpert, roomID, componentID)
	if errors.Is(err, store.ErrContentNotFound) {
		return nil
	}
	return err
}

// contentSaved tells the job's room that the room tools saved (or asked to
// publish) a component for the job's requester: a transient content_saved
// event the workshop toasts from. Only requests on behalf of a job have a
// room; a member's own save in the Library needs no event.
func (s *Service) contentSaved(ctx context.Context, actor contentActor, c store.ContentComponent, version, action string, created bool) {
	if actor.Job == nil || actor.Job.RoomID == "" || s.Backends.Realtime == "" {
		return
	}
	tid := firstNonEmpty(actor.Job.TenantID, actor.Claims.Tenant)
	pub := realtime.NewHTTPPublisher(s.Backends.Realtime, "gateway")
	pub.Publish(tenant.NewContext(ctx, tid), wire.Event{
		RoomID: actor.Job.RoomID, Kind: wire.EventContentSaved,
		Payload: wire.ContentSaved{ComponentID: c.ID, Kind: shortKind(c.Kind), Name: c.Name, DisplayName: c.DisplayName,
			Version: version, Action: action, Created: created, Owner: actor.Claims.Subject},
		AtUnixMs: s.nowTime().UnixMilli(),
	})
}

// contentUsesChanged tells a room's clients that its content uses changed
// (a transient content_uses_changed event): the composer and the room
// settings refetch the room's uses without reopening the room. A member
// scope change concerns no room. claims act in the room's tenant
// (contentUseScope). Best-effort, like every cross-process publish: the
// write already succeeded.
func (s *Service) contentUsesChanged(ctx context.Context, claims auth.Claims, scope store.ContentUseScope, roomID, componentID string) {
	if scope == store.ContentUseMember || roomID == "" || s.Backends.Realtime == "" {
		return
	}
	pub := realtime.NewHTTPPublisher(s.Backends.Realtime, "gateway")
	pub.Publish(tenant.NewContext(ctx, claims.Tenant), wire.Event{
		RoomID: roomID, Kind: wire.EventContentUsesChanged,
		Payload:  wire.ContentUsesChanged{Scope: string(scope), ComponentID: componentID},
		AtUnixMs: s.nowTime().UnixMilli(),
	})
}
