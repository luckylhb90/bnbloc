package room

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strings"
	"unicode/utf8"

	"github.com/luckylhb90/bnbloc/internal/ids"
	"github.com/luckylhb90/bnbloc/internal/obs"
	"github.com/luckylhb90/bnbloc/internal/roomctx"
	"github.com/luckylhb90/bnbloc/internal/schedule"
	"github.com/luckylhb90/bnbloc/internal/store"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Room templates (N3). A template is a reusable room setup: description,
// channels, starter prompts, suggested agents, default members, and
// schedules. Instantiating one creates the room through the same createRoom
// path as POST /v1/rooms, adds channels and members through the same helpers
// as their routes, and creates the schedules DISABLED and owned by the
// member who instantiated (nothing posts until they enable it).
//
// Suggested agents are hints only. bnbloc's borrow model is per-room,
// owner-granted, fail-closed: a template can never carry an authorization,
// so instantiation neither invites the agent's owner nor touches a grant. The
// UI offers both as explicit follow-ups.

// Template input bounds. They size a human-curated setup (a template that
// needs more is a provisioning script) and keep one body inside the
// 64 KiB request cap.
const (
	maxTemplateNameRunes    = 120
	maxTemplateDescRunes    = 1000
	maxRoomDescriptionRunes = 500 // rooms.description is VARCHAR(500)
	maxTemplateChannels     = 20
	maxChannelNameRunes     = 80
	maxStarterPrompts       = 20
	maxStarterPromptBytes   = 4 * 1024
	maxSuggestedAgents      = 20
	maxDefaultMembers       = 50
	maxTemplateSchedules    = 20
)

func (s *Service) registerTemplateRoutes(mux *http.ServeMux) {
	mux.HandleFunc("GET /v1/room-templates", s.handleListTemplates)
	mux.HandleFunc("POST /v1/room-templates", s.handleCreateTemplate)
	mux.HandleFunc("GET /v1/room-templates/{templateId}", s.handleGetTemplate)
	mux.HandleFunc("PATCH /v1/room-templates/{templateId}", s.handlePatchTemplate)
	mux.HandleFunc("DELETE /v1/room-templates/{templateId}", s.handleDeleteTemplate)
	// "from-room/{roomId}" and "{templateId}/instantiate" overlap as mux
	// patterns (from-room/instantiate matches both), so one two-segment POST
	// dispatches on the literal; template ids are "tpl_…", never "from-room".
	mux.HandleFunc("POST /v1/room-templates/{first}/{second}", s.handleTemplateAction)
	// Org-admin moderation: reachable only through /v1/tenant/*, which the
	// gateway gates on manage_org before signing the request.
	mux.HandleFunc("DELETE /v1/tenant/room-templates/{templateId}", s.handleModerateTemplate)
}

func (s *Service) handleTemplateAction(w http.ResponseWriter, r *http.Request) {
	first, second := r.PathValue("first"), r.PathValue("second")
	switch {
	case first == "from-room":
		s.handleTemplateFromRoom(w, r, second)
	case second == "instantiate":
		s.handleInstantiateTemplate(w, r, first)
	default:
		writeErr(w, http.StatusNotFound, "not found")
	}
}

func (s *Service) templatesReady(w http.ResponseWriter, r *http.Request) (string, bool) {
	user := caller(r)
	if user == "" {
		writeErr(w, http.StatusUnauthorized, "missing authenticated subject")
		return "", false
	}
	if s.Templates == nil {
		obs.ServerError(w, "room templates unavailable", nil)
		writeErr(w, http.StatusServiceUnavailable, "room templates unavailable")
		return "", false
	}
	return user, true
}

func writeTemplateErr(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, store.ErrTemplateNotFound):
		writeErr(w, http.StatusNotFound, "template not found")
	case errors.Is(err, store.ErrTemplateForbidden):
		writeErr(w, http.StatusForbidden, "only the author may change this template")
	default:
		log.Printf("room: template store: %v", err)
		writeErr(w, http.StatusInternalServerError, "template store error")
	}
}

func runesIn(s string, max int) bool { return utf8.RuneCountInString(s) <= max }

func validTemplateName(name string) error {
	switch {
	case name == "":
		return errors.New("name is required")
	case !runesIn(name, maxTemplateNameRunes):
		return errors.New("name too long")
	}
	return nil
}

