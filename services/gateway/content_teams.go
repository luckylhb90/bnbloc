package gateway

import (
	"context"
	"errors"
	"net/http"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/contentuse"
	"github.com/luckylhb90/bnbloc/internal/httpx"
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

// putRoomExpert makes one of a room's pinned experts the room's expert
// (setRoomExpert) and answers the refusal.
func (s *Service) putRoomExpert(w http.ResponseWriter, r *http.Request, claims auth.Claims, roomID string, c store.ContentComponent) {
	var refused errRoomExpert
	switch err := s.setRoomExpert(r.Context(), claims, roomID, c); {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.As(err, &refused):
		httpx.Error(w, refused.Error(), http.StatusBadRequest)
	default:
		contentStoreErr(w, "set the room expert failed", err)
	}
}

// errRoomExpert is why a component cannot be a room's expert.
type errRoomExpert string

func (e errRoomExpert) Error() string { return string(e) }

const (
	errRoomExpertKind   errRoomExpert = "the room expert must be an expert"
	errRoomExpertPinned errRoomExpert = "pin the expert in the room first"
	errRoomExpertTeam   errRoomExpert = "a team cannot be the room expert; pick it per message"
)

// setRoomExpert makes c, one of a room's pinned experts, the room's
// expert, replacing the previous one: every agent turn in the room wears
// it under the chat's own expert. A team cannot be the room's expert
// (every turn would lead a team). claims are the room editor's
// (contentUseScope). The one writer of a room's expert (the Library's
// use route and a room package's install).
func (s *Service) setRoomExpert(ctx context.Context, claims auth.Claims, roomID string, c store.ContentComponent) error {
	return s.setScopedExpert(ctx, claims, roomID, store.ContentUseRoomExpert, roomID, c)
}

// setScopedExpert is the one writer of a room's or a channel's expert (the
// use scope and its id): c must be one of the room's pinned experts and no
// team, and it replaces the scope's previous one.
func (s *Service) setScopedExpert(ctx context.Context, claims auth.Claims, roomID string, scope store.ContentUseScope, scopeID string, c store.ContentComponent) error {
	if c.Kind != contentpkg.KindExpert {
		return errRoomExpertKind
	}
	pins, err := s.ContentComponents.ListContentUses(ctx, store.ContentUseRoom, roomID)
	if err != nil {
		return err
	}
	pinned, ok := "", false
	for _, u := range pins {
		if u.ComponentID == c.ID {
			pinned, ok = u.PinnedVersion, true
		}
	}
	if !ok {
		return errRoomExpertPinned
	}
	v, err := s.ContentComponents.GetContentComponentVersion(ctx, c.ID, firstNonEmpty(pinned, c.LatestVersion))
	if err != nil {
		return err
	}
	m, err := contentuse.Manifest(v)
	if err != nil {
		return err
	}
	if m.Team != nil {
		return errRoomExpertTeam
	}
	current, err := s.ContentComponents.ListContentUses(ctx, scope, scopeID)
	if err != nil {
		return err
	}
	for _, u := range current {
		if u.ComponentID == c.ID {
			continue
		}
		if err := s.ContentComponents.DeleteContentUse(ctx, scope, scopeID, u.ComponentID); err != nil {
			return err
		}
	}
	u := store.ContentUse{TenantID: claims.Tenant, ScopeKind: scope, ScopeID: scopeID, ComponentID: c.ID, Kind: c.Kind,
		CreatedBy: claims.Subject, CreatedAtMs: s.nowTime().UnixMilli()}
	if err := s.ContentComponents.PutContentUse(ctx, u); err != nil {
		return err
	}
	s.contentUsesChanged(ctx, claims, u.ScopeKind, roomID, c.ID)
	return nil
}

// putChannelExpert makes one of a room's pinned experts a channel's expert
// (channels PRD C9) and answers the refusal: turns in that channel and its
// threads wear it instead of the room's. The same rules as the room's
// (setRoomExpert): an expert, pinned in the room, never a team.
func (s *Service) putChannelExpert(w http.ResponseWriter, r *http.Request, claims auth.Claims, roomID, channelID string, c store.ContentComponent) {
	var refused errRoomExpert
	switch err := s.setScopedExpert(r.Context(), claims, roomID, store.ContentUseChannelExpert, store.ChannelExpertScopeID(roomID, channelID), c); {
	case err == nil:
		w.WriteHeader(http.StatusNoContent)
	case errors.As(err, &refused):
		httpx.Error(w, refused.Error(), http.StatusBadRequest)
	default:
		contentStoreErr(w, "set the channel expert failed", err)
	}
}

// unpinRoomExpert clears the room's expert when its pin is removed (a room
// expert is always one of the room's pinned experts).
func (s *Service) unpinRoomExpert(ctx context.Context, roomID, componentID string) error {
	err := s.ContentComponents.DeleteContentUse(ctx, store.ContentUseRoomExpert, roomID, componentID)
	if err != nil && !errors.Is(err, store.ErrContentNotFound) {
		return err
	}
	// A channel's expert is one of the room's pinned experts too.
	experts, err := s.ContentComponents.ChannelExperts(ctx, roomID)
	if err != nil {
		return err
	}
	for _, u := range experts {
		if u.ComponentID != componentID {
			continue
		}
		if err := s.ContentComponents.DeleteContentUse(ctx, store.ContentUseChannelExpert, u.ScopeID, componentID); err != nil && !errors.Is(err, store.ErrContentNotFound) {
			return err
		}
	}
	return nil
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

// effectiveContent is what a job's turn wears (contentuse.Effective), with
// a thread's turns taking its parent channel's expert (channels PRD C9: a
// thread names no expert of its own). Every gateway resolution of a job's
// content goes through it.
func (s *Service) effectiveContent(ctx context.Context, job store.AgentJob) (wire.ContentEffective, error) {
	if s.ChannelInfo != nil && job.ChannelID != "" {
		ch, ok, err := s.ChannelInfo(tenant.NewContext(ctx, job.TenantID), job.RoomID, job.ChannelID)
		if err != nil {
			return wire.ContentEffective{}, err
		}
		if ok && ch.IsThread() && ch.ParentID != "" {
			job.ChannelID = ch.ParentID
		}
	}
	return contentuse.Effective(ctx, s.ContentComponents, job)
}
