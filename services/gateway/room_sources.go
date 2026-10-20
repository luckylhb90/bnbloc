package gateway

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"time"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/contentuse"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/notify"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/rbac"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Room sources (docs/prd/2026-10-20-rooms-as-components.md §2.3, slice 2):
// the one owner of the consent that lets a room's scheduled tasks read
// records of the whole organization. A room package asks for a source
// with a purpose; installing it records the ask for the new room
// (askRoomSources); the organization's admin grants it with a scope or
// withdraws it; sourceCover.covers is the one rule of which rooms' records
// a grant reaches; the schedule-run tool read_records
// (schedule_records.go) reads only through it, so a withdrawal holds at
// the next read. Members of a covered room see which room reviews them.
//
//	GET    /v1/tenant/room-sources                          the organization's asks (org admin)
//	PUT    /v1/tenant/room-sources/{roomId}/{source}        grant with a scope   wire.RoomSourceDecision
//	DELETE /v1/tenant/room-sources/{roomId}/{source}        withdraw
//	GET    /v1/rooms/{roomId}/sources                       a member: the room's asks, who reviews it, its opt-ins
//	PUT    /v1/rooms/{roomId}/sources/{source}/opt-in       the room's owner opts its records in or out
//	POST   /v1/rooms/{roomId}/sources/{source}/ask-admin    the room's owner or an admin asks the admins to decide
//
// Grants, withdrawals and opt-ins are audited (wire.AuditKindSource).

// sourceCover is a granted ask with what its scope needs to decide a room:
// the rooms opted in to its source.
type sourceCover struct {
	grant  store.RoomSourceGrant
	optIns map[string]bool
}

// covers is the one rule of whether a grant reaches room's records: never
// while waiting or withdrawn, never the reading room itself (its members
// read it already); scope all every other room, none-private every room
// that is not private (a room of unknown visibility counts as private),
// opted-in the rooms whose owner opted in.
func (c sourceCover) covers(room store.Room) bool {
	if room.RoomID == c.grant.RoomID {
		return false
	}
	switch c.grant.Scope {
	case wire.RoomSourceScopeAll:
		return true
	case wire.RoomSourceScopeNonePrivate:
		return room.Visibility == store.VisibilityShared
	case wire.RoomSourceScopeOptedIn:
		return c.optIns[room.RoomID]
	}
	return false
}

// sourceOptIns are the rooms of ctx's tenant opted in to source.
func (s *Service) sourceOptIns(ctx context.Context, source string) (map[string]bool, error) {
	list, err := s.RoomSources.RoomSourceOptIns(ctx, source)
	if err != nil {
		return nil, err
	}
	out := make(map[string]bool, len(list))
	for _, o := range list {
		out[o.RoomID] = true
	}
	return out, nil
}

// coverOf is g's cover, its opt-ins read when its scope needs them.
func (s *Service) coverOf(ctx context.Context, g store.RoomSourceGrant) (sourceCover, error) {
	c := sourceCover{grant: g}
	if g.Scope != wire.RoomSourceScopeOptedIn {
		return c, nil
	}
	var err error
	c.optIns, err = s.sourceOptIns(ctx, g.Source)
	return c, err
}

// sourceAlias is the opaque, stable name a reader who may not see names
// reads person as under grant g: keyed by the ask's own random key, so
// two grants' aliases of one person never match.
func sourceAlias(g store.RoomSourceGrant, person string) string {
	if person == "" {
		return ""
	}
	mac := hmac.New(sha256.New, []byte(g.AliasKey))
	mac.Write([]byte(person))
	return "member-" + hex.EncodeToString(mac.Sum(nil))[:10]
}

