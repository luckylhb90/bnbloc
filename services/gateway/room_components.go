package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/auth"
	"github.com/luckylhb90/bnbloc/internal/contentpkg"
	"github.com/luckylhb90/bnbloc/internal/contentshare"
	"github.com/luckylhb90/bnbloc/internal/contentuse"
	"github.com/luckylhb90/bnbloc/internal/extpipe"
	"github.com/luckylhb90/bnbloc/internal/httpx"
	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/tenant"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Room components (docs/prd/2026-10-20-rooms-as-components.md §2.1, slice
// 1): a room's setup saved as a room package (contentpkg.KindRoom) and
// installed as a new room.
//
//	GET  /v1/rooms/{roomId}/component-draft       → wire.RoomComponentDraft
//	POST /v1/content-components/{id}/create-room  wire.RoomInstallRequest → wire.RoomInstallReply
//
// The draft is what a room holds, read for its owner or an admin through
// the backends' own routes; the member reviews it and saves it through the
// content form like any kind (POST /v1/content-components, or a new
// version), so the one validator and the one save decide. Install creates
// a room the installer owns through the same routes a member's own clicks
// take: the room (its description the package's), its channels, its
// schedules disabled (realtime's checks, owned by the installer), the room
// expert and the packs (putContentUse, setRoomExpert) and the apps
// (copyApp, "Use this app"'s copy). A failure after the room exists
// deletes it, so a half-made room never lingers. The sources a package
// asks for are recorded as the new room's asks, waiting for the
// organization admin's consent (room_sources.go). What a package carries
// beyond the setup (the room's instructions, the apps made in it, its
// files, the tools its runs had) is captured, saved and installed by
// room_capture.go.

// handleRoomComponentDraft answers what "Save as component" captures of a
// room: its owner's or an admin's (the room editors'). save_component's
// room kind reads it through a job's grant, as the job's requester: the
// same capture as the dialog's.
func (s *Service) handleRoomComponentDraft(w http.ResponseWriter, r *http.Request, caller auth.Claims) {
	actor, ok := s.contentCaller(w, r, caller, grantHonored)
	if !ok {
		return
	}
	roomID := r.PathValue("roomId")
	editor, ok := s.roomEditorClaims(actor.Claims, roomID)
	if !ok {
		httpx.Error(w, "only the room's owner or admins save it as a component", http.StatusForbidden)
		return
	}
	d, err := s.roomDraft(r.Context(), editor, roomID)
	if err != nil {
		s.writeRoomBackendErr(w, "capture the room failed", err)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, d)
}

// roomDraft captures roomID's setup as claims (the room's editor, in its
// organization) read it: its name, description, welcome and quick prompts,
// its channels, its AI tasks and notices, the room expert and room packs,
// the apps copied in from works, each a part the editor may see (another
// member's private component could not be installed by anyone else), and
// the sources it asks for. What a package does not hold is said in Left.
func (s *Service) roomDraft(ctx context.Context, claims auth.Claims, roomID string) (wire.RoomComponentDraft, error) {
	var room wire.RoomDetail
	path := "/v1/rooms/" + url.PathEscape(roomID)
	if err := s.roomBackendDo(ctx, backendHTTP, http.MethodGet, path, "", nil, claims.Subject, claims.Tenant, &room); err != nil {
		return wire.RoomComponentDraft{}, err
	}
	title := firstNonEmpty(room.Name, roomID)
	d := wire.RoomComponentDraft{RoomID: roomID, Name: firstNonEmpty(contentpkg.NameFrom(title), "room"), DisplayName: title,
		Description: room.Description, Welcome: room.Welcome, QuickPrompts: room.QuickPrompts, Channels: []string{}, Schedules: []wire.RoomPackageSchedule{},
		Skills: []wire.RoomDraftPart{}, Prompts: []wire.RoomDraftPart{}, Apps: []wire.RoomDraftPart{}, MadeApps: []wire.RoomDraftDeliverable{},
		Files: []wire.RoomDraftDeliverable{}, Tools: []wire.RoomDraftTool{}, Sources: []wire.RoomSourceAsk{}, Left: []string{}}
	if d.QuickPrompts == nil {
		d.QuickPrompts = []wire.RoomQuickPrompt{}
	}
	if err := s.draftSources(ctx, claims, roomID, &d); err != nil {
		return d, err
	}
	var channels struct {
		Channels []struct {
			ChannelID string `json:"channelId"`
			Name      string `json:"name"`
			IsDefault bool   `json:"isDefault"`
		} `json:"channels"`
	}
	if err := s.roomBackendDo(ctx, backendHTTP, http.MethodGet, path+"/channels", "", nil, claims.Subject, claims.Tenant, &channels); err != nil {
		return d, err
	}
	names := map[string]string{} // channel id -> name, the default channel's ""
	for _, c := range channels.Channels {
		if !c.IsDefault {
			names[c.ChannelID] = c.Name
			d.Channels = append(d.Channels, c.Name)
		}
	}
	if err := s.draftSchedules(ctx, claims, roomID, names, &d); err != nil {
		return d, err
	}
	if s.ContentComponents != nil {
		if err := s.draftContent(ctx, claims, roomID, &d); err != nil {
			return d, err
		}
		if err := s.draftDeliverables(ctx, claims, roomID, &d); err != nil {
			return d, err
		}
		if err := s.draftTools(ctx, claims, roomID, &d); err != nil {
			return d, err
		}
	}
	d.Left = append(d.Left, "members and their agents: a room package names no people, whoever installs it invites them",
		"the data of its apps: an app is copied with a fresh, empty data space")
	return d, nil
}

