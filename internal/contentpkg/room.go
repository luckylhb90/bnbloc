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
// bound), its welcome and quick prompts, its channels by name, its
// scheduled tasks (AI tasks and notices to the room's members, created
// disabled on install), the room expert and the skill and prompt packs it
// turns on (requires), the MCP tools it needs (requires.mcp), its apps
// (works by "name@range", copied in like "Use this app") and the sources
// its tasks ask to read, each with its purpose (wire.RoomSourceAsk: the
// install records the ask for the new room, the organization's admin
// grants it). README.md is optional. A room package is no content use:
// nothing in it reaches an agent until it is installed as a room.

// Bounds of a room package.
const (
	// maxRoomDescription is a room's own bound (rooms.description): the
	// package's description becomes the room's.
	maxRoomDescription = 500
	maxRoomChannels    = 20
	maxRoomChannelName = 80
	maxRoomSchedules   = 20
	maxRoomApps        = 10
)

// RoomLimits is a room package's bundle profile: component.json and
// README.md.
var RoomLimits = tarbundle.Limits{MaxBytes: 1 << 20, MaxFiles: 2}

// defaultChannel is the channel every room has; a package names only the
// others (a schedule in it names no channel).
const defaultChannel = "general"

var errRoomFields = errors.New("channels, schedules, apps, sources and a room expert belong to a room package")

func init() {
	register(KindSpec{Kind: KindRoom, Short: "room", Limits: RoomLimits,
		validate: validateRoom, fromForm: roomFromForm})
}

// validateRoom: the room's description within a room's bound, a welcome
// and quick prompts (an expert's rules), channels, schedules, apps and
// sources, no scripts, and nothing but component.json and README.md.
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
	for _, f := range p.Bundle.Paths() {
		if f != ManifestFile && f != ReadmeFile {
			return fmt.Errorf("%s: a room package holds only %s and %s", f, ManifestFile, ReadmeFile)
		}
	}
	if _, ok := p.Bundle.File(ReadmeFile); ok {
		if _, err := readText(p.Bundle, ReadmeFile, maxInstructions); err != nil {
			return err
		}
	}
	if err := checkText("description", m.Description, maxRoomDescription, false); err != nil {
		return fmt.Errorf("%w (it becomes the room's description)", err)
	}
	if err := checkText("welcome", m.Welcome, maxWelcome, false); err != nil {
		return err
	}
	if len(m.QuickPrompts) > maxQuickPrompts {
		return fmt.Errorf("at most %d quick prompts", maxQuickPrompts)
	}
	for i, q := range m.QuickPrompts {
		if err := checkQuickPrompt(m, i, q); err != nil {
			return err
		}
	}
	channels, err := checkRoomChannels(m.Channels)
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
	return checkRoomSources(m.Sources)
}

// checkRoomChannels: names as they are shown (trimmed, one line, bounded),
// unique regardless of case, never the default channel. It returns the
// set of lower-cased names.
func checkRoomChannels(names []string) (map[string]bool, error) {
	if len(names) > maxRoomChannels {
		return nil, fmt.Errorf("at most %d channels", maxRoomChannels)
	}
	seen := map[string]bool{}
	for _, c := range names {
		key := strings.ToLower(c)
		switch {
		case c == "" || c != strings.TrimSpace(c):
			return nil, fmt.Errorf("channel %q: a name without surrounding spaces", c)
		case key == defaultChannel:
			return nil, fmt.Errorf("channel %q: every room has it; list only the others", c)
		case seen[key]:
			return nil, fmt.Errorf("channel %q is listed twice", c)
		}
		if err := checkText("channel "+c, c, maxRoomChannelName, true); err != nil {
			return nil, err
		}
		seen[key] = true
	}
	return seen, nil
}

// ScheduleMisfit is why a room's schedule is not one a package holds
// (RoomScheduleMisfit).
type ScheduleMisfit int

