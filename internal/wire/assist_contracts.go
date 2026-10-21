package wire

// Assist output contracts (docs/prd/2026-10-20-no-built-in-content.md
// §2.3): the platform ships no assist prompt, only the tasks its web and
// gateway call by name and what each must answer. An assist package saved
// or installed under one of these names must meet its contract
// (contentpkg's validator refuses it otherwise), so a replaced prompt may
// change the wording, never break the dialog that reads it. The prompts
// themselves are content (the default content repository, an
// organization's or a member's own).

// The tasks the web and the gateway run by name.
//
//wire:union AssistTaskName
const (
	AssistTaskEnhancePrompt    = "enhance-prompt"
	AssistTaskDescribeWork     = "describe-work"
	AssistTaskDescribeRoom     = "describe-room"
	AssistTaskDraftExpert      = "draft-expert"
	AssistTaskDraftSkillPack   = "draft-skill-pack"
	AssistTaskDraftPromptPack  = "draft-prompt-pack"
	AssistTaskPlanRoom         = "plan-room"
	AssistTaskDraftSchedule    = "draft-schedule"
	AssistTaskExplainSourceUse = "explain-source-use"
	AssistTaskReviewSkill      = "review-skill"
	AssistTaskFillSettings     = "fill-settings"
	AssistTaskSuggestSkillDirs = "suggest-skill-dirs"
	AssistTaskFindAWay         = "find-a-way"
)

// AssistContract is what an assist named Task must answer: its output, the
// fields of a JSON reply the caller reads (a package may declare more,
// never fewer, and each in the contract's type), whether it must have a
// gate (a caller that runs it gated), and whether it explains a consent.
type AssistContract struct {
	Task   string                `json:"task" tstype:"AssistTaskName"`
	Output string                `json:"output" tstype:"AssistOutput"`
	Fields []AssistContractField `json:"fields,omitempty"`
	// Gate: the caller runs it gated (AssistRequest.Gated), so a package
	// of this name declares a gate.
	Gate bool `json:"gate,omitempty"`
	// Consent: the gateway runs it to explain a decision only an
	// organization admin makes (a room's source grant), so it resolves to
	// the organization's version, else the platform's, never a member's
	// own or a shared copy (internal/assist, ConsentTask).
	Consent bool `json:"consent,omitempty"`
}

// AssistContractField is one field of a contract: its name and type
// (AssistFieldTypes, empty = text), an items field's item fields, and
// Values, the closed list the caller acts on (any other value is read as
// the caller's fallback): the instructions should offer exactly these.
type AssistContractField struct {
	Name   string                `json:"name"`
	Type   string                `json:"type,omitempty" tstype:"AssistFieldType"`
	Item   []AssistContractField `json:"item,omitempty"`
	Values []string              `json:"values,omitempty"`
}