// draftSchedules captures the room's AI tasks and notices to its members
// that recur (contentpkg's rule of a package's schedule); the others are
// said in Left.
func (s *Service) draftSchedules(ctx context.Context, claims auth.Claims, roomID string, channels map[string]string, d *wire.RoomComponentDraft) error {
	var list struct {
		Schedules []wire.RoomSchedule `json:"schedules"`
	}
	if err := s.realtimeDo(ctx, http.MethodGet, "/v1/rooms/"+url.PathEscape(roomID)+"/schedules", nil, claims.Subject, claims.Tenant, &list); err != nil {
		return err
	}
	misfits := map[contentpkg.ScheduleMisfit]int{}
	links := 0
	for _, sc := range list.Schedules {
		r := sc.Recipient
		if m := contentpkg.RoomScheduleMisfit(r, sc.Recurrence); m != contentpkg.ScheduleFits {
			misfits[m]++
			continue
		}
		if r.URL != "" {
			links++
			r.URL = ""
		}
		d.Schedules = append(d.Schedules, wire.RoomPackageSchedule{Channel: channels[sc.ChannelID], Text: sc.Text, Recipient: r,
			Recurrence: sc.Recurrence, Timezone: sc.Timezone})
	}
	for _, left := range []struct {
		misfit contentpkg.ScheduleMisfit
		what   string
	}{
		{contentpkg.ScheduleAsPerson, "posting as a person or driving an agent (a package's schedule is an AI task or a notice)"},
		{contentpkg.ScheduleToPeople, "notifying named people or a group (a package's notice goes to the room's members)"},
		{contentpkg.ScheduleOnce, "running once (a package's schedule recurs)"},
	} {
		if n := misfits[left.misfit]; n > 0 {
			d.Left = append(d.Left, plural(n, "schedule")+" "+left.what)
		}
	}
	if links > 0 {
		d.Left = append(d.Left, "the link of "+plural(links, "scheduled notice")+" (a link points into this room)")
	}
	return nil
}

// draftSources captures the sources the room asks for, each with its
// purpose, granted or not: the package asks again for the room installed
// from it (a deployment without the consent store has none).
func (s *Service) draftSources(ctx context.Context, claims auth.Claims, roomID string, d *wire.RoomComponentDraft) error {
	if s.RoomSources == nil {
		return nil
	}
	asks, err := s.roomAsks(tenant.NewContext(ctx, claims.Tenant), roomID)
	for _, g := range asks {
		d.Sources = append(d.Sources, wire.RoomSourceAsk{Name: g.Source, Purpose: g.Purpose})
	}
	return err
}

