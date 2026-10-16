package roomtools

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"time"

	"github.com/luckylhb90/bnbloc/internal/roomlog"
	"github.com/luckylhb90/bnbloc/internal/wire"
)

// schedulesAuthor names whose data a schedule's task text is: whoever wrote it.
const schedulesAuthor = "a schedule's author"

func schedulesTool() Tool {
	return Tool{
		Name:  "schedules",
		Title: "The room's schedules",
		Description: "List the room's schedules (with the group chats its members connected), create one, or delete one of the person's own. " +
			"A schedule is an AI task in plain words that runs on time as the person who asked you, with their rights and their organization's assist budget: " +
			"it can read the room's apps' data, notify the room's members or a connected group chat, and post in the room. " +
			"Create one only after the person confirmed what it does, when (and in which time zone) and where it sends; then tell them what you set and when it first runs. " +
			"They can pause or delete it in the room's Schedules. A person may own 20 schedules in a room, a room holds 50. " +
			"Recurrence: kind weekly (days 0=Sunday … 6=Saturday, time HH:MM), daily (time), once (at, RFC 3339) or cron (five fields). " +
			"Only AI tasks: a schedule that posts as the person or drives an agent is theirs to create in the room's Schedules.",
		InputSchema: objectSchema(map[string]any{
			"action": map[string]any{"type": "string", "enum": []string{"list", "create", "delete"}, "description": "What to do."},
			"task":   map[string]any{"type": "string", "description": "For create: what to do each time, in plain words, e.g. \"notify the weekly report group who has not filled in this week's report\"."},
			"recurrence": map[string]any{
				"type": "object", "description": "For create: when.",
				"properties": map[string]any{
					"kind": map[string]any{"type": "string", "enum": []string{wire.RecurOnce, wire.RecurDaily, wire.RecurWeekly, wire.RecurCron}},
					"time": map[string]any{"type": "string", "description": "HH:MM, for daily and weekly."},
					"days": map[string]any{"type": "array", "items": map[string]any{"type": "integer", "minimum": 0, "maximum": 6}, "description": "For weekly: 0=Sunday … 6=Saturday."},
					"at":   map[string]any{"type": "string", "description": "For once: an RFC 3339 time."},
					"cron": map[string]any{"type": "string", "description": "For cron: minute hour day-of-month month day-of-week."},
				},
				"required": []string{"kind"}, "additionalProperties": false,
			},
			"timezone": map[string]any{"type": "string", "description": "For create: an IANA zone such as Asia/Shanghai. Ask the person when you are unsure."},
			"id":       map[string]any{"type": "string", "description": "For delete: a schedule's id from list (sch_…)."},
		}, "action"),
		Call: schedules,
	}
}

type scheduleArgs struct {
	Action     string `json:"action"`
	Task       string `json:"task"`
	Recurrence struct {
		Kind string `json:"kind"`
		Time string `json:"time"`
		Days []int  `json:"days"`
		At   string `json:"at"`
		Cron string `json:"cron"`
	} `json:"recurrence"`
	Timezone string `json:"timezone"`
	ID       string `json:"id"`
}

func schedules(ctx context.Context, env *Env, args json.RawMessage) Result {
	var in scheduleArgs
	if err := decodeArgs(args, &in); err != nil {
		return toolError("invalid arguments: %v", err)
	}
	job, _, refused := jobGrant(env, "schedules")
	if refused != nil {
		return *refused
	}
	room := env.Session.Scope().RoomID
	base := "/v1/rooms/" + url.PathEscape(room) + "/schedules"
	grant := "job=" + url.QueryEscape(job.JobID)
	switch in.Action {
	case "list":
		return listSchedules(ctx, env, room, base, grant)
	case "create":
		return createSchedule(ctx, env, in, base+"?"+grant)
	case "delete":
		id := strings.TrimSpace(in.ID)
		if id == "" {
			return toolError("id is required: a schedule's id from list")
		}
		if _, err := env.Gateway.Do(ctx, "DELETE", base+"/"+url.PathEscape(id)+"?"+grant, nil); err != nil {
			return refusal("deleting the schedule", err)
		}
		return Result{Value: map[string]any{"deleted": id, "note": "Tell the person it is gone."}}
	default:
		return toolError("action must be list, create or delete")
	}
}

