package wire

import "encoding/json"

// Scheduled prompts (N2). realtime serves schedules (it owns chat publish
// and runs the scheduler). The JSON tags are the public contract the web
// client reads.

// MaxChatTextBytes is the product cap on one chat message's text. realtime
// enforces it on every post; schedule validation (a room package's
// schedules too) uses the same bound so a stored scheduled prompt always
// fits the post it becomes.
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
	// ScheduleRecipientNotice is a notice sent at set times, through the
	// notification emitter (the one an app's notify uses): where it goes, its
	// title and its body are the schedule's own, so a run needs no model to
	// say what to do. With Compose the model only writes the body.
	ScheduleRecipientNotice = "notice"
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
	// For kind "notice": To is where it goes, as an app's notify says it
	// (AppNotifyRoom, a list of logins, or "group:<name>"); Title is its title
	// and URL an optional link on this deployment. The schedule's text is its
	// body, in which {date}, {week} and the other NoticeVars names stand for the
	// run's own values. With Compose the text is instead what the model is asked
	// to write the body from each run (the destination and title stay fixed).
	To      json.RawMessage `json:"to,omitempty"`
	Title   string          `json:"title,omitempty"`
	URL     string          `json:"url,omitempty"`
	Compose bool            `json:"compose,omitempty"`
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
	FailedRuns  int64 `json:"failedRuns,omitempty"`
	LastSeq     int64 `json:"lastSeq,omitempty"`
	RunCount    int64 `json:"runCount"`
	CreatedAtMs int64 `json:"createdAtMs"`
	UpdatedAtMs int64 `json:"updatedAtMs"`
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