// draftContent captures the room expert and the room's skill and prompt
// packs, at the version in effect; a pinned expert that is not the room's
// is said in Left (a package names one expert), as is a part the editor
// may not see.
func (s *Service) draftContent(ctx context.Context, claims auth.Claims, roomID string, d *wire.RoomComponentDraft) error {
	expert, err := s.roomExpertOf(ctx, roomID)
	if err != nil {
		return err
	}
	uses, err := s.ContentComponents.ListContentUses(ctx, store.ContentUseRoom, roomID)
	if err != nil {
		return err
	}
	for _, u := range uses {
		c, err := s.ContentComponents.GetContentComponent(ctx, u.ComponentID)
		if errors.Is(err, store.ErrContentNotFound) {
			continue
		}
		if err != nil {
			return err
		}
		if !extpipe.MemberVisibility(viewerOf(claims), extpipe.GateForContent(c), 0).Visible {
			d.Left = append(d.Left, contentshare.Label(c)+" (not in your Library, so nobody else could install it)")
			continue
		}
		version := firstNonEmpty(u.PinnedVersion, c.LatestVersion)
		part := wire.RoomDraftPart{ID: c.ID, Name: c.Name, DisplayName: c.DisplayName, Version: version, Ref: contentpkg.MajorRef(c.Name, version)}
		switch c.Kind {
		case contentpkg.KindExpert:
			if c.ID != expert {
				d.Left = append(d.Left, contentshare.Label(c)+" (a package names only the room's expert; others are picked per message)")
				continue
			}
			d.Expert = &part
		case contentpkg.KindSkill:
			d.Skills = append(d.Skills, part)
		case contentpkg.KindPrompt:
			d.Prompts = append(d.Prompts, part)
		}
	}
	return nil
}

// plural is "1 schedule" or "n schedules".
func plural(n int, word string) string {
	if n == 1 {
		return "1 " + word
	}
	return fmt.Sprintf("%d %ss", n, word)
}

// handleCreateRoomFromComponent installs a room package visible to the
// caller as a new room of theirs and counts it as a use of the package
// (scope room-install).
func (s *Service) handleCreateRoomFromComponent(w http.ResponseWriter, r *http.Request, caller auth.Claims) {
	actor, ok := s.contentCaller(w, r, caller, grantRefused)
	if !ok {
		return
	}
	claims, ctx := actor.Claims, r.Context()
	c, err := s.ContentComponents.GetContentComponent(ctx, r.PathValue("id"))
	if err == nil && !extpipe.MemberVisibility(viewerOf(claims), extpipe.GateForContent(c), 0).Visible {
		err = store.ErrContentNotFound
	}
	if err != nil {
		contentStoreErr(w, "load content component failed", err)
		return
	}
	if c.Kind != contentpkg.KindRoom {
		httpx.Error(w, fmt.Sprintf("%s is a %s, not a room package", c.Name, shortKind(c.Kind)), http.StatusBadRequest)
		return
	}
	raw, ok := readBody(w, r, 64<<10)
	if !ok {
		return
	}
	var in wire.RoomInstallRequest
	if len(bytes.TrimSpace(raw)) > 0 && json.Unmarshal(raw, &in) != nil {
		httpx.Error(w, "invalid json body", http.StatusBadRequest)
		return
	}
	v, err := s.ContentComponents.GetContentComponentVersion(ctx, c.ID, firstNonEmpty(in.Version, c.LatestVersion))
	if err != nil {
		contentStoreErr(w, "load content version failed", err)
		return
	}
	pkg, err := s.contentPackage(ctx, v)
	if err != nil {
		obs.ServerError(w, "load content bundle failed", err)
		httpx.Error(w, "load content bundle failed", http.StatusBadGateway)
		return
	}
	ctx, cancel := context.WithTimeout(ctx, appCopyTimeout)
	defer cancel()
	reply, err := s.installRoom(ctx, claims, c, v, pkg, in)
	if err != nil {
		s.writeRoomBackendErr(w, "create the room failed", err)
		return
	}
	httpx.WriteJSON(w, http.StatusCreated, reply)
}