// AssistContracts are the contracts, one per task name.
var AssistContracts = []AssistContract{
	{Task: AssistTaskEnhancePrompt, Output: AssistOutputText},
	{Task: AssistTaskDescribeWork, Output: AssistOutputJSON, Fields: []AssistContractField{
		{Name: "displayName"}, {Name: "description"},
		{Name: "category", Values: []string{WorkCategoryOffice, WorkCategoryData, WorkCategoryProductivity, WorkCategoryContent, WorkCategoryLearning,
			WorkCategoryEngineering, WorkCategoryFinance, WorkCategoryLife, WorkCategoryOther}},
		{Name: "tags", Type: AssistFieldList},
	}},
	{Task: AssistTaskDescribeRoom, Output: AssistOutputJSON, Fields: []AssistContractField{
		{Name: "displayName"}, {Name: "description"}, {Name: "category"}, {Name: "tags", Type: AssistFieldList},
		{Name: "welcome"}, {Name: "instructions"},
		{Name: "dependsOn", Type: AssistFieldItems, Item: []AssistContractField{
			{Name: "what"},
			{Name: "kind", Values: []string{"instructions", "app", "file", "tool", "schedule", "agent", "data", "other"}},
			{Name: "included", Values: []string{"yes", "no"}},
			{Name: "why"},
		}},
		// A finding's why starts with its kind and a colon.
		{Name: "findings", Type: AssistFieldItems, Item: []AssistContractField{
			{Name: "field"}, {Name: "quote"},
			{Name: "why", Values: []string{"secret", "internal-link", "personal-name", "private-data"}},
		}},
	}},
	{Task: AssistTaskDraftExpert, Output: AssistOutputJSON, Fields: []AssistContractField{
		{Name: "displayName"}, {Name: "description"}, {Name: "instructions"}, {Name: "welcome"},
		{Name: "quickPrompts", Type: AssistFieldItems, Item: []AssistContractField{{Name: "title"}, {Name: "prompt"}}},
		{Name: "tags", Type: AssistFieldList},
	}},
	{Task: AssistTaskDraftSkillPack, Output: AssistOutputJSON, Fields: []AssistContractField{
		{Name: "displayName"}, {Name: "description"},
		{Name: "skills", Type: AssistFieldItems, Item: []AssistContractField{{Name: "name"}, {Name: "description"}, {Name: "body"}}},
		{Name: "tags", Type: AssistFieldList},
	}},
	{Task: AssistTaskDraftPromptPack, Output: AssistOutputJSON, Fields: []AssistContractField{
		{Name: "displayName"}, {Name: "description"},
		{Name: "prompts", Type: AssistFieldItems, Item: []AssistContractField{{Name: "title"}, {Name: "body"}}},
		{Name: "tags", Type: AssistFieldList},
	}},
	{Task: AssistTaskPlanRoom, Output: AssistOutputJSON, Fields: []AssistContractField{
		{Name: "roomId"}, {Name: "name"}, {Name: "description"}, {Name: "expert"}, {Name: "skills", Type: AssistFieldList}, {Name: "firstMessage"},
	}},
	{Task: AssistTaskDraftSchedule, Output: AssistOutputJSON, Fields: []AssistContractField{
		{Name: "text"},
		{Name: "kind", Values: []string{RecurOnce, RecurDaily, RecurWeekly, RecurCron}},
		{Name: "time"}, {Name: "days", Type: AssistFieldList}, {Name: "cron"}, {Name: "at"},
	}},
	{Task: AssistTaskExplainSourceUse, Output: AssistOutputJSON, Consent: true, Fields: []AssistContractField{
		{Name: "summary"}, {Name: "uses", Type: AssistFieldList}, {Name: "mismatch"},
	}},
	{Task: AssistTaskReviewSkill, Output: AssistOutputJSON, Fields: []AssistContractField{
		{Name: "findings", Type: AssistFieldList}, {Name: "name"}, {Name: "displayName"}, {Name: "description"}, {Name: "tags", Type: AssistFieldList},
	}},
	{Task: AssistTaskFillSettings, Output: AssistOutputJSON, Fields: []AssistContractField{
		{Name: "values", Type: AssistFieldItems, Item: []AssistContractField{{Name: "key"}, {Name: "value"}}},
		{Name: "notes"},
	}},
	{Task: AssistTaskSuggestSkillDirs, Output: AssistOutputJSON, Fields: []AssistContractField{
		{Name: "add", Type: AssistFieldItems, Item: []AssistContractField{{Name: "path"}, {Name: "why"}}},
		{Name: "notes"},
	}},
	{Task: AssistTaskFindAWay, Output: AssistOutputJSON, Gate: true, Fields: []AssistContractField{
		{Name: "cause", Values: []string{"access", "missing-file", "capability", "unclear", "agent-error", "other"}},
		{Name: "summary"},
		{Name: "fixes", Type: AssistFieldItems, Item: []AssistContractField{
			{Name: "action", Values: []string{"install", "ask-agent", "attach", "own-agent", "rephrase", "report", "allow-host"}},
			{Name: "target"}, {Name: "why"},
		}},
	}},
}

// AssistContractOf is the contract of a task name (ok false: a task no
// caller runs by name, free in its shape).
func AssistContractOf(task string) (AssistContract, bool) {
	for _, c := range AssistContracts {
		if c.Task == task {
			return c, true
		}
	}
	return AssistContract{}, false
}