func normalizeTemplateVisibility(v string) (string, error) {
	switch strings.TrimSpace(v) {
	case "", wire.TemplatePrivate:
		return wire.TemplatePrivate, nil
	case wire.TemplateTenant:
		return wire.TemplateTenant, nil
	}
	return "", fmt.Errorf("visibility must be %q or %q", wire.TemplatePrivate, wire.TemplateTenant)
}

// normalizeTemplateBody validates a template body and returns its normalized
// form: trimmed, de-duplicated, bounded; schedules validated with the same
// recurrence/recipient rules the schedule routes use.
func normalizeTemplateBody(b wire.RoomTemplateBody) (wire.RoomTemplateBody, error) {
	out := wire.RoomTemplateBody{
		RoomDescription: strings.TrimSpace(b.RoomDescription),
		Channels:        []string{},
		StarterPrompts:  []string{},
		SuggestedAgents: []wire.TemplateAgent{},
		DefaultMembers:  []string{},
		Schedules:       []wire.TemplateSchedule{},
	}
	if !runesIn(out.RoomDescription, maxRoomDescriptionRunes) {
		return out, errors.New("room description too long")
	}
	seen := map[string]bool{"general": true} // the default channel every room gets
	for _, c := range b.Channels {
		c = strings.TrimSpace(c)
		key := strings.ToLower(c)
		if c == "" || seen[key] {
			continue
		}
		if !runesIn(c, maxChannelNameRunes) {
			return out, fmt.Errorf("channel name %q too long", c)
		}
		seen[key] = true
		out.Channels = append(out.Channels, c)
	}
	if len(out.Channels) > maxTemplateChannels {
		return out, fmt.Errorf("at most %d channels", maxTemplateChannels)
	}
	for _, p := range b.StarterPrompts {
		if p = strings.TrimSpace(p); p == "" {
			continue
		}
		if len(p) > maxStarterPromptBytes {
			return out, errors.New("starter prompt too long")
		}
		out.StarterPrompts = append(out.StarterPrompts, p)
	}
	if len(out.StarterPrompts) > maxStarterPrompts {
		return out, fmt.Errorf("at most %d starter prompts", maxStarterPrompts)
	}
	agentSeen := map[string]bool{}
	for _, a := range b.SuggestedAgents {
		a = wire.TemplateAgent{OwnerID: strings.TrimSpace(a.OwnerID), Provider: strings.TrimSpace(a.Provider), Core: strings.TrimSpace(a.Core)}
		if a.OwnerID == "" || a.Provider == "" {
			return out, errors.New("a suggested agent needs ownerId and provider")
		}
		key := a.OwnerID + "\x00" + a.Core + "\x00" + a.Provider
		if agentSeen[key] {
			continue
		}
		agentSeen[key] = true
		out.SuggestedAgents = append(out.SuggestedAgents, a)
	}
	if len(out.SuggestedAgents) > maxSuggestedAgents {
		return out, fmt.Errorf("at most %d suggested agents", maxSuggestedAgents)
	}
	memberSeen := map[string]bool{}
	for _, m := range b.DefaultMembers {
		if m = strings.TrimSpace(m); m == "" || memberSeen[m] {
			continue
		}
		memberSeen[m] = true
		out.DefaultMembers = append(out.DefaultMembers, m)
	}
	if len(out.DefaultMembers) > maxDefaultMembers {
		return out, fmt.Errorf("at most %d default members", maxDefaultMembers)
	}
	for _, sc := range b.Schedules {
		norm, err := normalizeTemplateSchedule(sc, seen)
		if err != nil {
			return out, err
		}
		out.Schedules = append(out.Schedules, norm)
	}
	if len(out.Schedules) > maxTemplateSchedules {
		return out, fmt.Errorf("at most %d schedules", maxTemplateSchedules)
	}
	return out, nil
}