// installRoom makes a room of the package pkg (c at v) owned by the member
// in claims; anything after the room exists that fails deletes it (its
// deliverables go with it), with the app copies counted for it and the
// expert made from its instructions.
func (s *Service) installRoom(ctx context.Context, claims auth.Claims, c store.ContentComponent, v store.ContentComponentVersion, pkg *contentpkg.Package, in wire.RoomInstallRequest) (reply wire.RoomInstallReply, err error) {
	m := pkg.Manifest
	roomID := firstNonEmpty(in.RoomID, ids.New("room-"))
	reply = wire.RoomInstallReply{RoomID: roomID, Name: firstNonEmpty(in.Name, m.DisplayName, c.Name), ComponentID: c.ID, From: c.Name + "@" + v.Version,
		Channels: []string{}, Schedules: []string{}, Enabled: []string{}, Apps: []string{}, MadeApps: []string{}, Files: []string{}, Skipped: []string{},
		Sources: []wire.RoomSourceAsk{}, Tools: []string{}, ToolsToAdd: []string{}}
	prompts, err := s.roomQuickPrompts(ctx, claims, m, &reply)
	if err != nil {
		return reply, err
	}
	create, _ := json.Marshal(map[string]any{"roomId": roomID, "name": reply.Name, "description": m.Description, "visibility": in.Visibility,
		"welcome": m.Welcome, "quickPrompts": prompts})
	if err := s.roomBackendCall(ctx, http.MethodPost, "/v1/rooms", claims.Subject, claims.Tenant, create); err != nil {
		return reply, err
	}
	var copies []store.ContentUse            // app copies counted for this room
	var instructions *store.ContentComponent // the expert made from the instructions
	defer func() {
		if err == nil {
			return
		}
		bg := context.WithoutCancel(ctx)
		if derr := s.roomBackendCall(bg, http.MethodDelete, "/v1/rooms/"+url.PathEscape(roomID), claims.Subject, claims.Tenant, nil); derr != nil {
			log.Printf("gateway: room package %s: cleanup of room %s failed: %v", c.Name, roomID, derr)
		}
		for _, u := range copies {
			if derr := s.ContentComponents.DeleteContentUse(bg, u.ScopeKind, u.ScopeID, u.ComponentID); derr != nil {
				log.Printf("gateway: room package %s: uncount app copy %s failed: %v", c.Name, u.ScopeID, derr)
			}
		}
		if instructions != nil {
			if derr := s.ContentComponents.DeleteContentComponent(bg, instructions.ID); derr != nil {
				log.Printf("gateway: room package %s: delete the instructions' expert %s failed: %v", c.Name, instructions.ID, derr)
			}
		}
	}()
	channelIDs := map[string]string{}
	for _, name := range m.Channels {
		var ch struct {
			ChannelID string `json:"channelId"`
			Name      string `json:"name"`
		}
		body, _ := json.Marshal(map[string]string{"name": name})
		if err := s.roomBackendDo(ctx, backendHTTP, http.MethodPost, "/v1/rooms/"+url.PathEscape(roomID)+"/channels", "application/json", body,
			claims.Subject, claims.Tenant, &ch); err != nil {
			return reply, fmt.Errorf("create channel %q: %w", name, err)
		}
		channelIDs[strings.ToLower(name)] = ch.ChannelID
		reply.Channels = append(reply.Channels, ch.Name)
	}
	if err := s.installSchedules(ctx, claims, roomID, m.Schedules, channelIDs, &reply); err != nil {
		return reply, err
	}
	if err := s.installContent(ctx, claims, roomID, m, &reply); err != nil {
		return reply, err
	}
	if instructions, err = s.installInstructions(ctx, claims, roomID, pkg, &reply); err != nil {
		return reply, err
	}
	if copies, err = s.installApps(ctx, claims, roomID, m.Apps, &reply); err != nil {
		return reply, err
	}
	if err := s.installMadeApps(ctx, claims, roomID, pkg, &reply); err != nil {
		return reply, err
	}
	if err := s.installFiles(ctx, claims, roomID, pkg, &reply); err != nil {
		return reply, err
	}
	if err := s.installTools(ctx, claims, m.Requires.MCP, &reply); err != nil {
		return reply, err
	}
	if err := s.askRoomSources(ctx, claims, roomID, c, v.Version, m.Sources, &reply); err != nil {
		return reply, err
	}
	if err := s.ContentComponents.PutContentUse(ctx, store.ContentUse{TenantID: claims.Tenant, ScopeKind: store.ContentUseRoomInstall, ScopeID: roomID,
		ComponentID: c.ID, Kind: c.Kind, PinnedVersion: v.Version, CreatedBy: claims.Subject, CreatedAtMs: s.nowTime().UnixMilli()}); err != nil {
		return reply, fmt.Errorf("count the package's use: %w", err)
	}
	s.auditContent(ctx, "use", claims.Subject, claims.Tenant, c, v.Version,
		contentshare.Label(c)+" v"+v.Version+" installed by "+claims.Subject+" as room "+roomID)
	return reply, nil
}

