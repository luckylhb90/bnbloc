package roomtools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strings"

	"github.com/luckylhb90/bnbloc/core"
	"github.com/luckylhb90/bnbloc/internal/roomlog"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// room_setup's channel action (docs/prd/2026-10-28-channels-and-threads.md
// §2.6): a channel made or set up as the person who asked, on the routes the
// room's channel menu uses (POST .../channels, PATCH .../channels/{id},
// POST .../archive and .../restore), through the agent door. The room
// service decides who may (mayManageChannel, mayPlaceChannel); a refusal
// comes back as its own words. Deleting a channel, its owner and who is in a
// section are never an agent's act (the door refuses them).

// channelArgs are the channel action's fields; a nil field is left as it is.
type channelArgs struct {
	Name      *string `json:"name"`
	Purpose   *string `json:"purpose"`
	Rules     *string `json:"rules"`
	SectionID *string `json:"sectionId"`
	Position  *int    `json:"position"`
	// DefaultAgent is a handle from list_room_agents ("" none), resolved to
	// the agent's key (owner, core, provider) before it is stored.
	DefaultAgent       *string `json:"defaultAgent"`
	WhoPosts           *string `json:"whoPosts"`
	NoAgents           *bool   `json:"noAgents"`
	AgentParticipation *string `json:"agentParticipation"`
	SlowModeSeconds    *int    `json:"slowModeSeconds"`
	Archived           *bool   `json:"archived"`
}

// settingsGiven reports whether a settings field is set.
func (a channelArgs) settingsGiven() bool {
	return a.DefaultAgent != nil || a.WhoPosts != nil || a.NoAgents != nil || a.AgentParticipation != nil || a.SlowModeSeconds != nil
}

// changed names the fields given, in words, for the room's line.
func (a channelArgs) changed() []string {
	var out []string
	for _, f := range []struct {
		set  bool
		word string
	}{
		{a.Name != nil, "name"}, {a.Purpose != nil, "purpose"}, {a.Rules != nil, "rules"}, {a.SectionID != nil, "section"},
		{a.Position != nil, "position"}, {a.DefaultAgent != nil, "default agent"}, {a.WhoPosts != nil, "who posts"},
		{a.NoAgents != nil, "agents admitted"}, {a.AgentParticipation != nil, "agent participation"}, {a.SlowModeSeconds != nil, "slow mode"},
	} {
		if f.set {
			out = append(out, f.word)
		}
	}
	return out
}

// channel makes a channel (no id) or changes one (id).
func (c setupCall) channel(ctx context.Context, in setupArgs) Result {
	if in.Channel == nil {
		return toolError("channel is required: the fields to set (room_help room_setup names them)")
	}
	a := *in.Channel
	id := strings.TrimSpace(in.ID)
	if id == "" {
		return c.newChannel(ctx, a)
	}
	if strings.ContainsAny(id, "/?#") {
		return toolError("id is a channel's id from get")
	}
	ch, found, res := c.findChannel(ctx, id)
	if res != nil {
		return *res
	}
	if !found {
		return toolError("no channel %q in this room (get lists the channels and their ids)", id)
	}
	if a.Archived == nil && len(a.changed()) == 0 {
		return toolError("give the channel's fields to change (name, purpose, rules, sectionId, position, defaultAgent, whoPosts, noAgents, agentParticipation, slowModeSeconds, archived)")
	}
	patch, res := c.channelPatch(ctx, a, ch.Settings)
	if res != nil {
		return *res
	}
	path := c.base + "/channels/" + url.PathEscape(id)
	archived := ch.ArchivedAtMs > 0
	// A restore comes before the other changes, an archive after them: the
	// changes land on a channel that is open.
	if a.Archived != nil && !*a.Archived && archived {
		if _, err := c.env.Gateway.Do(ctx, http.MethodPost, path+"/restore?"+c.door, nil); err != nil {
			return refusal("restoring #"+ch.Name, err)
		}
		c.announce(ctx, "restored the channel #"+ch.Name)
	}
	name := ch.Name
	if words := a.changed(); len(words) > 0 {
		raw, err := c.env.Gateway.Do(ctx, http.MethodPatch, path+"?"+c.door, patch)
		if err != nil {
			return refusal("changing #"+ch.Name, err)
		}
		var after wire.RoomChannel
		if json.Unmarshal(raw, &after) == nil && after.Name != "" {
			name = after.Name
		}
		c.announce(ctx, "changed the channel #"+name+": "+strings.Join(words, ", "))
	}
	if a.Archived != nil && *a.Archived && !archived {
		if _, err := c.env.Gateway.Do(ctx, http.MethodPost, path+"/archive?"+c.door, nil); err != nil {
			return refusal("archiving #"+name, err)
		}
		c.announce(ctx, "archived the channel #"+name)
	}
	return Result{Value: map[string]any{"channel": name, "id": id, "note": "Tell the person what you changed; they can change it again from the channel's menu."}}
}