const (
	ScheduleFits ScheduleMisfit = iota
	// ScheduleAsPerson posts as a person or drives an agent: in a package
	// it would act as whoever installs it.
	ScheduleAsPerson
	// ScheduleToPeople is a notice to named people or a group: a package
	// names no people.
	ScheduleToPeople
	// ScheduleOnce runs once: a one-time run belongs to one room.
	ScheduleOnce
)

// RoomScheduleMisfit is the one rule of which schedules a room package
// holds: AI tasks and notices to the room's members that recur. The
// validator refuses the others and a room's draft leaves them out.
func RoomScheduleMisfit(r wire.ScheduleRecipient, rec wire.ScheduleRecurrence) ScheduleMisfit {
	var to string
	switch kind := strings.TrimSpace(r.Kind); {
	case kind != wire.ScheduleRecipientAI && kind != wire.ScheduleRecipientNotice:
		return ScheduleAsPerson
	case kind == wire.ScheduleRecipientNotice && (json.Unmarshal(r.To, &to) != nil || strings.TrimSpace(to) != wire.AppNotifyRoom):
		return ScheduleToPeople
	case strings.TrimSpace(rec.Kind) == wire.RecurOnce:
		return ScheduleOnce
	}
	return ScheduleFits
}

// checkRoomSchedule: a schedule a package holds (RoomScheduleMisfit), its
// recipient, its text by the schedule rule (schedule.CheckText), its
// recurrence (schedule.Validate) and a channel of the package.
func checkRoomSchedule(s wire.RoomPackageSchedule, channels map[string]bool) error {
	switch RoomScheduleMisfit(s.Recipient, s.Recurrence) {
	case ScheduleAsPerson:
		return errors.New("a room package's schedule is an AI task or a notice; one that posts as a person or drives an agent is not")
	case ScheduleToPeople:
		return fmt.Errorf("a notice in a room package goes to the room's members (to %q): a package names no people or group", wire.AppNotifyRoom)
	case ScheduleOnce:
		return errors.New("a room package's schedule recurs (daily, weekly or cron): a one-time run belongs to one room")
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

// checkRoomApps: works by "name@range", each named once.
func checkRoomApps(apps []string) error {
	if len(apps) > maxRoomApps {
		return fmt.Errorf("at most %d apps", maxRoomApps)
	}
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

// checkRoomSources: sources of the one list (wire.RoomSources), each once
// and with its purpose (the admin consents to it), within
// wire.MaxRoomSourcePurpose characters.
func checkRoomSources(sources []wire.RoomSourceAsk) error {
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

func roomFromForm(f Form, m *Manifest, files map[string][]byte) error {
	if strings.TrimSpace(f.Instructions) != "" || len(f.Skills) > 0 || f.Team != nil {
		return errors.New("instructions, skills and a team belong to experts and skill packs")
	}
	if len(f.Prompts) > 0 {
		return errors.New("prompts belong to prompt packs")
	}
	m.Welcome = strings.TrimSpace(f.Welcome)
	m.QuickPrompts = f.QuickPrompts
	for _, c := range f.Channels {
		m.Channels = append(m.Channels, strings.TrimSpace(c))
	}
	m.Schedules = f.Schedules
	m.Apps = f.Apps
	for _, src := range f.Sources {
		m.Sources = append(m.Sources, wire.RoomSourceAsk{Name: src.Name, Purpose: strings.TrimSpace(src.Purpose)})
	}
	return nil
}

// RoomSpec is a room manifest's setup as the API shows it (nil for any
// other kind).
func (m Manifest) RoomSpec() *wire.RoomSpec {
	if m.Kind != KindRoom {
		return nil
	}
	out := &wire.RoomSpec{Channels: m.Channels, Schedules: m.Schedules, Apps: m.Apps, Sources: m.Sources}
	if out.Channels == nil {
		out.Channels = []string{}
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
