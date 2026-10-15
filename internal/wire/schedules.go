package wire

// Scheduled prompts (N2) and room templates (N3). realtime serves schedules
// (it owns chat publish and runs the scheduler); room serves templates (it
// owns rooms, members, and channels). The JSON tags are the public contract
// the web client reads.

// MaxChatTextBytes is the product cap on one chat message's text. realtime
// enforces it on every post; schedule and template validation use the same
// bound so a stored scheduled prompt always fits the post it becomes.
const MaxChatTextBytes = 16 * 1024

// Schedule recipient kinds: a people-only room message, a message
// addressed to one member's agent (the bridge serving that agent claims it,
// and enforces borrow authorization at drive time exactly as for a typed
// message), or a task in plain words the assist model carries out with the
// room's actions, as the schedule's owner (docs/prd/2026-10-15-app-
// identity-schedules-notify-ai.md §4).
//
//wire:union ScheduleRecipientKind
const (
	ScheduleRecipientRoom  = "room"
	ScheduleRecipientAgent = "agent"
	ScheduleRecipientAI    = "ai"
)

// Schedule recurrence kinds.
//
//wire:union RecurrenceKind
const (
	// RecurOnce runs once at AtMs.
	RecurOnce = "once"
	// RecurDaily runs every day at Time (HH:MM) in the schedule's timezone.
	RecurDaily = "daily"
	// RecurWeekly runs on Days (0=Sunday..6=Saturday) at Time.
	RecurWeekly = "weekly"
	// RecurCron runs on a five-field cron-lite expression
	// ("minute hour day-of-month month day-of-week").
	RecurCron = "cron"
)

// Schedule run outcomes recorded on the schedule (LastStatus).
//
//wire:union ScheduleRunStatus
const (
	ScheduleRunOK      = "ok"
	ScheduleRunError   = "error"
	ScheduleRunSkipped = "skipped"
)

// ScheduleRecipient addresses a scheduled message. Kind "room" drives no
// agent; kind "agent" carries the target agent's owner + provider (+ core
// when the owner exposes more than one).
type ScheduleRecipient struct {
	Kind     string `json:"kind" tstype:"ScheduleRecipientKind"`
	OwnerID  string `json:"ownerId,omitempty"`
	Provider string `json:"provider,omitempty"`
	Core     string `json:"core,omitempty"`
}

// ScheduleRecurrence is when a schedule fires. Only the fields of its Kind
// are meaningful: AtMs (once), Time (daily, weekly), Days (weekly), Cron
// (cron).
type ScheduleRecurrence struct {
	Kind string `json:"kind" tstype:"RecurrenceKind"`
	AtMs int64  `json:"atMs,omitempty"`
	Time string `json:"time,omitempty"`
	Days []int  `json:"days,omitempty"`
	Cron string `json:"cron,omitempty"`
}

// RoomSchedule is one scheduled prompt: Text posted into the room as
// CreatedBy, addressed to Recipient, whenever Recurrence fires in Timezone.
// NextRunAtMs is 0 when the schedule will not fire (paused, or a one-shot
// that already ran).
type RoomSchedule struct {
	ScheduleID  string             `json:"scheduleId"`
	TenantID    string             `json:"tenantId"`
	RoomID      string             `json:"roomId"`
	ChannelID   string             `json:"channelId,omitempty"`
	CreatedBy   string             `json:"createdBy"`
	Text        string             `json:"text"`
	Recipient   ScheduleRecipient  `json:"recipient"`
	Recurrence  ScheduleRecurrence `json:"recurrence"`
	Timezone    string             `json:"timezone"`
	Enabled     bool               `json:"enabled"`
	NextRunAtMs int64              `json:"nextRunAtMs,omitempty"`
	LastRunAtMs int64              `json:"lastRunAtMs,omitempty"`
	LastStatus  string             `json:"lastStatus,omitempty" tstype:"ScheduleRunStatus"`
	LastError   string             `json:"lastError,omitempty"`
	// LastSummary is what an ai schedule's last run did, in a line.
	LastSummary string `json:"lastSummary,omitempty"`
	// FailedRuns is how many runs in a row failed (0 after a run that
	// did not).
	FailedRuns int64 `json:"failedRuns,omitempty"`
	LastSeq     int64              `json:"lastSeq,omitempty"`
	RunCount    int64              `json:"runCount"`
	CreatedAtMs int64              `json:"createdAtMs"`
	UpdatedAtMs int64              `json:"updatedAtMs"`
}

// ScheduleRunRequest is realtime's call to the gateway to run an ai
// schedule's slot (POST /internal/v1/schedule-runs): the schedule as
// stored, the slot it runs (the idempotency of what it sends), and DryRun:
// read for real, send nothing, report what it would do.
type ScheduleRunRequest struct {
	Schedule RoomSchedule `json:"schedule"`
	SlotMs   int64        `json:"slotMs"`
	DryRun   bool         `json:"dryRun,omitempty"`
}

