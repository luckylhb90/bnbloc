package gateway

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/notify"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/rbac"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Room sources (docs/prd/2026-10-20-rooms-as-components.md §2.3, §7): the
// one owner of the consent that lets a room's scheduled tasks read records
// of the whole organization. A room package asks for a source with a
// purpose; installing it records the ask for the new room
// (askRoomSources); the organization's admin grants it to that room, read
// by its owner (the grantee), with a scope, or withdraws it;
// sourceCover.covers is the one rule of which rooms' records a grant
// reaches; the schedule-run tool read_records (schedule_records.go) reads
// only through heldGrant, so a withdrawal holds at the next read. Members
// of a covered room see which room, read by whom, reviews them.
//
// A grant holds only while what the admin consented to holds
// (watchGrant): the reading room stays private (its members read what it
// reads: one that turns shared is withdrawn), its owner stays the grantee
// (a transfer pauses it, back to waiting), the admins are told of each
// member who joins it after the grant and of each of the grantee's AI
// tasks created or changed since, with an AI explanation against the
// purpose (the console shows the change until an admin acknowledges it).
// The watch runs when a change the edge proxies may affect it
// (afterSourceRoomChange), before every read, and when the console opens
// the ask.
//
//	GET    /v1/tenant/room-sources                          the organization's asks (org admin)
//	GET    /v1/tenant/room-sources/{roomId}/{source}        one ask: the room's owner, members, tasks, changes
//	PUT    /v1/tenant/room-sources/{roomId}/{source}        grant with a scope, read by the owner   wire.RoomSourceDecision
//	DELETE /v1/tenant/room-sources/{roomId}/{source}        withdraw
//	POST   /v1/tenant/room-sources/{roomId}/{source}/ack    acknowledge the tasks changed since the grant
//	GET    /v1/rooms/{roomId}/sources                       a member: the room's asks, who reviews it, its opt-ins
//	PUT    /v1/rooms/{roomId}/sources/{source}/opt-in       the room's owner opts its records in or out
//	POST   /v1/rooms/{roomId}/sources/{source}/ask-admin    the room's owner or an admin asks the admins to decide
//
// Grants, withdrawals, pauses, acknowledgments and opt-ins are audited
// (wire.AuditKindSource).

// sourceWatcher is the audit's actor of a change the watch finds rather
// than one a person's request made (a read's or the console's check).
const sourceWatcher = "room-sources"

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