// normalizeTemplateSchedule validates one template schedule. channels holds
// the template's channel names (lower-cased) plus "general".
func normalizeTemplateSchedule(sc wire.TemplateSchedule, channels map[string]bool) (wire.TemplateSchedule, error) {
	text := strings.TrimSpace(sc.Text)
	if text == "" {
		return sc, errors.New("a schedule needs text")
	}
	if len(sc.Text) > wire.MaxChatTextBytes {
		return sc, errors.New("schedule text too long")
	}
	ch := strings.TrimSpace(sc.Channel)
	if strings.EqualFold(ch, "general") {
		ch = ""
	}
	if ch != "" && !channels[strings.ToLower(ch)] {
		return sc, fmt.Errorf("schedule channel %q is not a channel of the template", ch)
	}
	rcpt, err := schedule.NormalizeRecipient(sc.Recipient)
	if err != nil {
		return sc, err
	}
	rec, _, err := schedule.Validate(sc.Recurrence, sc.Timezone)
	if err != nil {
		return sc, err
	}
	return wire.TemplateSchedule{Channel: ch, Text: text, Recipient: rcpt, Recurrence: rec, Timezone: strings.TrimSpace(sc.Timezone)}, nil
}

func (s *Service) handleListTemplates(w http.ResponseWriter, r *http.Request) {
	user, ok := s.templatesReady(w, r)
	if !ok {
		return
	}
	list, err := s.Templates.ListTemplates(r.Context(), user, r.URL.Query().Get("mine") == "1")
	if err != nil {
		writeTemplateErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"templates": list})
}