// askRoomSources records the sources a room package asks for as asks of
// the room just installed from it, waiting for the organization's admin
// (each with a fresh alias key). A deployment without the consent store
// keeps none: the reply says the source cannot be granted here.
func (s *Service) askRoomSources(ctx context.Context, claims auth.Claims, roomID string, c store.ContentComponent, version string, asks []wire.RoomSourceAsk, reply *wire.RoomInstallReply) error {
	for _, a := range asks {
		if s.RoomSources == nil {
			reply.Skipped = append(reply.Skipped, fmt.Sprintf("source %s (this deployment keeps no consent for it)", a.Name))
			continue
		}
		key := make([]byte, 16)
		if _, err := rand.Read(key); err != nil {
			return err
		}
		if err := s.RoomSources.AskRoomSource(tenant.NewContext(ctx, claims.Tenant), store.RoomSourceGrant{RoomID: roomID, Source: a.Name,
			ComponentID: c.ID, ComponentName: c.Name, ComponentVersion: version, Purpose: a.Purpose, AliasKey: hex.EncodeToString(key),
			AskedBy: claims.Subject, AskedAtMs: s.nowTime().UnixMilli()}); err != nil {
			return fmt.Errorf("record the ask for %s: %w", a.Name, err)
		}
		reply.Sources = append(reply.Sources, a)
	}
	return nil
}

// roomName is a room's name as the consent shows it (its id when it cannot
// be read).
func (s *Service) roomName(ctx context.Context, roomID string) string {
	room, err := s.RoomInfo(ctx, roomID)
	if err != nil {
		return roomID
	}
	return firstNonEmpty(room.Name, roomID)
}

// grantView is an ask as the console shows it: the room's name and the
// scheduled tasks of the package version it was installed from (none when
// that version is gone).
func (s *Service) grantView(ctx context.Context, g store.RoomSourceGrant) wire.RoomSourceGrant {
	out := wire.RoomSourceGrant{RoomID: g.RoomID, RoomName: s.roomName(ctx, g.RoomID), Source: g.Source, Purpose: g.Purpose,
		ComponentID: g.ComponentID, From: g.ComponentName + "@" + g.ComponentVersion, Schedules: []wire.RoomPackageSchedule{},
		AskedBy: g.AskedBy, AskedAtMs: g.AskedAtMs, Scope: g.Scope, DecidedBy: g.DecidedBy, DecidedAtMs: g.DecidedAtMs}
	if s.ContentComponents == nil {
		return out
	}
	if v, err := s.ContentComponents.GetContentComponentVersion(ctx, g.ComponentID, g.ComponentVersion); err == nil {
		if m, err := contentuse.Manifest(v); err == nil && m.Schedules != nil {
			out.Schedules = m.Schedules
		}
	}
	return out
}

// handleListRoomSources answers GET /v1/tenant/room-sources: every ask of
// the organization's rooms, for its admin.
func (s *Service) handleListRoomSources(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	ctx := tenant.NewContext(r.Context(), claims.Tenant)
	list, err := s.RoomSources.RoomSourceGrants(ctx)
	if err != nil {
		obs.ServerError(w, "list the room sources failed", err)
		httpx.Error(w, "list the room sources failed", http.StatusInternalServerError)
		return
	}
	out := wire.RoomSourceGrantsReply{Grants: make([]wire.RoomSourceGrant, 0, len(list))}
	for _, g := range list {
		out.Grants = append(out.Grants, s.grantView(ctx, g))
	}
	httpx.WriteJSON(w, http.StatusOK, out)
}