// roomQuickPrompts are the package's quick prompts as the new room keeps
// them: a title and its text. One that names a prompt of a pack the package
// requires takes that prompt's text, resolved for the installer
// (contentuse.ResolvePromptRef); one whose pack they may not use is
// reported.
func (s *Service) roomQuickPrompts(ctx context.Context, claims auth.Claims, m contentpkg.Manifest, reply *wire.RoomInstallReply) ([]wire.RoomQuickPrompt, error) {
	out := []wire.RoomQuickPrompt{}
	for _, q := range m.QuickPrompts {
		if q.Ref == "" {
			out = append(out, wire.RoomQuickPrompt{Title: q.Title, Prompt: q.Prompt})
			continue
		}
		ref, err := contentuse.ResolvePromptRef(ctx, s.ContentComponents, viewerOf(claims), "", m.Requires.Prompts, q.Ref)
		if err != nil {
			return nil, err
		}
		if ref == nil {
			reply.Skipped = append(reply.Skipped, fmt.Sprintf("quick prompt %q (its prompt pack is not one you can use)", firstNonEmpty(q.Title, q.Ref)))
			continue
		}
		out = append(out, wire.RoomQuickPrompt{Title: firstNonEmpty(q.Title, ref.Prompt.Title), Prompt: ref.Prompt.Body})
	}
	return out, nil
}

// installSchedules creates the package's schedules in the room, disabled,
// as the installer (realtime's own route and checks). A kind this
// deployment cannot run (no AI runs: 503) is reported, not created.
func (s *Service) installSchedules(ctx context.Context, claims auth.Claims, roomID string, list []wire.RoomPackageSchedule, channelIDs map[string]string, reply *wire.RoomInstallReply) error {
	for _, sc := range list {
		body, _ := json.Marshal(map[string]any{"text": sc.Text, "recipient": sc.Recipient, "recurrence": sc.Recurrence,
			"timezone": sc.Timezone, "channelId": channelIDs[strings.ToLower(sc.Channel)], "enabled": false})
		var created wire.RoomSchedule
		err := s.realtimeDo(ctx, http.MethodPost, "/v1/rooms/"+url.PathEscape(roomID)+"/schedules", body, claims.Subject, claims.Tenant, &created)
		var rbe *roomBackendError
		switch {
		case errors.As(err, &rbe) && rbe.Status == http.StatusServiceUnavailable:
			reply.Skipped = append(reply.Skipped, fmt.Sprintf("schedule %q (%s)", truncateRunes(strings.Join(strings.Fields(sc.Text), " "), 60), rbe.Body))
		case err != nil:
			return fmt.Errorf("create a schedule: %w", err)
		default:
			reply.Schedules = append(reply.Schedules, created.ScheduleID)
		}
	}
	return nil
}