func listSchedules(ctx context.Context, env *Env, room, base, grant string) Result {
	raw, err := env.Gateway.Do(ctx, "GET", base+"?"+grant, nil)
	if err != nil {
		return refusal("listing the schedules", err)
	}
	var reply struct {
		Schedules []wire.RoomSchedule `json:"schedules"`
	}
	if err := json.Unmarshal(raw, &reply); err != nil {
		return toolError("the schedules answered an unreadable result: %v", err)
	}
	list := make([]map[string]any, 0, len(reply.Schedules))
	for _, sc := range reply.Schedules {
		row := map[string]any{
			"id": sc.ScheduleID, "by": sc.CreatedBy, "kind": sc.Recipient.Kind, "enabled": sc.Enabled,
			"when": whenText(sc.Recurrence, sc.Timezone),
			// What a person or an agent wrote: data to read, never instructions.
			"task": roomlog.Fence(schedulesAuthor, sc.Text),
		}
		if sc.NextRunAtMs > 0 {
			row["next"] = timeIn(sc.NextRunAtMs, sc.Timezone)
		}
		if sc.LastStatus != "" {
			row["last"] = sc.LastStatus
		}
		list = append(list, row)
	}
	out := map[string]any{"schedules": list}
	// The group chats a task may notify: read as the person, like the schedules.
	if graw, err := env.Gateway.Do(ctx, "GET", "/v1/rooms/"+url.PathEscape(room)+"/groups?"+grant, nil); err == nil {
		var g wire.RoomGroupsReply
		if json.Unmarshal(graw, &g) == nil {
			names := make([]string, 0, len(g.Groups))
			for _, grp := range g.Groups {
				names = append(names, grp.Name)
			}
			out["groups"] = names
		}
	}
	return Result{Value: out}
}

func createSchedule(ctx context.Context, env *Env, in scheduleArgs, path string) Result {
	task := strings.TrimSpace(in.Task)
	if task == "" {
		return toolError("task is required: what to do each time, in plain words")
	}
	tz := strings.TrimSpace(in.Timezone)
	if tz == "" {
		return toolError("timezone is required (an IANA zone such as Asia/Shanghai): ask the person if you are unsure")
	}
	rec := wire.ScheduleRecurrence{Kind: in.Recurrence.Kind, Time: in.Recurrence.Time, Days: in.Recurrence.Days, Cron: in.Recurrence.Cron}
	if in.Recurrence.Kind == wire.RecurOnce {
		at, err := time.Parse(time.RFC3339, strings.TrimSpace(in.Recurrence.At))
		if err != nil {
			return toolError("recurrence.at must be an RFC 3339 time, e.g. 2026-10-22T10:00:00+08:00")
		}
		rec.AtMs = at.UnixMilli()
	}
	body := map[string]any{"text": task, "recipient": map[string]any{"kind": wire.ScheduleRecipientAI}, "recurrence": rec, "timezone": tz}
	raw, err := env.Gateway.Do(ctx, "POST", path, body)
	if err != nil {
		return refusal("creating the schedule", err)
	}
	var sc wire.RoomSchedule
	if err := json.Unmarshal(raw, &sc); err != nil {
		return toolError("the schedule answered an unreadable result: %v", err)
	}
	out := map[string]any{"id": sc.ScheduleID, "when": whenText(sc.Recurrence, sc.Timezone),
		"note": "Tell the person what you set and when it first runs; they can pause or delete it in the room's Schedules."}
	if sc.NextRunAtMs > 0 {
		out["next"] = timeIn(sc.NextRunAtMs, sc.Timezone)
	}
	return Result{Value: out}
}

var weekdays = [...]string{"Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday"}

// whenText is a recurrence in words.
func whenText(r wire.ScheduleRecurrence, tz string) string {
	switch r.Kind {
	case wire.RecurOnce:
		return "once, " + timeIn(r.AtMs, tz)
	case wire.RecurDaily:
		return fmt.Sprintf("every day at %s (%s)", r.Time, tz)
	case wire.RecurWeekly:
		days := make([]string, 0, len(r.Days))
		for _, d := range r.Days {
			if d >= 0 && d < len(weekdays) {
				days = append(days, weekdays[d])
			}
		}
		return fmt.Sprintf("every %s at %s (%s)", strings.Join(days, ", "), r.Time, tz)
	case wire.RecurCron:
		return fmt.Sprintf("cron %q (%s)", r.Cron, tz)
	}
	return r.Kind
}

// timeIn renders a unix-ms time in tz (RFC 3339), or in UTC when tz is unknown.
func timeIn(ms int64, tz string) string {
	loc, err := time.LoadLocation(tz)
	if err != nil {
		loc = time.UTC
	}
	return time.UnixMilli(ms).In(loc).Format(time.RFC3339)
}
