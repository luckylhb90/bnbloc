package contentpkg

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/tarbundle"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Channel presets (docs/prd/2026-10-28-channels-and-threads.md §2.1c): a
// kind of channel (a forum, a help desk, a tracker, an announcements
// channel) is content, never a platform name. A preset holds what a
// channel's owners set by hand: its purpose and rules and its settings
// document, and optionally the expert the channel's agent wears
// (requires.expert). Applying one is one PATCH of the channel through the
// room service's own route, so the room service stays the one owner of what
// a setting means: the preset's settings are that route's settings document,
// carried as JSON. The keys the room service knows today are checked here by
// its own rules (wire.IsChannelWhoPosts, wire.IsChannelAgentParticipation);
// the switches the channel gains next (posting style, tags, statuses, a post
// template) are checked by shape and bound only, and the room service decides
// them when the preset is applied. component.json holds all of it; README.md
// is optional. A preset is no content use: nothing in it reaches an agent
// until it is applied to a channel.

// KindChannelPreset is the channel preset kind.
const KindChannelPreset = "bnbloc.channel.preset/v1"

// Bounds of a channel preset.
const (
	maxPresetTags      = 30
	maxPresetStatuses  = 12
	maxPresetTagName   = 40
	maxPresetTagID     = 40
	maxPresetColor     = 16
	maxPostTemplate    = 4000
	maxPresetSlowMode  = 6 * 60 * 60
	maxPresetSettingsB = 16 << 10
)

// PresetLimits is a channel preset's bundle profile: component.json and an
// optional README.md.
var PresetLimits = tarbundle.Limits{MaxBytes: 128 << 10, MaxFiles: 2}

// ChannelPreset is a preset's channel setup: what applying it sets on the
// channel (its API shape, wire.ChannelPreset).
type ChannelPreset = wire.ChannelPreset