// installContent turns the package's room expert, skill packs and prompt
// packs on in the room, each resolved for the installer
// (contentuse.Resolve); one they may not use is reported. The expert is
// pinned and made the room's expert (setRoomExpert: a team stays pinned
// only).
func (s *Service) installContent(ctx context.Context, claims auth.Claims, roomID string, m contentpkg.Manifest, reply *wire.RoomInstallReply) error {
	type part struct{ ref, kind, word string }
	var parts []part
	if m.Requires.Expert != "" {
		parts = append(parts, part{m.Requires.Expert, contentpkg.KindExpert, "expert"})
	}
	for _, ref := range m.Requires.Skills {
		parts = append(parts, part{ref, contentpkg.KindSkill, "skill pack"})
	}
	for _, ref := range m.Requires.Prompts {
		parts = append(parts, part{ref, contentpkg.KindPrompt, "prompt pack"})
	}
	for _, p := range parts {
		c, v, found, err := contentuse.Resolve(ctx, s.ContentComponents, viewerOf(claims), p.ref, p.kind)
		if err != nil {
			return err
		}
		if !found {
			reply.Skipped = append(reply.Skipped, fmt.Sprintf("%s %s (not one you can use)", p.word, p.ref))
			continue
		}
		pin := ""
		if v.Version != c.LatestVersion {
			pin = v.Version
		}
		if err := s.putContentUse(ctx, claims, store.ContentUseRoom, roomID, c, pin); err != nil {
			return err
		}
		label := contentshare.Label(c)
		if p.kind != contentpkg.KindExpert {
			reply.Enabled = append(reply.Enabled, label)
			continue
		}
		var refused errRoomExpert
		switch err := s.setRoomExpert(ctx, claims, roomID, c); {
		case errors.As(err, &refused):
			reply.Skipped = append(reply.Skipped, fmt.Sprintf("%s as the room's expert (%s); it is pinned in the room", label, refused))
		case err != nil:
			return err
		default:
			reply.Expert = firstNonEmpty(c.DisplayName, c.Name)
		}
	}
	return nil
}

// installApps copies the package's apps into the room (copyApp), each the
// work its reference resolves to for the installer; one that is not an app
// they may use is reported. It answers the copies' use rows.
func (s *Service) installApps(ctx context.Context, claims auth.Claims, roomID string, refs []string, reply *wire.RoomInstallReply) ([]store.ContentUse, error) {
	var copies []store.ContentUse
	for _, ref := range refs {
		c, v, found, err := contentuse.Resolve(ctx, s.ContentComponents, viewerOf(claims), ref, contentpkg.KindWork)
		if err != nil {
			return copies, err
		}
		if !found {
			reply.Skipped = append(reply.Skipped, fmt.Sprintf("app %s (not a work you can use)", ref))
			continue
		}
		pkg, err := s.contentPackage(ctx, v)
		if err != nil {
			return copies, err
		}
		if pkg.Manifest.Type != wire.WorkTypeApp {
			reply.Skipped = append(reply.Skipped, fmt.Sprintf("app %s (a %s, not an app)", ref, pkg.Manifest.Type))
			continue
		}
		artifactID, _, err := s.copyApp(ctx, claims.Subject, claims, roomID, c, v, pkg)
		if err != nil {
			return copies, err
		}
		copies = append(copies, store.ContentUse{ScopeKind: store.ContentUseAppCopy, ScopeID: artifactID, ComponentID: c.ID})
		reply.Apps = append(reply.Apps, firstNonEmpty(c.DisplayName, c.Name))
	}
	return copies, nil
}

// installTools reports the MCP tools the package needs (requires.mcp: the
// tools the source room's runs had). A run's session gets a tool its
// content requires or its requester added, so one the installer can add
// and has not is in ToolsToAdd (the install dialog offers Add, the member
// catalog's install); one they may not use is reported as left out.
func (s *Service) installTools(ctx context.Context, claims auth.Claims, tools []contentpkg.MCPRequirement, reply *wire.RoomInstallReply) error {
	if len(tools) == 0 {
		return nil
	}
	cands, err := s.mcpCandidates(ctx, claims)
	if err != nil {
		return err
	}
	for _, t := range tools {
		reply.Tools = append(reply.Tools, t.Name)
		c, ok := cands[t.Name]
		switch {
		case !ok || !c.Visibility.Visible || c.Config.MCP == nil:
			reply.Skipped = append(reply.Skipped, fmt.Sprintf("MCP tool %s (not available to you)", t.Name))
		case !c.Visibility.Installed:
			reply.ToolsToAdd = append(reply.ToolsToAdd, t.Name)
		}
	}
	return nil
}