// ScheduleRunReply is what an ai run did: its outcome (ScheduleRunStatus),
// its summary (the model's last words), an error a person reads, and the
// actions it took (or, dry, would take).
type ScheduleRunReply struct {
	Status  string           `json:"status" tstype:"ScheduleRunStatus"`
	Summary string           `json:"summary,omitempty"`
	Error   string           `json:"error,omitempty"`
	Actions []ScheduleAction `json:"actions"`
	// Seq is the last message the run posted (0: none).
	Seq int64 `json:"seq,omitempty"`
}

// ScheduleAction is one action of a run: its name and what it did, in a
// line; DryRun when it was only described.
type ScheduleAction struct {
	Name   string `json:"name"`
	Detail string `json:"detail"`
	DryRun bool   `json:"dryRun,omitempty"`
}

// ScheduledMarker is set as payload.scheduled on a chat_message the scheduler
// posted, so clients can badge it. A typed message never carries it.
type ScheduledMarker struct {
	ScheduleID string `json:"scheduleId"`
}

// Room template visibility values: private is the author only, tenant is
// every member of the author's tenant.
//
//wire:union TemplateVisibility
const (
	TemplatePrivate = "private"
	TemplateTenant  = "tenant"
)

// TemplateAgent is a suggested agent of a template: a HINT only. After a
// room is created from the template the UI offers to invite the owner and
// to request that owner's borrow authorization; nothing is ever granted
// automatically.
type TemplateAgent struct {
	OwnerID  string `json:"ownerId"`
	Provider string `json:"provider"`
	Core     string `json:"core,omitempty"`
}

// TemplateSchedule is a schedule captured in a template. Channel is a channel
// NAME of the template (ids are per room); empty means the default channel.
// Instantiation creates it disabled, owned by the member who instantiated.
type TemplateSchedule struct {
	Channel    string             `json:"channel,omitempty"`
	Text       string             `json:"text"`
	Recipient  ScheduleRecipient  `json:"recipient"`
	Recurrence ScheduleRecurrence `json:"recurrence"`
	Timezone   string             `json:"timezone"`
}

// TemplateContentUse is a content component a template turns on in every
// room made from it: a room content use, pinned to Version or following the
// latest when empty.
type TemplateContentUse struct {
	ComponentID string `json:"componentId"`
	Version     string `json:"version,omitempty"`
}

// RoomTemplateBody is what a template sets up in a new room. A template's
// starters are its prompt packs' (PromptPacks become the new room's prompt
// pack uses); it has no starter list of its own.
type RoomTemplateBody struct {
	RoomDescription string               `json:"roomDescription,omitempty"`
	Channels        []string             `json:"channels"`
	PromptPacks     []TemplateContentUse `json:"promptPacks"`
	SuggestedAgents []TemplateAgent      `json:"suggestedAgents"`
	DefaultMembers  []string             `json:"defaultMembers"`
	Schedules       []TemplateSchedule   `json:"schedules"`
}

// RoomTemplate is one template in the tenant's template library.
type RoomTemplate struct {
	TemplateID  string           `json:"templateId"`
	TenantID    string           `json:"tenantId"`
	Name        string           `json:"name"`
	Description string           `json:"description,omitempty"`
	Visibility  string           `json:"visibility" tstype:"TemplateVisibility"`
	CreatedBy   string           `json:"createdBy"`
	Body        RoomTemplateBody `json:"body"`
	UseCount    int64            `json:"useCount"`
	CreatedAtMs int64            `json:"createdAtMs"`
	UpdatedAtMs int64            `json:"updatedAtMs"`
}

// RoomTemplateInstance is the instantiate response: the new room and what
// the template set up in it. SuggestedAgents are returned for the UI's
// follow-up offers; no grant is created for any agent. PromptPacks are the
// component ids turned on in the room; FailedPromptPacks the template's
// packs the creator may not use, left out.
type RoomTemplateInstance struct {
	RoomID            string          `json:"roomId"`
	Name              string          `json:"name"`
	TemplateID        string          `json:"templateId"`
	Channels          []string        `json:"channels"`
	AddedMembers      []string        `json:"addedMembers"`
	FailedMembers     []string        `json:"failedMembers,omitempty"`
	Schedules         []string        `json:"schedules"`
	PromptPacks       []string        `json:"promptPacks"`
	FailedPromptPacks []string        `json:"failedPromptPacks,omitempty"`
	SuggestedAgents   []TemplateAgent `json:"suggestedAgents"`
}