// grantView is an ask as the console lists it.
func (s *Service) grantView(ctx context.Context, g store.RoomSourceGrant) wire.RoomSourceGrant {
	return wire.RoomSourceGrant{RoomID: g.RoomID, RoomName: s.roomName(ctx, g.RoomID), Source: g.Source, Purpose: g.Purpose,
		ComponentID: g.ComponentID, From: g.ComponentName + "@" + g.ComponentVersion, AskedBy: g.AskedBy, AskedAtMs: g.AskedAtMs,
		Scope: g.Scope, Grantee: g.Grantee, DecidedBy: g.DecidedBy, DecidedAtMs: g.DecidedAtMs, Changed: len(g.Watch.Drift)}
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

// sourceRoom is what the consent reads of a reading room now: the room,
// its human members, its owner and the owner's AI tasks in it.
type sourceRoom struct {
	room    store.Room
	members []store.Membership
	owner   string
	tasks   []wire.RoomSchedule
}

// readSourceRoom reads roomID as the consent needs it; tasks false skips
// the owner's tasks (a check that stops before them).
func (s *Service) readSourceRoom(ctx context.Context, tenantID, roomID string, tasks bool) (sourceRoom, error) {
	var out sourceRoom
	var err error
	if out.room, err = s.RoomInfo(ctx, roomID); err != nil {
		return out, err
	}
	if out.members, err = s.RoomMembers(ctx, roomID); err != nil {
		return out, err
	}
	for _, m := range out.members {
		if m.Role == store.RoleOwner {
			out.owner = m.UserID
		}
	}
	if tasks && out.owner != "" {
		out.tasks, err = s.sourceTasks(ctx, tenantID, roomID, out.owner)
	}
	return out, err
}

// sourceTasks are person's AI tasks in roomID (what reads a grant pinned
// to them), read through realtime's schedules route as that person.
func (s *Service) sourceTasks(ctx context.Context, tenantID, roomID, person string) ([]wire.RoomSchedule, error) {
	var list struct {
		Schedules []wire.RoomSchedule `json:"schedules"`
	}
	if err := s.realtimeDo(ctx, http.MethodGet, "/v1/rooms/"+url.PathEscape(roomID)+"/schedules", nil, person, tenantID, &list); err != nil {
		return nil, fmt.Errorf("read the room's scheduled tasks: %w", err)
	}
	return slices.DeleteFunc(list.Schedules, func(sc wire.RoomSchedule) bool {
		return sc.CreatedBy != person || sc.Recipient.Kind != wire.ScheduleRecipientAI
	}), nil
}

// memberIDs are the ids of members, in their order.
func memberIDs(members []store.Membership) []string {
	out := make([]string, 0, len(members))
	for _, m := range members {
		out = append(out, m.UserID)
	}
	return out
}

// consentedTasks are tasks as a grant keeps what the admin consented to.
func consentedTasks(tasks []wire.RoomSchedule) []wire.RoomSourceTask {
	out := make([]wire.RoomSourceTask, 0, len(tasks))
	for _, t := range tasks {
		out = append(out, wire.RoomSourceTask{ScheduleID: t.ScheduleID, Text: t.Text})
	}
	return out
}

// explainSourceInput is the explain-source-use assist's <text> for an ask
// (its one builder: the grant dialog runs it from the console's detail,
// the watch for a drift's notice): the source, what it holds, the stated
// purpose and the tasks in their own words with when they run; a task
// changed since the admin consented carries its words then as before ("",
// a new task).
func explainSourceInput(source, purpose string, tasks []wire.RoomSchedule, before map[string]*string) string {
	type task struct {
		When     wire.ScheduleRecurrence `json:"when"`
		Timezone string                  `json:"timezone"`
		Task     string                  `json:"task"`
		Before   *string                 `json:"before,omitempty"`
	}
	in := struct {
		Source         string `json:"source"`
		Reads          string `json:"reads"`
		Purpose        string `json:"purpose"`
		ScheduledTasks []task `json:"scheduledTasks"`
	}{Source: source, Reads: sourceWords(source), Purpose: purpose, ScheduledTasks: []task{}}
	for _, t := range tasks {
		in.ScheduledTasks = append(in.ScheduledTasks, task{When: t.Recurrence, Timezone: t.Timezone, Task: t.Text, Before: before[t.ScheduleID]})
	}
	raw, _ := json.Marshal(in)
	return string(raw)
}

// handleRoomSourceDetail answers GET /v1/tenant/room-sources/{roomId}/{source}:
// the ask as it holds now (a granted one is watched first), with what its
// admin decides on.
func (s *Service) handleRoomSourceDetail(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	ctx := tenant.NewContext(r.Context(), claims.Tenant)
	g, err := s.heldGrant(ctx, claims.Tenant, r.PathValue("roomId"), r.PathValue("source"), sourceChange{actor: sourceWatcher, origin: s.publicOrigin(r)})
	if !writeSourceAskErr(w, err) {
		return
	}
	d, err := s.sourceDetail(ctx, claims.Tenant, g)
	if err != nil {
		obs.ServerError(w, "read the reading room failed", err)
		httpx.Error(w, "read the reading room failed", http.StatusBadGateway)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, d)
}

// writeSourceAskErr answers a failed read of an ask (true: there was none).
func writeSourceAskErr(w http.ResponseWriter, err error) bool {
	switch {
	case err == nil:
		return true
	case errors.Is(err, store.ErrRoomSourceNotFound):
		httpx.Error(w, err.Error(), http.StatusNotFound)
	default:
		obs.ServerError(w, "read the room's ask failed", err)
		httpx.Error(w, "read the room's ask failed", http.StatusInternalServerError)
	}
	return false
}

// sourceDetail is g with its reading room as the admin decides on it.
func (s *Service) sourceDetail(ctx context.Context, tenantID string, g store.RoomSourceGrant) (wire.RoomSourceDetail, error) {
	sr, err := s.readSourceRoom(ctx, tenantID, g.RoomID, true)
	if err != nil {
		return wire.RoomSourceDetail{}, err
	}
	d := wire.RoomSourceDetail{Grant: s.grantView(ctx, g), Shared: sr.room.Visibility == store.VisibilityShared, Owner: sr.owner,
		Members: []wire.RoomMember{}, Tasks: sr.tasks, Drift: g.Watch.Drift}
	if d.Tasks == nil {
		d.Tasks = []wire.RoomSchedule{}
	}
	if d.Drift == nil {
		d.Drift = []wire.RoomSourceDrift{}
	}
	for _, m := range sr.members {
		d.Members = append(d.Members, wire.RoomMember{UserID: m.UserID, Role: string(m.Role)})
	}
	before := map[string]*string{}
	for _, dr := range g.Watch.Drift {
		before[dr.ScheduleID] = &dr.Before
	}
	d.ExplainInput = explainSourceInput(g.Source, g.Purpose, sr.tasks, before)
	return d, nil
}

// handleDecideRoomSource answers PUT (grant, wire.RoomSourceDecision) and
// DELETE (withdraw) /v1/tenant/room-sources/{roomId}/{source} for the
// organization's admin. A grant needs a private reading room and is
// pinned to its owner, whom the admin names (a grant to someone no longer
// the owner is refused); it records the room's members and the owner's AI
// tasks as consented. A grant again (a new scope) consents to the tasks as
// they are now. A withdrawal holds at the reading room's next read; both
// are audited.
func (s *Service) handleDecideRoomSource(withdraw bool) sessionHandler {
	return func(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
		var in wire.RoomSourceDecision
		if !withdraw {
			if !httpx.DecodeJSON(w, r, 4<<10, &in) {
				return
			}
			if !slices.Contains(wire.RoomSourceScopes, in.Scope) {
				httpx.Error(w, fmt.Sprintf("scope is one of %v", wire.RoomSourceScopes), http.StatusBadRequest)
				return
			}
			if in.Grantee == "" {
				httpx.Error(w, "grantee is the room's owner the grant is read by", http.StatusBadRequest)
				return
			}
		}
		ctx := tenant.NewContext(r.Context(), claims.Tenant)
		roomID, source := r.PathValue("roomId"), r.PathValue("source")
		if _, err := s.RoomSources.GetRoomSourceGrant(ctx, roomID, source); !writeSourceAskErr(w, err) {
			return
		}
		d := store.RoomSourceDecision{RoomID: roomID, Source: source, Scope: in.Scope, By: claims.Subject, AtMs: s.nowTime().UnixMilli()}
		if !withdraw {
			sr, err := s.readSourceRoom(ctx, claims.Tenant, roomID, true)
			if err != nil {
				obs.ServerError(w, "read the reading room failed", err)
				httpx.Error(w, "read the reading room failed", http.StatusBadGateway)
				return
			}
			if sr.room.Visibility == store.VisibilityShared {
				httpx.Error(w, "the room is shared: anyone in the organization can join it and read what it reads. Its owner makes it private first", http.StatusConflict)
				return
			}
			if sr.owner == "" || in.Grantee != sr.owner {
				httpx.Error(w, fmt.Sprintf("the room's owner is %s now: look at the ask again", firstNonEmpty(sr.owner, "nobody")), http.StatusConflict)
				return
			}
			d.Grantee, d.Watch = sr.owner, store.RoomSourceWatch{Members: memberIDs(sr.members), Tasks: consentedTasks(sr.tasks)}
		}
		g, err := s.RoomSources.DecideRoomSource(ctx, d)
		if !writeSourceAskErr(w, err) {
			return
		}
		action, detail := "grant", fmt.Sprintf("%s for room %s (%s@%s), scope %s, read by %s", source, roomID, g.ComponentName, g.ComponentVersion, g.Scope, g.Grantee)
		if withdraw {
			action, detail = "withdraw", fmt.Sprintf("%s for room %s (%s@%s)", source, roomID, g.ComponentName, g.ComponentVersion)
		}
		s.audit(ctx, claims.Tenant, store.ExtAuditRecord{Kind: wire.AuditKindSource, Name: roomID, Action: action, Actor: claims.Subject,
			Outcome: wire.AuditOutcomeAllow, Detail: detail})
		httpx.WriteJSON(w, http.StatusOK, s.grantView(ctx, g))
	}
}

// handleAckRoomSource answers POST
// /v1/tenant/room-sources/{roomId}/{source}/ack: the admin consents to the
// grantee's tasks as they are now (the changes since the grant are no
// longer shown), audited. A waiting ask has nothing to acknowledge (409).
func (s *Service) handleAckRoomSource(w http.ResponseWriter, r *http.Request, claims auth.Claims) {
	ctx := tenant.NewContext(r.Context(), claims.Tenant)
	g, err := s.heldGrant(ctx, claims.Tenant, r.PathValue("roomId"), r.PathValue("source"), sourceChange{actor: sourceWatcher, origin: s.publicOrigin(r)})
	if !writeSourceAskErr(w, err) {
		return
	}
	if g.Scope == "" {
		httpx.Error(w, "the ask is not granted: nothing to acknowledge", http.StatusConflict)
		return
	}
	tasks, err := s.sourceTasks(ctx, claims.Tenant, g.RoomID, g.Grantee)
	if err != nil {
		obs.ServerError(w, "read the reading room failed", err)
		httpx.Error(w, "read the reading room failed", http.StatusBadGateway)
		return
	}
	g.Watch.Tasks, g.Watch.Drift = consentedTasks(tasks), nil
	if err := s.RoomSources.SetRoomSourceWatch(ctx, g.RoomID, g.Source, g.Grantee, g.Watch); err != nil {
		obs.ServerError(w, "save the acknowledgment failed", err)
		httpx.Error(w, "save the acknowledgment failed", http.StatusInternalServerError)
		return
	}
	s.audit(ctx, claims.Tenant, store.ExtAuditRecord{Kind: wire.AuditKindSource, Name: g.RoomID, Action: "acknowledge", Actor: claims.Subject,
		Outcome: wire.AuditOutcomeAllow, Detail: fmt.Sprintf("%s for room %s: %d tasks of %s as they are now", g.Source, g.RoomID, len(tasks), g.Grantee)})
	d, err := s.sourceDetail(ctx, claims.Tenant, g)
	if err != nil {
		obs.ServerError(w, "read the reading room failed", err)
		httpx.Error(w, "read the reading room failed", http.StatusBadGateway)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, d)
}

// sourceChange is who or what a watch runs for: the audit's actor (a
// person whose request changed the room, else sourceWatcher) and the
// origin the admins' notices link to the console on.
type sourceChange struct {
	actor  string
	origin string
}

// consoleURL is the organization console on ch's origin ("" without one).
func (ch sourceChange) consoleURL() string {
	if ch.origin == "" {
		return ""
	}
	return strings.TrimRight(ch.origin, "/") + "/#/org"
}

// afterSourceRoomChange is the proxy's hook on a successful change that
// may affect a room source's grant (apiPath.changesSourceRoom): it watches
// the room's grants before the answer leaves. An invite's redemption
// names its room in the answer.
func (s *Service) afterSourceRoomChange(r *http.Request, p apiPath, claims auth.Claims) func(*http.Response) error {
	return func(resp *http.Response) error {
		if resp.StatusCode < 200 || resp.StatusCode > 299 || s.RoomSources == nil {
			return nil
		}
		roomID := p.RoomID
		if roomID == "" {
			raw, err := io.ReadAll(resp.Body)
			_ = resp.Body.Close()
			resp.Body = io.NopCloser(bytes.NewReader(raw))
			var out struct {
				RoomID string `json:"roomId"`
			}
			if err != nil || json.Unmarshal(raw, &out) != nil || out.RoomID == "" {
				return nil
			}
			roomID = out.RoomID
		}
		s.watchRoomSources(context.WithoutCancel(r.Context()), s.inRoomTenant(claims, roomID).Tenant, roomID,
			sourceChange{actor: claims.Subject, origin: s.publicOrigin(r)})
		return nil
	}
}

// watchRoomSources watches every granted ask of roomID (watchGrant); a
// failure is logged: the next read watches again before it reads.
func (s *Service) watchRoomSources(ctx context.Context, tenantID, roomID string, ch sourceChange) {
	if s.RoomSources == nil || s.RoomInfo == nil || s.RoomMembers == nil {
		return
	}
	for _, src := range wire.RoomSources {
		if _, err := s.heldGrant(tenant.NewContext(ctx, tenantID), tenantID, roomID, src, ch); err != nil && !errors.Is(err, store.ErrRoomSourceNotFound) {
			slog.Warn("bnbloc gateway: watch a room source failed", "event", "bnbloc.room_source.watch_failed", "room", roomID, "source", src, "error", err)
		}
	}
}

// heldGrant is roomID's ask for source as it holds now: a granted ask is
// watched first (watchGrant), so a grant that no longer holds is back to
// waiting before anyone reads through it.
func (s *Service) heldGrant(ctx context.Context, tenantID, roomID, source string, ch sourceChange) (store.RoomSourceGrant, error) {
	g, err := s.RoomSources.GetRoomSourceGrant(ctx, roomID, source)
	if err != nil || g.Scope == "" {
		return g, err
	}
	return s.watchGrant(ctx, tenantID, g, ch)
}

// watchGrant checks a granted ask against its reading room now and
// returns it as it then stands:
//   - the room is shared: withdrawn (anyone in the organization can join
//     it from the plaza and read what it reads);
//   - its owner is not the grantee: paused, back to waiting (a grant is
//     pinned to the person the admin granted it to);
//   - members the admins were not told of: told, naming the room and the
//     person (the grant holds: a private room's members are invited);
//   - the grantee's AI tasks created or changed since the admin consented:
//     kept as the grant's drift with an AI explanation against the purpose
//     (explain-source-use, on the grantee's AI budget) and told (the
//     grant holds; the console shows the change until acknowledged).
//
// Every change is audited and told through notifyOrgAdmins. A task list
// that cannot be read leaves the drift for the next watch.
func (s *Service) watchGrant(ctx context.Context, tenantID string, g store.RoomSourceGrant, ch sourceChange) (store.RoomSourceGrant, error) {
	sr, err := s.readSourceRoom(ctx, tenantID, g.RoomID, false)
	if err != nil {
		return g, err
	}
	name := firstNonEmpty(sr.room.Name, g.RoomID)
	if sr.room.Visibility == store.VisibilityShared {
		return s.ungrant(ctx, tenantID, g, ch, "withdraw", "This room was made shared: anyone in the organization can join it and read what it reads, so its grant to read "+
			sourceWords(g.Source)+" is withdrawn. Its owner makes it private again and asks you to grant it again.")
	}
	if sr.owner != g.Grantee {
		return s.ungrant(ctx, tenantID, g, ch, "pause", fmt.Sprintf("%s no longer owns this room (%s does), so its grant to read %s, read by %s, waits for you again. Grant it again to have it read by its new owner.",
			g.Grantee, firstNonEmpty(sr.owner, "nobody"), sourceWords(g.Source), g.Grantee))
	}
	w := g.Watch
	var joined []string
	for _, m := range sr.members {
		if !slices.Contains(w.Members, m.UserID) {
			joined = append(joined, m.UserID)
		}
	}
	w.Members = memberIDs(sr.members)
	var changed []wire.RoomSourceDrift
	if tasks, err := s.sourceTasks(ctx, tenantID, g.RoomID, g.Grantee); err != nil {
		slog.Warn("bnbloc gateway: read a reading room's tasks failed", "event", "bnbloc.room_source.tasks_failed", "room", g.RoomID, "error", err)
	} else {
		w.Drift, changed = taskDrift(w, tasks, s.nowTime().UnixMilli())
		if len(changed) > 0 {
			summary, mismatch := s.explainDrift(ctx, tenantID, g, tasks, changed)
			for i := range w.Drift {
				if isChanged(changed, w.Drift[i].ScheduleID) {
					w.Drift[i].Summary, w.Drift[i].Mismatch = summary, mismatch
				}
			}
		}
	}
	if len(joined) == 0 && len(changed) == 0 && slices.Equal(w.Members, g.Watch.Members) && slices.Equal(w.Drift, g.Watch.Drift) {
		return g, nil
	}
	if err := s.RoomSources.SetRoomSourceWatch(ctx, g.RoomID, g.Source, g.Grantee, w); err != nil {
		return g, err
	}
	g.Watch = w
	for _, m := range joined {
		s.tellSourceAdmins(ctx, tenantID, g, ch, notify.Event{Actor: m, Title: m + " joined a room that reads the organization's " + sourceWords(g.Source),
			Text: fmt.Sprintf("%s reads %s under your grant (read by %s), and its members read what it posts. Purpose: %s. Withdraw the grant in Organization → Room sources if %s should not.",
				name, sourceWords(g.Source), g.Grantee, g.Purpose, m),
			DedupKey: fmt.Sprintf("room-source-member:%s:%s:%s:%d", g.RoomID, g.Source, m, g.DecidedAtMs)})
	}
	for _, c := range w.Drift {
		if !isChanged(changed, c.ScheduleID) {
			continue
		}
		title, text := driftNotice(g, c)
		s.tellSourceAdmins(ctx, tenantID, g, ch, notify.Event{Actor: g.Grantee, Title: title, Text: text,
			DedupKey: fmt.Sprintf("room-source-task:%s:%s:%s:%x", g.RoomID, g.Source, c.ScheduleID, sha256.Sum256([]byte(c.After)))})
	}
	return g, nil
}

// driftNotice is the admins' notice of a task changed since they
// consented: the words then and now, and the AI explanation against the
// purpose (a mismatch first).
func driftNotice(g store.RoomSourceGrant, c wire.RoomSourceDrift) (title, text string) {
	verb, text := "changed", "Before: "+oneLine(c.Before)+"\nNow: "+oneLine(c.After)+"\n"
	if c.Before == "" {
		verb, text = "added", "The task: "+oneLine(c.After)+"\n"
	}
	switch {
	case c.Mismatch != "":
		text += "Beyond the stated purpose (AI): " + c.Mismatch
	case c.Summary != "":
		text += "AI: " + c.Summary
	default:
		text += "No AI explanation could be made: read the task against the purpose (" + oneLine(g.Purpose) + ")."
	}
	return fmt.Sprintf("%s %s a task that reads the organization's %s", g.Grantee, verb, sourceWords(g.Source)), text
}

// taskDrift is w's drift against the grantee's tasks now: a task whose
// words differ from what the admin consented to (or that is new) is in
// it, kept as it was while its words have not changed again; a task back
// to its consented words, or deleted, leaves it. changed are the entries
// new or changed by this watch (the ones to tell).
func taskDrift(w store.RoomSourceWatch, tasks []wire.RoomSchedule, atMs int64) (drift, changed []wire.RoomSourceDrift) {
	consented := map[string]string{}
	for _, t := range w.Tasks {
		consented[t.ScheduleID] = t.Text
	}
	prev := map[string]wire.RoomSourceDrift{}
	for _, d := range w.Drift {
		prev[d.ScheduleID] = d
	}
	for _, t := range tasks {
		before, known := consented[t.ScheduleID]
		if known && before == t.Text {
			continue
		}
		if d, ok := prev[t.ScheduleID]; ok && d.After == t.Text {
			drift = append(drift, d)
			continue
		}
		d := wire.RoomSourceDrift{ScheduleID: t.ScheduleID, Before: before, After: t.Text, AtMs: atMs}
		drift, changed = append(drift, d), append(changed, d)
	}
	return drift, changed
}

// isChanged reports whether scheduleID is one of changed.
func isChanged(changed []wire.RoomSourceDrift, scheduleID string) bool {
	return slices.ContainsFunc(changed, func(d wire.RoomSourceDrift) bool { return d.ScheduleID == scheduleID })
}

// explainDrift asks explain-source-use (the built-in, for the grantee:
// their organization's model and AI budget) what the changed tasks do
// against g's purpose; empty when it cannot (assists off, a failure,
// logged).
func (s *Service) explainDrift(ctx context.Context, tenantID string, g store.RoomSourceGrant, tasks []wire.RoomSchedule, changed []wire.RoomSourceDrift) (summary, mismatch string) {
	before := map[string]*string{}
	for _, c := range changed {
		before[c.ScheduleID] = &c.Before
	}
	only := slices.DeleteFunc(slices.Clone(tasks), func(t wire.RoomSchedule) bool { return before[t.ScheduleID] == nil })
	fields, err := s.runBuiltinJSON(ctx, auth.Claims{Subject: g.Grantee, Tenant: tenantID}, "explain-source-use", explainSourceInput(g.Source, g.Purpose, only, before))
	if err != nil {
		slog.Warn("bnbloc gateway: explain a reading room's changed task failed", "event", "bnbloc.room_source.explain_failed", "room", g.RoomID, "error", err)
		return "", ""
	}
	str := func(k string) string { v, _ := fields[k].(string); return strings.TrimSpace(v) }
	return str("summary"), str("mismatch")
}

// ungrant sends g back to waiting (action "withdraw" or "pause") for why,
// audited and told to the admins, and returns it.
func (s *Service) ungrant(ctx context.Context, tenantID string, g store.RoomSourceGrant, ch sourceChange, action, why string) (store.RoomSourceGrant, error) {
	out, err := s.RoomSources.DecideRoomSource(ctx, store.RoomSourceDecision{RoomID: g.RoomID, Source: g.Source, By: ch.actor, AtMs: s.nowTime().UnixMilli()})
	if err != nil {
		return g, err
	}
	s.audit(ctx, tenantID, store.ExtAuditRecord{Kind: wire.AuditKindSource, Name: g.RoomID, Action: action, Actor: ch.actor,
		Outcome: wire.AuditOutcomeAllow, Detail: fmt.Sprintf("%s for room %s (%s@%s), read by %s: %s", g.Source, g.RoomID, g.ComponentName, g.ComponentVersion, g.Grantee, why)})
	title := "A room's grant to read the organization's " + sourceWords(g.Source) + " is withdrawn"
	if action == "pause" {
		title = "A room's grant to read the organization's " + sourceWords(g.Source) + " waits for you again"
	}
	s.tellSourceAdmins(ctx, tenantID, g, ch, notify.Event{Actor: ch.actor, Title: title, Text: why,
		DedupKey: fmt.Sprintf("room-source-%s:%s:%s:%d", action, g.RoomID, g.Source, g.DecidedAtMs)})
	return out, nil
}

// tellSourceAdmins sends ev about g's room to the organization's admins
// through their one notification path (notifyOrgAdmins), linking to the
// console; a failure is logged (the change itself is kept and audited).
func (s *Service) tellSourceAdmins(ctx context.Context, tenantID string, g store.RoomSourceGrant, ch sourceChange, ev notify.Event) {
	ev.RoomID, ev.URL = g.RoomID, ch.consoleURL()
	if _, _, err := s.notifyOrgAdmins(ctx, tenantID, ev); err != nil {
		slog.Warn("bnbloc gateway: tell the admins of a room source failed", "event", "bnbloc.room_source.notify_failed", "room", g.RoomID, "error", err)
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
// stands, the rooms whose grants cover its records (named, with the
// person whose tasks read; linked for a member of the reading room) and
// the sources its owner opted it in to.
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
			info.Asks = append(info.Asks, wire.RoomSourceAskState{Source: src, Purpose: g.Purpose, Scope: g.Scope, Grantee: g.Grantee})
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
		rv := wire.RoomReviewer{Source: g.Source, RoomName: s.roomName(ctx, g.RoomID), Grantee: g.Grantee}
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
	if !writeSourceAskErr(w, err) {
		return
	}
	if g.Scope != "" {
		httpx.Error(w, "the organization's admin already granted it", http.StatusConflict)
		return
	}
	day := s.nowTime().UTC().Format(time.DateOnly)
	_, queued, err := s.notifyOrgAdmins(r.Context(), rc.Tenant, notify.Event{RoomID: roomID, Actor: claims.Subject,
		Title: claims.Subject + " asks you to let a room read the organization's " + sourceWords(source),
		Text:  "Purpose: " + g.Purpose + "\nFrom " + g.ComponentName + "@" + g.ComponentVersion + ". Decide in Organization → Room sources.",
		URL:   sourceChange{origin: s.publicOrigin(r)}.consoleURL(), DedupKey: "room-source-ask:" + roomID + ":" + source + ":" + day})
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