// handleDecideRoomSource answers PUT (grant, wire.RoomSourceDecision) and
// DELETE (withdraw) /v1/tenant/room-sources/{roomId}/{source} for the
// organization's admin. A withdrawal holds at the reading room's next
// read; both are audited.
func (s *Service) handleDecideRoomSource(withdraw bool) sessionHandler {
	return func(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
		scope := ""
		if !withdraw {
			var in wire.RoomSourceDecision
			if !httpx.DecodeJSON(w, r, 4<<10, &in) {
				return
			}
			if !slices.Contains(wire.RoomSourceScopes, in.Scope) {
				httpx.Error(w, fmt.Sprintf("scope is one of %v", wire.RoomSourceScopes), http.StatusBadRequest)
				return
			}
			scope = in.Scope
		}
		ctx := tenant.NewContext(r.Context(), claims.Tenant)
		roomID, source := r.PathValue("roomId"), r.PathValue("source")
		g, err := s.RoomSources.DecideRoomSource(ctx, roomID, source, scope, claims.Subject, s.nowTime().UnixMilli())
		if errors.Is(err, store.ErrRoomSourceNotFound) {
			httpx.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		if err != nil {
			obs.ServerError(w, "decide the room source failed", err)
			httpx.Error(w, "decide the room source failed", http.StatusInternalServerError)
			return
		}
		action, detail := "grant", fmt.Sprintf("%s for room %s (%s@%s), scope %s", source, roomID, g.ComponentName, g.ComponentVersion, scope)
		if withdraw {
			action, detail = "withdraw", fmt.Sprintf("%s for room %s (%s@%s)", source, roomID, g.ComponentName, g.ComponentVersion)
		}
		s.audit(ctx, claims.Tenant, store.ExtAuditRecord{Kind: wire.AuditKindSource, Name: roomID, Action: action, Actor: claims.Subject,
			Outcome: wire.AuditOutcomeAllow, Detail: detail})
		httpx.WriteJSON(w, http.StatusOK, s.grantView(ctx, g))
	}
}

// sourceParam is the {source} of a room's source route, one of
// wire.RoomSources (else a 404 is written).
func sourceParam(w http.ResponseWriter, r *http.Request) (string, bool) {
	source := r.PathValue("source")
	if !slices.Contains(wire.RoomSources, source) {
		httpx.Error(w, "no such source", http.StatusNotFound)
		return "", false
	}
	return source, true
}

// handleRoomSources answers GET /v1/rooms/{roomId}/sources for any member
// (a visitor included: transparency is everyone's).
func (s *Service) handleRoomSources(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	roomID := r.PathValue("roomId")
	rc, ok := s.roomClaims(claims, roomID)
	if _, member := s.roomMember(claims.Subject, roomID); !ok || !member {
		httpx.Error(w, "only the room's members read its sources", http.StatusForbidden)
		return
	}
	s.writeRoomSources(w, r, tenant.NewContext(r.Context(), rc.Tenant), claims.Subject, roomID)
}

// writeRoomSources answers what roomID asks to read and where each ask
// stands, the rooms whose grants cover its records (named; linked for a
// member of the reading room) and the sources its owner opted it in to.
func (s *Service) writeRoomSources(w http.ResponseWriter, r *http.Request, ctx context.Context, caller, roomID string) {
	info, err := s.roomSourcesInfo(ctx, caller, roomID)
	if err != nil {
		obs.ServerError(w, "read the room's sources failed", err)
		httpx.Error(w, "read the room's sources failed", http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, info)
}

func (s *Service) roomSourcesInfo(ctx context.Context, caller, roomID string) (wire.RoomSourcesInfo, error) {
	info := wire.RoomSourcesInfo{Asks: []wire.RoomSourceAskState{}, ReviewedBy: []wire.RoomReviewer{}, OptedIn: []string{}}
	optIns := map[string]map[string]bool{}
	for _, src := range wire.RoomSources {
		g, err := s.RoomSources.GetRoomSourceGrant(ctx, roomID, src)
		switch {
		case err == nil:
			info.Asks = append(info.Asks, wire.RoomSourceAskState{Source: src, Purpose: g.Purpose, Scope: g.Scope})
		case !errors.Is(err, store.ErrRoomSourceNotFound):
			return info, err
		}
		if optIns[src], err = s.sourceOptIns(ctx, src); err != nil {
			return info, err
		}
		if optIns[src][roomID] {
			info.OptedIn = append(info.OptedIn, src)
		}
	}
	room, err := s.RoomInfo(ctx, roomID)
	if err != nil {
		return info, err
	}
	grants, err := s.RoomSources.RoomSourceGrants(ctx)
	if err != nil {
		return info, err
	}
	for _, g := range grants {
		if !(sourceCover{grant: g, optIns: optIns[g.Source]}).covers(room) {
			continue
		}
		rv := wire.RoomReviewer{Source: g.Source, RoomName: s.roomName(ctx, g.RoomID)}
		if _, member := s.roomMember(caller, g.RoomID); member {
			rv.RoomID = g.RoomID
		}
		info.ReviewedBy = append(info.ReviewedBy, rv)
	}
	return info, nil
}

// handleRoomSourceOptIn answers PUT /v1/rooms/{roomId}/sources/{source}/opt-in:
// the room's owner opts its records in to (or out of) the organization's
// grants of scope opted-in, audited; the answer is the room's sources.
func (s *Service) handleRoomSourceOptIn(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	roomID := r.PathValue("roomId")
	source, ok := sourceParam(w, r)
	if !ok {
		return
	}
	var in wire.RoomSourceOptIn
	if !httpx.DecodeJSON(w, r, 4<<10, &in) {
		return
	}
	rc, ok := s.roomClaims(claims, roomID)
	if m, member := s.roomMember(claims.Subject, roomID); !ok || !member || !m.IsHuman() || m.Role != store.RoleOwner {
		httpx.Error(w, "only the room's owner opts its conversations in", http.StatusForbidden)
		return
	}
	ctx := tenant.NewContext(r.Context(), rc.Tenant)
	if err := s.RoomSources.SetRoomSourceOptIn(ctx, store.RoomSourceOptIn{RoomID: roomID, Source: source, OptedBy: claims.Subject,
		OptedAtMs: s.nowTime().UnixMilli()}, in.OptedIn); err != nil {
		obs.ServerError(w, "save the opt-in failed", err)
		httpx.Error(w, "save the opt-in failed", http.StatusInternalServerError)
		return
	}
	action := "opt-out"
	if in.OptedIn {
		action = "opt-in"
	}
	s.audit(ctx, rc.Tenant, store.ExtAuditRecord{Kind: wire.AuditKindSource, Name: roomID, Action: action, Actor: claims.Subject,
		Outcome: wire.AuditOutcomeAllow, Detail: source + " of room " + roomID + " for grants of scope " + wire.RoomSourceScopeOptedIn})
	s.writeRoomSources(w, r, ctx, claims.Subject, roomID)
}

// handleRoomSourceAskAdmin answers POST
// /v1/rooms/{roomId}/sources/{source}/ask-admin: the room's owner or an
// admin asks the organization's admins to decide a waiting ask, through
// the admins' one notification path (notifyOrgAdmins), at most once a day
// per room and source.
func (s *Service) handleRoomSourceAskAdmin(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	roomID := r.PathValue("roomId")
	source, ok := sourceParam(w, r)
	if !ok {
		return
	}
	rc, ok := s.roomEditorClaims(claims, roomID)
	if !ok {
		httpx.Error(w, "only the room's owner or admins ask for its sources", http.StatusForbidden)
		return
	}
	ctx := tenant.NewContext(r.Context(), rc.Tenant)
	g, err := s.RoomSources.GetRoomSourceGrant(ctx, roomID, source)
	if errors.Is(err, store.ErrRoomSourceNotFound) {
		httpx.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if err != nil {
		obs.ServerError(w, "read the room's ask failed", err)
		httpx.Error(w, "read the room's ask failed", http.StatusInternalServerError)
		return
	}
	if g.Scope != "" {
		httpx.Error(w, "the organization's admin already granted it", http.StatusConflict)
		return
	}
	link := ""
	if origin := s.publicOrigin(r); origin != "" {
		link = origin + "/#/org"
	}
	day := s.nowTime().UTC().Format(time.DateOnly)
	_, queued, err := s.notifyOrgAdmins(r.Context(), rc.Tenant, notify.Event{RoomID: roomID, Actor: claims.Subject,
		Title: claims.Subject + " asks you to let a room read the organization's " + sourceWords(source),
		Text:  "Purpose: " + g.Purpose + "\nFrom " + g.ComponentName + "@" + g.ComponentVersion + ". Decide in Organization → Room sources.",
		URL:   link, DedupKey: "room-source-ask:" + roomID + ":" + source + ":" + day})
	if err != nil {
		obs.ServerError(w, "ask the admins failed", err)
		httpx.Error(w, "ask the admins failed", http.StatusInternalServerError)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, wire.RoomReportReply{Queued: queued})
}

// sourceWords is what a source holds, in a few words.
func sourceWords(source string) string {
	if source == wire.RoomSourceRuns {
		return "agent conversations"
	}
	return source + " records"
}

// readerSeesNames reports whether a reading room's schedule owner may read
// people's names in its records: an organization admin only (the PRD's §3
// privacy line); everyone else reads aliases (sourceAlias).
func (s *Service) readerSeesNames(tenantID, owner string) bool {
	if s.TenantMemberRole == nil {
		return false
	}
	role, found := s.TenantMemberRole(tenantID, owner)
	return found && rbac.Can(role, rbac.PermManageOrg)
}
