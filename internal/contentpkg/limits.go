package contentpkg

import (
	"fmt"
	"strings"

	"github.com/luckylhb90/bnbloc/internal/tarbundle"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Limit is one bound the validator enforces, named the way package
// authoring guidance states it.
type Limit struct {
	// Applies says which packages it bounds ("every package", "expert",
	// "skill pack", "prompt pack", "team expert", "work", "assist", "room package").
	Applies string
	// Field is what is bounded: a manifest field, a file or the bundle
	// ("displayName", "prompts[].body", "INSTRUCTIONS.md").
	Field string
	// Max is the bound, in Unit.
	Max int
	// Unit is what Max counts.
	Unit LimitUnit
}

// LimitUnit is what a Limit counts.
type LimitUnit string

const (
	UnitChars LimitUnit = "characters"
	UnitBytes LimitUnit = "bytes"
	UnitItems LimitUnit = "entries"
	UnitFiles LimitUnit = "files"
)

// Limits is the one table of the validator's bounds, read from the same
// constants its checks use. Nothing states a limit by copying it: an agent
// reads them as LimitsDoc, the file every room's records hold
// (component.RoomContentLimitsFile), and content that guides authoring
// (the Expert builder) points there.
func Limits() []Limit {
	const all, expert, skill, prompt, team, work, assist, room = "every package", "expert", "skill pack", "prompt pack", "team expert", "work", "assist", "room package"
	return []Limit{
		{all, "name", maxName, UnitChars},
		{all, "displayName", maxDisplayName, UnitChars},
		{all, "description", maxDescription, UnitChars},
		{all, "category", maxCategory, UnitChars},
		{all, "tags", maxTags, UnitItems},
		{all, "tags[]", maxTag, UnitChars},
		{all, "icon", maxIcon, UnitChars},
		{all, "requires (all lists together)", maxRequires, UnitItems},
		{all, "files in the package", tarbundle.ContentLimits.MaxFiles, UnitFiles},
		{all, "package size", tarbundle.ContentLimits.MaxBytes, UnitBytes},
		{expert, InstructionsFile, maxInstructions, UnitBytes},
		{expert + ", " + room, "quickPrompts", maxQuickPrompts, UnitItems},
		{expert + ", " + room, "quickPrompts[].title", maxQuickTitle, UnitChars},
		{expert + ", " + room, "quickPrompts[].prompt", maxQuickPrompt, UnitChars},
		{expert + ", " + room, "welcome", maxWelcome, UnitChars},
		{expert + ", " + skill, "skills", maxSkillsInPack, UnitItems},
		{expert + ", " + skill, "SKILL.md", maxInstructions, UnitBytes},
		{expert + ", " + skill, "SKILL.md description", maxSkillDesc, UnitChars},
		{team, "team.lead", maxQuickTitle, UnitChars},
		{team, "team.members", maxTeamMembers, UnitItems},
		{team, "team.members[].description", maxRoleDesc, UnitChars},
		{prompt, "prompts", maxPromptsInPack, UnitItems},
		{prompt, "prompts[].title", maxPromptTitle, UnitChars},
		{prompt, "prompts[].body", maxPromptBody, UnitChars},
		{prompt, "prompts[].slash (after the /)", maxSlashCommand, UnitChars},
		{prompt, "prompts[].variables", maxPromptVars, UnitItems},
		{prompt, "variables[].name", maxVarName, UnitChars},
		{prompt, "variables[].label", maxVarLabel, UnitChars},
		{prompt, "variables[].default", maxVarDefault, UnitChars},
		{prompt, "variables[].options", maxVarOptions, UnitItems},
		{prompt, "variables[].options[]", maxVarOption, UnitChars},
		{prompt, "files in the package", PromptLimits.MaxFiles, UnitFiles},
		{prompt, "package size", PromptLimits.MaxBytes, UnitBytes},
		{work, "files in the package", WorkLimits.MaxFiles, UnitFiles},
		{work, "package size", WorkLimits.MaxBytes, UnitBytes},
		{work, "cover image", wire.MaxWorkCoverBytes, UnitBytes},
		{work, "recipe.references", wire.MaxWorkReferences, UnitItems},
		{work, "recipe reference files together", wire.MaxWorkReferenceBytes, UnitBytes},
		{work, "recipe.prompt", wire.MaxChatTextBytes, UnitBytes},
		{work, ReadmeFile, maxInstructions, UnitBytes},
		{assist, InstructionsFile, maxAssistInstructions, UnitBytes},
		{assist, "fields", maxAssistFields, UnitItems},
		{assist, "fields[].name", maxAssistFieldName, UnitChars},
		{assist, "fields[].description", maxAssistFieldDesc, UnitChars},
		{assist, "gate.question", maxAssistGateQuestion, UnitChars},
		{assist, "files in the package", AssistLimits.MaxFiles, UnitFiles},
		{assist, "package size", AssistLimits.MaxBytes, UnitBytes},
		{room, "description (the room's)", maxRoomDescription, UnitChars},
		{room, "sections", maxRoomSections, UnitItems},
		{room, "sections[].name", maxRoomChannelName, UnitChars},
		{room, "sections[].rules", wire.MaxChannelRules, UnitChars},
		{room, "channels", maxRoomChannels, UnitItems},
		{room, "channels[].name", maxRoomChannelName, UnitChars},
		{room, "channels[].purpose", wire.MaxChannelPurpose, UnitChars},
		{room, "channels[].rules", wire.MaxChannelRules, UnitChars},
		{room, "schedules", maxRoomSchedules, UnitItems},
		{room, "schedules[].text", wire.MaxChatTextBytes, UnitBytes},
		{room, "apps and appSnapshots together", MaxRoomApps, UnitItems},
		{room, "files", wire.MaxRoomFiles, UnitItems},
		{room, "files[].name", maxReferenceName, UnitChars},
		{room, "apps/ and files/ together", wire.MaxRoomCarryBytes, UnitBytes},
		{room, "sources[].purpose", wire.MaxRoomSourcePurpose, UnitChars},
		{room, InstructionsFile, maxInstructions, UnitBytes},
		{room, ReadmeFile, maxInstructions, UnitBytes},
		{room, "files in the package", RoomLimits.MaxFiles, UnitFiles},
		{room, "package size", RoomLimits.MaxBytes, UnitBytes},
	}
}

// LimitsDoc is Limits as the Markdown file an agent reads in its room's
// records (component.RoomContentLimitsFile): what a package may hold, as
// the validator checks it on save.
func LimitsDoc() string {
	var b strings.Builder
	b.WriteString("# Content package limits\n\n")
	b.WriteString("What bnbloc's validator allows in a content package (an expert, a skill pack, a prompt pack, an assist, a work, a room package); ")
	b.WriteString("a save over any of them is refused. Generated from the validator's own checks and kept current by bnbloc.\n\n")
	b.WriteString("| Applies to | Field | At most |\n|---|---|---|\n")
	for _, l := range Limits() {
		fmt.Fprintf(&b, "| %s | %s | %s |\n", l.Applies, l.Field, limitAmount(l))
	}
	return b.String()
}

// limitAmount states a limit's bound with its unit (bytes as KiB or MiB
// when whole).
func limitAmount(l Limit) string {
	if l.Unit == UnitBytes {
		switch {
		case l.Max%(1<<20) == 0:
			return fmt.Sprintf("%d MiB", l.Max>>20)
		case l.Max%(1<<10) == 0:
			return fmt.Sprintf("%d KiB", l.Max>>10)
		}
	}
	return fmt.Sprintf("%d %s", l.Max, l.Unit)
}