// newChannel creates a channel by name (where the room's creation policy
// lets the person put one), then sets the rest of what was given on it.
func (c setupCall) newChannel(ctx context.Context, a channelArgs) Result {
	if a.Name == nil || strings.TrimSpace(*a.Name) == "" {
		return toolError("channel.name is required for a new channel (give id to change one)")
	}
	if a.Archived != nil && *a.Archived {
		return toolError("a new channel is not archived: make it, then archive it if the person asks")
	}
	create := wire.ChannelCreateRequest{Name: strings.TrimSpace(*a.Name)}
	if a.Purpose != nil {
		create.Purpose = *a.Purpose
	}
	if a.SectionID != nil {
		create.SectionID = *a.SectionID
	}
	rest := a
	rest.Name, rest.Purpose, rest.SectionID, rest.Archived = nil, nil, nil, nil
	// The rest is checked before anything is made: a bad default agent never
	// leaves a half-made channel.
	patch, res := c.channelPatch(ctx, rest, wire.ChannelSettings{})
	if res != nil {
		return *res
	}
	raw, err := c.env.Gateway.Do(ctx, http.MethodPost, c.base+"/channels?"+c.door, create)
	if err != nil {
		return refusal("adding the channel", err)
	}
	var ch wire.RoomChannel
	if err := json.Unmarshal(raw, &ch); err != nil || ch.ChannelID == "" {
		return failure("the channel answered an unreadable result: %v", err)
	}
	c.announce(ctx, "added the channel #"+ch.Name)
	out := map[string]any{"channel": ch.Name, "id": ch.ChannelID, "note": "Tell the person it is there."}
	if words := rest.changed(); len(words) > 0 {
		if _, err := c.env.Gateway.Do(ctx, http.MethodPatch, c.base+"/channels/"+url.PathEscape(ch.ChannelID)+"?"+c.door, patch); err != nil {
			r := refusal("setting up #"+ch.Name+" (it was made)", err)
			return r
		}
		c.announce(ctx, "changed the channel #"+ch.Name+": "+strings.Join(words, ", "))
	}
	return Result{Value: out}
}

// findChannel reads the room's channel id as the person sees it: among the
// open channels, else the archived ones.
func (c setupCall) findChannel(ctx context.Context, id string) (wire.RoomChannel, bool, *Result) {
	for _, q := range []string{"", wire.ChannelListArchived + "=1&"} {
		raw, err := c.env.Gateway.Do(ctx, http.MethodGet, c.base+"/channels?"+q+c.door, nil)
		if err != nil {
			r := refusal("reading the channels", err)
			return wire.RoomChannel{}, false, &r
		}
		var list wire.ChannelList
		if err := json.Unmarshal(raw, &list); err != nil {
			r := failure("the channels answered an unreadable result: %v", err)
			return wire.RoomChannel{}, false, &r
		}
		for _, ch := range list.Channels {
			if ch.ChannelID == id {
				return ch, true, nil
			}
		}
	}
	return wire.RoomChannel{}, false, nil
}