func (s *Service) handleGetTemplate(w http.ResponseWriter, r *http.Request) {
	user, ok := s.templatesReady(w, r)
	if !ok {
		return
	}
	t, err := s.Templates.GetTemplate(r.Context(), r.PathValue("templateId"), user)
	if err != nil {
		writeTemplateErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

// templateInput is the create body (and, with nil fields unchanged, the
// patch body).
type templateInput struct {
	Name        *string                `json:"name"`
	Description *string                `json:"description"`
	Visibility  *string                `json:"visibility"`
	Body        *wire.RoomTemplateBody `json:"body"`
}

func (s *Service) handleCreateTemplate(w http.ResponseWriter, r *http.Request) {
	user, ok := s.templatesReady(w, r)
	if !ok {
		return
	}
	var in templateInput
	if !decode(w, r, &in) {
		return
	}
	var body wire.RoomTemplateBody
	if in.Body != nil {
		body = *in.Body
	}
	s.saveNewTemplate(w, r, user, in, body)
}

// saveNewTemplate validates and stores a new template authored by user.
func (s *Service) saveNewTemplate(w http.ResponseWriter, r *http.Request, user string, in templateInput, body wire.RoomTemplateBody) {
	t := wire.RoomTemplate{TemplateID: ids.New("tpl_"), CreatedBy: user, CreatedAtMs: s.Now(), UpdatedAtMs: s.Now()}
	if in.Name != nil {
		t.Name = strings.TrimSpace(*in.Name)
	}
	if in.Description != nil {
		t.Description = strings.TrimSpace(*in.Description)
	}
	var vis string
	if in.Visibility != nil {
		vis = *in.Visibility
	}
	var err error
	if err = validTemplateName(t.Name); err == nil {
		if !runesIn(t.Description, maxTemplateDescRunes) {
			err = errors.New("description too long")
		} else if t.Visibility, err = normalizeTemplateVisibility(vis); err == nil {
			t.Body, err = normalizeTemplateBody(body)
		}
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	created, err := s.Templates.CreateTemplate(r.Context(), t)
	if err != nil {
		writeTemplateErr(w, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (s *Service) handlePatchTemplate(w http.ResponseWriter, r *http.Request) {
	user, ok := s.templatesReady(w, r)
	if !ok {
		return
	}
	var in templateInput
	if !decode(w, r, &in) {
		return
	}
	var patch store.TemplatePatch
	if in.Name != nil {
		name := strings.TrimSpace(*in.Name)
		if err := validTemplateName(name); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		patch.Name = &name
	}
	if in.Description != nil {
		d := strings.TrimSpace(*in.Description)
		if !runesIn(d, maxTemplateDescRunes) {
			writeErr(w, http.StatusBadRequest, "description too long")
			return
		}
		patch.Description = &d
	}
	if in.Visibility != nil {
		v, err := normalizeTemplateVisibility(*in.Visibility)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		patch.Visibility = &v
	}
	if in.Body != nil {
		b, err := normalizeTemplateBody(*in.Body)
		if err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		patch.Body = &b
	}
	t, err := s.Templates.UpdateTemplate(r.Context(), r.PathValue("templateId"), user, patch, s.Now())
	if err != nil {
		writeTemplateErr(w, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Service) handleDeleteTemplate(w http.ResponseWriter, r *http.Request) {
	s.deleteTemplate(w, r, false)
}

func (s *Service) handleModerateTemplate(w http.ResponseWriter, r *http.Request) {
	s.deleteTemplate(w, r, true)
}

func (s *Service) deleteTemplate(w http.ResponseWriter, r *http.Request, moderator bool) {
	user, ok := s.templatesReady(w, r)
	if !ok {
		return
	}
	if err := s.Templates.DeleteTemplate(r.Context(), r.PathValue("templateId"), user, moderator); err != nil {
		writeTemplateErr(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleTemplateFromRoom saves a room as a template (room owner or admin).
// The server captures what it owns — the room description, its channels,
// its members (includeMembers), and its schedules (includeSchedules); the
// client supplies what the member picks: starter prompts (recent messages
// they chose) and suggested agents (from the room's agent roster).
func (s *Service) handleTemplateFromRoom(w http.ResponseWriter, r *http.Request, rawRoomID string) {
	user, ok := s.templatesReady(w, r)
	if !ok {
		return
	}
	roomID, err := roomctx.Normalize(rawRoomID)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if role, ok := s.Store.MemberRole(r.Context(), roomID, user); !ok || !role.CanInvite() {
		writeErr(w, http.StatusForbidden, "only the room owner or an admin may save it as a template")
		return
	}
	var in struct {
		templateInput
		StarterPrompts   []string             `json:"starterPrompts"`
		SuggestedAgents  []wire.TemplateAgent `json:"suggestedAgents"`
		IncludeMembers   bool                 `json:"includeMembers"`
		IncludeSchedules bool                 `json:"includeSchedules"`
	}
	if !decode(w, r, &in) {
		return
	}
	rm, err := s.Store.GetRoom(r.Context(), roomID)
	if err != nil {
		writeErr(w, http.StatusNotFound, "room not found")
		return
	}
	body := wire.RoomTemplateBody{
		RoomDescription: rm.Description,
		StarterPrompts:  in.StarterPrompts,
		SuggestedAgents: in.SuggestedAgents,
	}
	channelNames := map[string]string{}
	for _, c := range s.Channels.ListChannels(r.Context(), roomID) {
		// The people chat is per-room conversation, not room setup.
		if c.IsDefault || c.IsPeople() {
			continue
		}
		channelNames[c.ChannelID] = c.Name
		body.Channels = append(body.Channels, c.Name)
	}
	if in.IncludeMembers {
		for _, m := range s.Store.Members(r.Context(), roomID) {
			// Agent hosts are not people a template should add.
			if m.UserID != user && !m.AgentHost {
				body.DefaultMembers = append(body.DefaultMembers, m.UserID)
			}
		}
	}
	if in.IncludeSchedules && s.Schedules != nil {
		list, err := s.Schedules.ListSchedules(r.Context(), roomID)
		if err != nil {
			obs.ServerError(w, "could not read the room's schedules", err)
			writeErr(w, http.StatusInternalServerError, "could not read the room's schedules")
			return
		}
		for _, sc := range list {
			body.Schedules = append(body.Schedules, wire.TemplateSchedule{
				Channel: channelNames[sc.ChannelID], Text: sc.Text, Recipient: sc.Recipient,
				Recurrence: sc.Recurrence, Timezone: sc.Timezone,
			})
		}
	}
	if in.Name == nil || strings.TrimSpace(*in.Name) == "" {
		name := rm.Name
		if name == "" {
			name = rm.RoomID
		}
		in.Name = &name
	}
	s.saveNewTemplate(w, r, user, in.templateInput, body)
}

// handleInstantiateTemplate creates a room from a template. Body: {roomId,
// name?, visibility?}. The room is created exactly like POST /v1/rooms (the
// caller owns it); channels, default members, and disabled schedules follow.
// A failure after the room exists deletes it, so a half-applied template
// never lingers.
func (s *Service) handleInstantiateTemplate(w http.ResponseWriter, r *http.Request, templateID string) {
	user, ok := s.templatesReady(w, r)
	if !ok {
		return
	}
	var in struct {
		RoomID     string `json:"roomId"`
		Name       string `json:"name"`
		Visibility string `json:"visibility"`
	}
	if !decode(w, r, &in) {
		return
	}
	t, err := s.Templates.GetTemplate(r.Context(), templateID, user)
	if err != nil {
		writeTemplateErr(w, err)
		return
	}
	roomID, err := roomctx.Normalize(in.RoomID)
	if err == nil {
		err = roomctx.ValidateID(roomID)
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid room id: 1-64 bytes, no whitespace edges, control characters, or / \\ ? # %")
		return
	}
	name := strings.TrimSpace(in.Name)
	if name == "" {
		name = roomID
	}
	res, err := s.instantiate(r.Context(), user, t, store.Room{
		RoomID: roomID, Name: name, Description: t.Body.RoomDescription,
		Visibility: store.NormalizeVisibility(in.Visibility), SyncMode: store.NormalizeSyncMode(""),
		CreatedAtMs: s.Now(),
	})
	if errors.Is(err, store.ErrRoomExists) {
		writeErr(w, http.StatusConflict, "room id already exists")
		return
	}
	if err != nil {
		obs.ServerError(w, "instantiate template failed", err)
		writeErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := s.Templates.UseTemplate(r.Context(), t.TemplateID, user); err != nil {
		log.Printf("room: template %s use count: %v", t.TemplateID, err)
	}
	writeJSON(w, http.StatusOK, res)
}

// instantiate applies template t to a new room owned by user.
func (s *Service) instantiate(ctx context.Context, user string, t wire.RoomTemplate, room store.Room) (res wire.RoomTemplateInstance, err error) {
	// A stored template was valid when saved; re-normalize so a template
	// saved under older bounds still applies safely.
	body, err := normalizeTemplateBody(t.Body)
	if err != nil {
		return res, fmt.Errorf("template is no longer valid: %w", err)
	}
	if _, err = s.createRoom(ctx, room, user, ""); err != nil {
		return res, err
	}
	defer func() {
		if err != nil {
			if derr := s.Store.DeleteRoom(context.WithoutCancel(ctx), room.RoomID); derr != nil {
				log.Printf("room: template cleanup of %s failed: %v", room.RoomID, derr)
			}
		}
	}()
	res = wire.RoomTemplateInstance{
		RoomID: room.RoomID, Name: room.Name, TemplateID: t.TemplateID,
		Channels: []string{}, AddedMembers: []string{}, Schedules: []string{},
		StarterPrompts: body.StarterPrompts, SuggestedAgents: body.SuggestedAgents,
	}
	channelIDs := map[string]string{}
	for _, name := range body.Channels {
		ch, cerr := s.createChannel(ctx, room.RoomID, name, user)
		if cerr != nil {
			return res, fmt.Errorf("create channel %q: %w", name, cerr)
		}
		channelIDs[strings.ToLower(name)] = ch.ChannelID
		res.Channels = append(res.Channels, ch.Name)
	}
	// Default members join through the add-member path with its normal
	// policy (the creator is the owner, so may add). One member that cannot
	// be added does not sink the room: it is reported, and the creator can
	// invite them later.
	for _, m := range body.DefaultMembers {
		if m == user {
			continue
		}
		if aerr := s.addMember(ctx, room.RoomID, m, store.RoleMember); aerr != nil {
			log.Printf("room: template %s add member %s to %s: %v", t.TemplateID, m, room.RoomID, aerr)
			res.FailedMembers = append(res.FailedMembers, m)
			continue
		}
		res.AddedMembers = append(res.AddedMembers, m)
	}
	if len(body.Schedules) > 0 && s.Schedules == nil {
		return res, errors.New("schedules unavailable")
	}
	now := s.Now()
	for _, ts := range body.Schedules {
		sc, serr := s.Schedules.CreateSchedule(ctx, wire.RoomSchedule{
			ScheduleID: ids.New("sch_"), RoomID: room.RoomID, ChannelID: channelIDs[strings.ToLower(ts.Channel)],
			CreatedBy: user, Text: ts.Text, Recipient: ts.Recipient, Recurrence: ts.Recurrence,
			Timezone: ts.Timezone, Enabled: false, CreatedAtMs: now, UpdatedAtMs: now,
		})
		if serr != nil {
			return res, fmt.Errorf("create schedule: %w", serr)
		}
		res.Schedules = append(res.Schedules, sc.ScheduleID)
	}
	return res, nil
}
