package contentpkg

import (
	"github.com/luckylhb90/bnbloc/internal/tarbundle"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// Limit is one bound the validator enforces, named the way package
// authoring guidance states it.
type Limit struct {
	// Applies says which packages it bounds ("every package", "expert",
	// "skill pack", "prompt pack", "team expert", "work", "assist").
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
// constants its checks use. Guidance that states a limit renders it from
// here (the Expert builder's package-format skill does, and its test fails
// when the skill drifts), so no limit is written down twice.
func Limits() []Limit {
	const all, expert, skill, prompt, team, work, assist = "every package", "expert", "skill pack", "prompt pack", "team expert", "work", "assist"
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
		{expert, "quickPrompts", maxQuickPrompts, UnitItems},
		{expert, "quickPrompts[].title", maxQuickTitle, UnitChars},
		{expert, "quickPrompts[].prompt", maxQuickPrompt, UnitChars},
		{expert, "welcome", maxWelcome, UnitChars},
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
		{assist, "files in the package", AssistLimits.MaxFiles, UnitFiles},
		{assist, "package size", AssistLimits.MaxBytes, UnitBytes},
	}
}