// channelPatch is the PATCH body of a: the settings document is the
// channel's current one (cur) with the given settings changed, since a
// PATCH replaces it whole.
func (c setupCall) channelPatch(ctx context.Context, a channelArgs, cur wire.ChannelSettings) (wire.ChannelPatchRequest, *Result) {
	p := wire.ChannelPatchRequest{Name: a.Name, Purpose: a.Purpose, Rules: a.Rules, SectionID: a.SectionID, Position: a.Position}
	if !a.settingsGiven() {
		return p, nil
	}
	s := cur
	if a.DefaultAgent != nil {
		handle := strings.TrimSpace(*a.DefaultAgent)
		if handle == "" {
			s.DefaultAgent = nil
		} else {
			agents, err := roomAgents(ctx, c.env)
			if err != nil {
				r := refusal("listing the room's agents", err)
				return p, &r
			}
			// The agent by its key (owner, core, provider), resolved from the
			// handle exactly; a handle that names none, or more than one, is
			// refused (nothing is guessed).
			target, ok := resolveHandle(agents, handle)
			if !ok {
				r := toolError("defaultAgent %q is not one of the room's agents online now (list_room_agents names them)", handle)
				return p, &r
			}
			s.DefaultAgent = &wire.ChannelAgent{OwnerID: target.Owner, Core: target.Core, Provider: target.Provider}
		}
	}
	if a.WhoPosts != nil {
		s.WhoPosts = strings.TrimSpace(*a.WhoPosts)
	}
	if a.NoAgents != nil {
		s.NoAgents = *a.NoAgents
	}
	if a.AgentParticipation != nil {
		s.AgentParticipation = strings.TrimSpace(*a.AgentParticipation)
	}
	if a.SlowModeSeconds != nil {
		s.SlowModeSeconds = *a.SlowModeSeconds
	}
	p.Settings = &s
	return p, nil
}

// channelsSetup is the room's channels, sections and archived channels as
// get reads them: ids, names, the people's text fenced, the settings.
func (c setupCall) channelsSetup(ctx context.Context, out map[string]any) *Result {
	views := map[string][]map[string]any{}
	var sections []map[string]any
	for _, q := range []string{"", wire.ChannelListArchived + "=1&"} {
		raw, err := c.env.Gateway.Do(ctx, http.MethodGet, c.base+"/channels?"+q+c.door, nil)
		if err != nil {
			r := refusal("reading the channels", err)
			return &r
		}
		var list wire.ChannelList
		if err := json.Unmarshal(raw, &list); err != nil {
			r := failure("the channels answered an unreadable result: %v", err)
			return &r
		}
		for _, ch := range list.Channels {
			row := map[string]any{"id": ch.ChannelID, "name": ch.Name}
			if ch.IsDefault {
				row["default"] = true
			}
			if ch.Purpose != "" {
				row["purpose"] = fenced(ch.Purpose)
			}
			if ch.Rules != "" {
				row["rules"] = fenced(ch.Rules)
			}
			if ch.SectionID != "" {
				row["sectionId"] = ch.SectionID
			}
			if s := settingsWords(ch.Settings); len(s) > 0 {
				row["settings"] = s
			}
			views[q] = append(views[q], row)
		}
		if q == "" {
			for _, sec := range list.Sections {
				row := map[string]any{"id": sec.SectionID, "name": sec.Name}
				if sec.Rules != "" {
					row["rules"] = fenced(sec.Rules)
				}
				sections = append(sections, row)
			}
		}
	}
	out["channels"] = nonNil(views[""])
	out["sections"] = nonNil(sections)
	if archived := views[wire.ChannelListArchived+"=1&"]; len(archived) > 0 {
		out["archivedChannels"] = archived
	}
	return nil
}

// settingsWords is a channel's settings that differ from the default.
func settingsWords(s wire.ChannelSettings) map[string]any {
	out := map[string]any{}
	if a := s.DefaultAgent; a != nil {
		// The handle list_room_agents and defaultAgent take: core-qualified
		// when the agent names a core.
		handle := roomlog.AgentSpeaker(a.OwnerID, a.Provider)
		if a.Core != "" {
			handle = roomlog.AgentSpeaker(a.OwnerID, core.QualifyTargetID(a.Core, a.Provider))
		}
		out["defaultAgent"] = handle
	}
	if s.WhoPosts != "" {
		out["whoPosts"] = s.WhoPosts
	}
	if s.NoAgents {
		out["noAgents"] = true
	}
	if s.AgentParticipation != "" {
		out["agentParticipation"] = s.AgentParticipation
	}
	if s.SlowModeSeconds > 0 {
		out["slowModeSeconds"] = s.SlowModeSeconds
	}
	return out
}

func nonNilStrings(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func nonNil(rows []map[string]any) []map[string]any {
	if rows == nil {
		return []map[string]any{}
	}
	return rows
}