// presetSettings reads a preset's settings document by the room service's
// keys: the ones it knows today by its own rules, the coming switches by
// shape and bound.
type presetSettings struct {
	WhoPosts           string `json:"whoPosts"`
	NoAgents           bool   `json:"noAgents"`
	SlowModeSeconds    int    `json:"slowModeSeconds"`
	AgentParticipation string `json:"agentParticipation"`
	PostingStyle       string `json:"postingStyle"`
	Tags               []struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		Color string `json:"color"`
	} `json:"tags"`
	Statuses []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"statuses"`
	PostTemplate string `json:"postTemplate"`
}

// presetSettingKeys are the keys a preset's settings may hold: a channel's
// owners' settings, never a default agent (an agent is a person's, a
// preset names no people) and nothing unknown.
var presetSettingKeys = map[string]bool{
	"whoPosts": true, "noAgents": true, "slowModeSeconds": true, "agentParticipation": true,
	"postingStyle": true, "tags": true, "statuses": true, "postTemplate": true,
}

// Posting styles a preset may set (§2.1c "Posting style").
var presetPostingStyles = map[string]bool{"": true, "messages": true, "threads": true}

var errPresetFields = errors.New("a channel's purpose, rules and settings belong to a channel preset")

func init() {
	register(KindSpec{Kind: KindChannelPreset, Short: "channel-preset", Limits: PresetLimits,
		validate: validateChannelPreset, fromForm: channelPresetFromForm})
}

// validateChannelPreset: a channel's purpose and rules within the room
// service's bounds, settings the room service takes, an optional expert
// (requires.expert), and beside component.json only README.md.
func validateChannelPreset(p *Package) error {
	m := p.Manifest
	switch {
	case len(m.Skills) > 0 || len(m.Prompts) > 0 || m.Team != nil:
		return errors.New("a channel preset lists no skills, prompts or team; its channel's agent wears requires.expert")
	case len(m.QuickPrompts) > 0 || m.Welcome != "":
		return errors.New("quick prompts and a welcome belong to experts and room packages")
	case len(m.Requires.Skills)+len(m.Requires.Prompts)+len(m.Requires.MCP) > 0:
		return errors.New("a channel preset requires at most an expert (requires.expert)")
	case m.declares(PermScripts):
		return errors.New("a channel preset ships no scripts")
	case m.Channel == nil:
		return errors.New("a channel preset holds its channel's setup (channel)")
	}
	for _, f := range p.Bundle.Paths() {
		if f != ManifestFile && f != ReadmeFile {
			return fmt.Errorf("%s: a channel preset holds only %s and %s", f, ManifestFile, ReadmeFile)
		}
	}
	if _, ok := p.Bundle.File(ReadmeFile); ok {
		if _, err := readText(p.Bundle, ReadmeFile, maxInstructions); err != nil {
			return err
		}
	}
	return CheckChannelPreset(*m.Channel)
}

// CheckChannelPreset checks a preset's channel setup (a save's, an assist's
// draft the web shows): what the room service would take on a PATCH.
func CheckChannelPreset(c ChannelPreset) error {
	if err := checkText("channel.purpose", c.Purpose, wire.MaxChannelPurpose, true); err != nil {
		return err
	}
	if err := checkText("channel.rules", c.Rules, wire.MaxChannelRules, false); err != nil {
		return err
	}
	if len(c.Settings) == 0 {
		return nil
	}
	if len(c.Settings) > maxPresetSettingsB {
		return fmt.Errorf("channel.settings is larger than %d bytes", maxPresetSettingsB)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(c.Settings, &keys); err != nil {
		return fmt.Errorf("channel.settings: a JSON object: %w", err)
	}
	for k := range keys {
		if !presetSettingKeys[k] {
			return fmt.Errorf("channel.settings.%s: a preset sets a channel's whoPosts, noAgents, slowModeSeconds, agentParticipation, postingStyle, tags, statuses and postTemplate", k)
		}
	}
	var s presetSettings
	dec := json.NewDecoder(bytes.NewReader(c.Settings))
	if err := dec.Decode(&s); err != nil {
		return fmt.Errorf("channel.settings: %w", err)
	}
	switch {
	case !wire.IsChannelWhoPosts(s.WhoPosts):
		return fmt.Errorf("channel.settings.whoPosts is \"\" (everyone) or %q", wire.ChannelPostersAdmins)
	case !wire.IsChannelAgentParticipation(s.AgentParticipation):
		return errors.New("channel.settings.agentParticipation is one of the channel's participation values")
	case s.SlowModeSeconds < 0 || s.SlowModeSeconds > maxPresetSlowMode:
		return fmt.Errorf("channel.settings.slowModeSeconds is 0 (off) to %d", maxPresetSlowMode)
	case !presetPostingStyles[s.PostingStyle]:
		return errors.New("channel.settings.postingStyle is \"messages\" or \"threads\"")
	case len(s.Tags) > maxPresetTags:
		return fmt.Errorf("channel.settings.tags: at most %d", maxPresetTags)
	case len(s.Statuses) > maxPresetStatuses:
		return fmt.Errorf("channel.settings.statuses: at most %d", maxPresetStatuses)
	}
	ids := map[string]bool{}
	for i, t := range s.Tags {
		if err := checkPresetEntry(fmt.Sprintf("tags[%d]", i), t.ID, t.Name, ids); err != nil {
			return err
		}
		if err := checkText(fmt.Sprintf("channel.settings.tags[%d].color", i), t.Color, maxPresetColor, true); err != nil {
			return err
		}
	}
	ids = map[string]bool{}
	for i, st := range s.Statuses {
		if err := checkPresetEntry(fmt.Sprintf("statuses[%d]", i), st.ID, st.Name, ids); err != nil {
			return err
		}
	}
	return checkText("channel.settings.postTemplate", s.PostTemplate, maxPostTemplate, false)
}

// checkPresetEntry: a tag or status is an id (a name: lower-case letters,
// digits and hyphens, once per list) and a one-line name.
func checkPresetEntry(what, id, name string, seen map[string]bool) error {
	switch {
	case !ValidName(id) || len(id) > maxPresetTagID:
		return fmt.Errorf("channel.settings.%s.id %q: lower-case letters, digits and hyphens", what, id)
	case seen[id]:
		return fmt.Errorf("channel.settings.%s.id %q is listed twice", what, id)
	case strings.TrimSpace(name) == "":
		return fmt.Errorf("channel.settings.%s needs a name", what)
	}
	seen[id] = true
	return checkText("channel.settings."+what+".name", name, maxPresetTagName, true)
}

// channelPresetFromForm: a preset is its manifest (and the README the edited
// version carries).
func channelPresetFromForm(f Form, _ *Package, m *Manifest, _ map[string][]byte) error {
	if len(f.Skills) > 0 || f.Team != nil || len(f.Prompts) > 0 || len(f.QuickPrompts) > 0 || f.Welcome != "" || strings.TrimSpace(f.Instructions) != "" {
		return errors.New("a channel preset holds its channel's setup only (channel)")
	}
	if f.Channel == nil {
		return errors.New("a channel preset holds its channel's setup (channel)")
	}
	c := *f.Channel
	c.Purpose, c.Rules = strings.TrimSpace(c.Purpose), strings.TrimSpace(c.Rules)
	m.Channel = &c
	return nil
}
