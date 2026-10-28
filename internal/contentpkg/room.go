package contentpkg

import (
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/schedule"
	"github.com/luckylhb90/bnbloc/internal/semver"
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Room packages (docs/prd/2026-10-20-rooms-as-components.md §2.1): a
// room's setup, saved from a room and installed as a new room its
// installer owns (the gateway's room_components.go). component.json holds
// all of it: the room's description (the envelope's, within a room's
// bound), its welcome and quick prompts, its sections and channels (each
// channel with its purpose, rules, section and settings: the layout the
// new room starts with, docs/prd/2026-10-28-channels-and-threads.md C25), its
// scheduled tasks (AI tasks, notices to the room's members and requests to
// an agent without the agent, created disabled on install; a request waits
// for the installer to pick one of their agents), the room expert and the skill and prompt packs it
// turns on (requires), the MCP tools it needs (requires.mcp), its apps
// (works by "name@range", copied in like "Use this app") and the sources
// its tasks ask to read, each with its purpose (wire.RoomSourceAsk: the
// install records the ask for the new room, the organization's admin
// grants it). README.md is optional. Beside the manifest a package may
// carry the room's instructions, apps made in it and room files
// (room_carry.go). A room package is no content use: nothing in it
// reaches an agent until it is installed as a room.

// Bounds of a room package.
const (
	// maxRoomDescription is a room's own bound (rooms.description): the
	// package's description becomes the room's.
	maxRoomDescription = 500
	maxRoomChannels    = 20
	// maxRoomSections bounds a package's sections, and maxRoomChannelName
	// a channel's and a section's name (decision: pending owner; the room
	// service bounds neither).
	maxRoomSections    = 10
	maxRoomChannelName = 80
	maxRoomSchedules   = 20
	// MaxRoomApps bounds a package's apps: works and apps made in the room
	// together (a save's include is held to it before it reads a site).
	MaxRoomApps = 10
)

// RoomLimits is a room package's bundle profile: component.json,
// README.md and INSTRUCTIONS.md (within maxRoomText), and the apps' sites
// and the files it carries (within wire.MaxRoomCarryBytes and a site
// snapshot's file count, plus wire.MaxRoomFiles).
var RoomLimits = tarbundle.Limits{MaxBytes: wire.MaxRoomCarryBytes + maxRoomText, MaxFiles: tarbundle.SiteLimits.MaxFiles + wire.MaxRoomFiles + 3}

// maxRoomText is the room a package's manifest, README.md and
// INSTRUCTIONS.md take beyond what it carries.
const maxRoomText = 1 << 20

// defaultChannel is the channel every room has; a package names only the
// others (a schedule in it names no channel).
const defaultChannel = "general"

var errRoomFields = errors.New("channels, schedules, apps, files, sources and a room expert belong to a room package")

// CheckRoomDescription checks a room's description (a room's own, the room
// service's, and a room package's, which becomes it): within the room's
// bound.
func CheckRoomDescription(description string) error {
	return checkText("description", description, maxRoomDescription, false)
}

func init() {
	register(KindSpec{Kind: KindRoom, Short: "room", Limits: RoomLimits, Snapshot: true,
		validate: validateRoom, fromForm: roomFromForm})
}

// validateRoom: the room's description within a room's bound, a welcome
// and quick prompts (an expert's rules), channels, schedules, apps and
// sources, no scripts permission, and beside component.json and README.md
// only what it carries (checkRoomCarry).
func validateRoom(p *Package) error {
	m := p.Manifest
	switch {
	case len(m.Skills) > 0:
		return errors.New("a room package lists no skills field; require skill packs (requires.skills)")
	case len(m.Prompts) > 0:
		return errors.New("prompts belong to prompt packs; require them (requires.prompts)")
	case m.declares(PermScripts):
		return errors.New("a room package ships no scripts")
	}
	if err := checkRoomCarry(p); err != nil {
		return err
	}
	if _, ok := p.Bundle.File(ReadmeFile); ok {
		if _, err := readText(p.Bundle, ReadmeFile, maxInstructions); err != nil {
			return err
		}
	}
	if err := CheckRoomDescription(m.Description); err != nil {
		return fmt.Errorf("%w (it becomes the room's description)", err)
	}
	if err := checkStart(m); err != nil {
		return err
	}
	sections, err := checkRoomSections(m.Sections)
	if err != nil {
		return err
	}
	channels, err := checkRoomChannels(m.Channels, sections)
	if err != nil {
		return err
	}
	if len(m.Schedules) > maxRoomSchedules {
		return fmt.Errorf("at most %d schedules", maxRoomSchedules)
	}
	for i, s := range m.Schedules {
		if err := checkRoomSchedule(s, channels); err != nil {
			return fmt.Errorf("schedule %d: %w", i+1, err)
		}
	}
	if err := checkRoomApps(m.Apps); err != nil {
		return err
	}
	return CheckRoomSources(m.Sources)
}

// checkRoomName: a channel's or section's name as it is shown (trimmed,
// one line, bounded), unique in its list regardless of case. It returns
// the lower-cased name.
func checkRoomName(what, name string, seen map[string]bool) (string, error) {
	key := strings.ToLower(name)
	switch {
	case name == "" || name != strings.TrimSpace(name):
		return "", fmt.Errorf("%s %q: a name without surrounding spaces", what, name)
	case seen[key]:
		return "", fmt.Errorf("%s %q is listed twice", what, name)
	}
	if err := checkText(what+" "+name, name, maxRoomChannelName, true); err != nil {
		return "", err
	}
	return key, nil
}

// checkRoomSections: open sections (C25), each named once, its rules
// within a channel's rules bound (wire.MaxChannelRules, the room service's
// for a section). It returns the set of lower-cased names.
func checkRoomSections(sections []wire.RoomPackageSection) (map[string]bool, error) {
	if len(sections) > maxRoomSections {
		return nil, fmt.Errorf("at most %d sections", maxRoomSections)
	}
	seen := map[string]bool{}
	for _, s := range sections {
		key, err := checkRoomName("section", s.Name, seen)
		if err != nil {
			return nil, err
		}
		if err := checkText("section "+s.Name+" rules", s.Rules, wire.MaxChannelRules, false); err != nil {
			return nil, err
		}
		seen[key] = true
	}
	return seen, nil
}

// checkRoomChannels: each channel named once, never the default channel,
// its purpose and rules within the room service's bounds
// (wire.MaxChannelPurpose, wire.MaxChannelRules), in a section the
// package makes ("" the top level), with settings the room service takes
// (wire.IsChannelWhoPosts, slow mode not negative). It returns the set of
// lower-cased names.
func checkRoomChannels(channels []wire.RoomPackageChannel, sections map[string]bool) (map[string]bool, error) {
	if len(channels) > maxRoomChannels {
		return nil, fmt.Errorf("at most %d channels", maxRoomChannels)
	}
	seen := map[string]bool{}
	for _, c := range channels {
		key, err := checkRoomName("channel", c.Name, seen)
		if err != nil {
			return nil, err
		}
		if key == defaultChannel {
			return nil, fmt.Errorf("channel %q: every room has it; list only the others", c.Name)
		}
		if err := checkText("channel "+c.Name+" purpose", c.Purpose, wire.MaxChannelPurpose, true); err != nil {
			return nil, err
		}
		if err := checkText("channel "+c.Name+" rules", c.Rules, wire.MaxChannelRules, false); err != nil {
			return nil, err
		}
		if c.Section != "" && !sections[strings.ToLower(c.Section)] {
			return nil, fmt.Errorf("channel %q: section %q is not one of the package's sections", c.Name, c.Section)
		}
		if !wire.IsChannelWhoPosts(c.Settings.WhoPosts) {
			return nil, fmt.Errorf("channel %q: settings.whoPosts is \"\" (everyone) or %q", c.Name, wire.ChannelPostersAdmins)
		}
		if c.Settings.SlowModeSeconds < 0 {
			return nil, fmt.Errorf("channel %q: settings.slowModeSeconds is not negative (0 is off)", c.Name)
		}
		seen[key] = true
	}
	return seen, nil
}

// cleanRoomLayout trims the layout a form wrote (names, texts, a channel's
// section), as the room service trims what it stores.
func cleanRoomLayout(sections []wire.RoomPackageSection, channels []wire.RoomPackageChannel) ([]wire.RoomPackageSection, []wire.RoomPackageChannel) {
	var outS []wire.RoomPackageSection
	for _, s := range sections {
		outS = append(outS, wire.RoomPackageSection{Name: strings.TrimSpace(s.Name), Rules: strings.TrimSpace(s.Rules)})
	}
	var outC []wire.RoomPackageChannel
	for _, c := range channels {
		outC = append(outC, wire.RoomPackageChannel{Name: strings.TrimSpace(c.Name), Purpose: strings.TrimSpace(c.Purpose), Rules: strings.TrimSpace(c.Rules),
			Section: strings.TrimSpace(c.Section), Settings: c.Settings})
	}
	return outS, outC
}

// ScheduleMisfit is why a room's schedule is not one a package holds
// (RoomScheduleMisfit).
type ScheduleMisfit int

const (
	ScheduleFits ScheduleMisfit = iota
	// ScheduleAsPerson posts as a person: in a package it would post as
	// whoever installs it, words they never wrote.
	ScheduleAsPerson
	// ScheduleToPeople is a notice to named people or a group: a package
	// names no people.
	ScheduleToPeople
	// ScheduleOnce runs once: a one-time run belongs to one room.
	ScheduleOnce
)

// RoomScheduleMisfit is the one rule of which schedules a room package
// holds: AI tasks, notices to the room's members and requests to an agent
// that recur. The validator refuses the others and a room's draft leaves
// them out. A request to an agent is held without its agent
// (PackageRecipient): the installer picks one of theirs.
func RoomScheduleMisfit(r wire.ScheduleRecipient, rec wire.ScheduleRecurrence) ScheduleMisfit {
	var to string
	switch kind := strings.TrimSpace(r.Kind); {
	case kind != wire.ScheduleRecipientAI && kind != wire.ScheduleRecipientNotice && kind != wire.ScheduleRecipientAgent:
		return ScheduleAsPerson
	case kind == wire.ScheduleRecipientNotice && (json.Unmarshal(r.To, &to) != nil || strings.TrimSpace(to) != wire.AppNotifyRoom):
		return ScheduleToPeople
	case strings.TrimSpace(rec.Kind) == wire.RecurOnce:
		return ScheduleOnce
	}
	return ScheduleFits
}

// PackageRecipient is a fitting schedule's recipient as a package holds
// it: a request to an agent without its agent (a package names no people
// or their agents; it waits for the installer's pick,
// schedule.WaitsForAgent), any other unchanged.
func PackageRecipient(r wire.ScheduleRecipient) wire.ScheduleRecipient {
	if strings.TrimSpace(r.Kind) == wire.ScheduleRecipientAgent {
		return wire.ScheduleRecipient{Kind: wire.ScheduleRecipientAgent}
	}
	return r
}

// checkRoomSchedule: a schedule a package holds (RoomScheduleMisfit), its
// recipient (a request to an agent names none, PackageRecipient), its
// text by the schedule rule (schedule.CheckText), its recurrence
// (schedule.Validate) and a channel of the package.
func checkRoomSchedule(s wire.RoomPackageSchedule, channels map[string]bool) error {
	switch RoomScheduleMisfit(s.Recipient, s.Recurrence) {
	case ScheduleAsPerson:
		return errors.New("a room package's schedule is an AI task, a notice or a request to an agent; one that posts as a person is not")
	case ScheduleToPeople:
		return fmt.Errorf("a notice in a room package goes to the room's members (to %q): a package names no people or group", wire.AppNotifyRoom)
	case ScheduleOnce:
		return errors.New("a room package's schedule recurs (daily, weekly or cron): a one-time run belongs to one room")
	}
	if strings.TrimSpace(s.Recipient.Kind) == wire.ScheduleRecipientAgent && !schedule.WaitsForAgent(s.Recipient) {
		return errors.New("a request to an agent in a room package names no agent (no ownerId, provider or core): whoever installs it picks one of theirs")
	}
	rcpt, err := schedule.NormalizeRecipient(s.Recipient)
	if err != nil {
		return err
	}
	if _, err := schedule.CheckText(rcpt, s.Text); err != nil {
		return err
	}
	if _, _, err := schedule.Validate(s.Recurrence, s.Timezone); err != nil {
		return err
	}
	if s.Channel != "" && !channels[strings.ToLower(s.Channel)] {
		return fmt.Errorf("channel %q is not one of the package's channels (none is the default channel)", s.Channel)
	}
	return nil
}

// checkRoomApps: works by "name@range", each named once (their count, with
// the apps made in the room, is checkRoomCarry's).
func checkRoomApps(apps []string) error {
	seen := map[string]bool{}
	for _, a := range apps {
		if err := checkReference("apps", a); err != nil {
			return err
		}
		ref, _ := ParseReference(a)
		if seen[ref.Name] {
			return fmt.Errorf("apps: %q is named twice", ref.Name)
		}
		seen[ref.Name] = true
	}
	return nil
}

// CheckRoomSources: sources of the one list (wire.RoomSources), each once
// and with its purpose (the admin consents to it), within
// wire.MaxRoomSourcePurpose characters. A package's asks, and an ask made
// in a room itself (the gateway's room_sources.go), are checked by it.
func CheckRoomSources(sources []wire.RoomSourceAsk) error {
	seen := map[string]bool{}
	for _, s := range sources {
		if !slices.Contains(wire.RoomSources, s.Name) {
			return fmt.Errorf("source %q: one of %s", s.Name, strings.Join(wire.RoomSources, ", "))
		}
		if seen[s.Name] {
			return fmt.Errorf("source %q is named twice", s.Name)
		}
		seen[s.Name] = true
		if strings.TrimSpace(s.Purpose) == "" {
			return fmt.Errorf("source %q: say its purpose (what the room's tasks read it for); the organization's admin consents to it", s.Name)
		}
		if err := checkText("source "+s.Name+" purpose", s.Purpose, wire.MaxRoomSourcePurpose, false); err != nil {
			return err
		}
	}
	return nil
}

func roomFromForm(f Form, base *Package, m *Manifest, files map[string][]byte) error {
	if len(f.Skills) > 0 || f.Team != nil {
		return errors.New("skills and a team belong to experts and skill packs")
	}
	if len(f.Prompts) > 0 {
		return errors.New("prompts belong to prompt packs")
	}
	m.Welcome = strings.TrimSpace(f.Welcome)
	m.QuickPrompts = f.QuickPrompts
	m.Sections, m.Channels = cleanRoomLayout(f.Sections, f.Channels)
	m.Schedules = f.Schedules
	m.Apps = f.Apps
	for _, src := range f.Sources {
		m.Sources = append(m.Sources, wire.RoomSourceAsk{Name: src.Name, Purpose: strings.TrimSpace(src.Purpose)})
	}
	return layoutRoomCarry(f, base, m, files)
}

// RoomSpec is a room manifest's setup as the API shows it (nil for any
// other kind).
func (m Manifest) RoomSpec() *wire.RoomSpec {
	if m.Kind != KindRoom {
		return nil
	}
	out := &wire.RoomSpec{Sections: m.Sections, Channels: m.Channels, Schedules: m.Schedules, Apps: m.Apps, AppSnapshots: m.AppSnapshots, Files: m.Files, Sources: m.Sources}
	if out.AppSnapshots == nil {
		out.AppSnapshots = []wire.RoomAppSnapshot{}
	}
	if out.Files == nil {
		out.Files = []wire.RoomPackageFile{}
	}
	if out.Sections == nil {
		out.Sections = []wire.RoomPackageSection{}
	}
	if out.Channels == nil {
		out.Channels = []wire.RoomPackageChannel{}
	}
	if out.Schedules == nil {
		out.Schedules = []wire.RoomPackageSchedule{}
	}
	if out.Apps == nil {
		out.Apps = []string{}
	}
	if out.Sources == nil {
		out.Sources = []wire.RoomSourceAsk{}
	}
	return out
}

// MajorRef is the reference a package names a component by when it was
// picked at version: "name@^major" (any later release of that major),
// or the bare name when version does not parse.
func MajorRef(name, version string) string {
	v, err := semver.Parse(version)
	if err != nil {
		return name
	}
	return fmt.Sprintf("%s@^%d", name, v.Major)
}
